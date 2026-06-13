package alert

// alerter.go — the OBS-04/05/06 brain: the Run(ctx) goroutine that
// subscribes the three gateway event streams (gw:breaker:events,
// gw:shed:events, gw:emerg:events) on ONE Redis connection, classifies
// each event into a severity tier (severity.go), dedups it against the
// 5-minute window (dedup.go), and fans the survivors out to the
// Chatwoot / ClickUp / Brevo channels (client.go).
//
// # The non-blocking invariant (threat T-07-15, 07-RESEARCH.md Pitfall 5)
//
// The consume loop NEVER calls Channel.Send directly. It classifies +
// dedups + ENQUEUES a Message onto a bounded per-channel worker queue,
// then immediately goes back to draining Redis. Each channel has its
// own buffered queue + a single worker goroutine that drains it and
// does the actual (slow, network-bound) Send. Consequences:
//
//   - a dead Chatwoot cannot stall event consumption — its worker
//     blocks on the breaker timeout, the consume loop does not;
//   - a full worker queue bumps obs.AlertDroppedTotal and the loop
//     continues — back-pressure is a counter, not a stall.
//
// # The reconnect loop (breaker/subscribe.go canonical pattern)
//
// Run copies the documented canonical Pub/Sub-with-redis-go skeleton:
// an outer for{} reconnect loop, an inner select on ctx.Done()+msg,
// malformed JSON → log.Warn + continue (never crash), channel drop →
// Close() + 1s backoff + reconnect, ctx cancel → clean return.
//
// # Boot reconciliation (threat T-07-18, Pitfall 4)
//
// Redis Pub/Sub is at-most-once with no replay: a transition that fires
// during the boot gap is lost. ReconcileBoot reads the current emergency
// FSM state Hash on startup so a gateway restart DURING an active
// incident still surfaces a critical alert. main.go (plan 07-06) calls
// ReconcileBoot once, then spawns Run early — before the breaker / shed
// / emerg subsystems start publishing.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

const (
	// channelQueueDepth is the buffered depth of each per-channel worker
	// queue. 64 absorbs a short incident burst (a flap publishing the
	// same handful of fingerprints) without dropping; once full,
	// additional fan-outs bump obs.AlertDroppedTotal instead of blocking
	// the consume loop (Pitfall 5). Matches shed/mirror.go's queue depth.
	channelQueueDepth = 64

	// reconnectBackoff is the pause before re-subscribing after the
	// Pub/Sub channel drops. Matches breaker/subscribe.go.
	reconnectBackoff = 1 * time.Second
)

// channelJob is the payload enqueued onto a per-channel worker queue.
type channelJob struct {
	msg Message
}

// channelWorker is one bounded delivery pipeline: a buffered job queue
// drained by a single goroutine that calls the wrapped Channel.Send.
type channelWorker struct {
	ch   Channel
	jobs chan channelJob
}

// Alerter consumes the gateway's three Redis Pub/Sub event streams and
// fans deduplicated, severity-tiered alerts out to its channels. Safe to
// run as a single goroutine via Run; the per-channel workers it spawns
// are the only place Channel.Send is ever called.
type Alerter struct {
	rdb     *redis.Client
	workers map[string]*channelWorker // channel name → bounded worker
	log     *slog.Logger
}

// NewAlerter builds an Alerter from a Redis client, the set of enabled
// delivery channels, and a logger. Channels are indexed by Name() so the
// fan-out can look up the worker for each name returned by channelsFor.
// A disabled channel is simply absent from the slice — the alerter then
// has no worker for that name and skips it in the fan-out (logging a
// debug line), which is the intended "channel not configured" behavior.
//
// The per-channel worker goroutines are NOT started here — Run starts
// them, so an Alerter that is constructed but never Run spawns nothing.
func NewAlerter(rdb *redis.Client, channels []Channel, log *slog.Logger) *Alerter {
	if log == nil {
		log = slog.Default()
	}
	workers := make(map[string]*channelWorker, len(channels))
	for _, c := range channels {
		if c == nil {
			continue
		}
		workers[c.Name()] = &channelWorker{
			ch:   c,
			jobs: make(chan channelJob, channelQueueDepth),
		}
	}
	return &Alerter{
		rdb:     rdb,
		workers: workers,
		log:     log.With("module", "ALERTER"),
	}
}

// Run starts the per-channel worker goroutines and then enters the
// canonical Pub/Sub reconnect loop, subscribing all three event channels
// on ONE connection. It blocks until ctx is cancelled, then returns
// cleanly. Designed to be invoked once at boot inside its own goroutine
// (`go alerter.Run(rootCtx)`), spawned EARLY — before the subsystems
// that publish — so a transition during boot is not silently lost
// (Pitfall 4).
func (a *Alerter) Run(ctx context.Context) {
	log := a.log.With("subsystem", "run")

	// Start one drain goroutine per channel. Each worker is the ONLY
	// place Channel.Send is called — the consume loop never blocks on a
	// slow external API.
	for name, w := range a.workers {
		go a.runWorker(ctx, name, w)
	}

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		// One Subscribe call, three channels — msg.Channel discriminates
		// the payload shape downstream in severityFor.
		ps := a.rdb.Subscribe(ctx,
			redisx.BreakerEventsChannel(),
			redisx.ShedEventsChannel,
			redisx.EmergEventsChannel,
			// Phase 12 Plan 02 (D-03 / FINDING 1): PrimaryEvents was subscribed
			// by nobody before this plan — a primary_death_confirmed event was
			// published but never fanned out to an operator. Adding it here wires
			// the distinct billing-stop vs host-death critical alert.
			redisx.PrimaryEventsChannel,
		)
		ch := ps.Channel()
		drained := false
		for !drained {
			select {
			case <-ctx.Done():
				_ = ps.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					drained = true
					break
				}
				a.handle(ctx, msg.Channel, []byte(msg.Payload))
			}
		}
		_ = ps.Close()
		log.Warn("alert pubsub channel closed; reconnecting")
		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectBackoff):
		}
	}
}

// handle classifies one raw event, applies the dedup gate, and enqueues
// the survivors onto their per-channel worker queues. It NEVER calls
// Channel.Send — that is the worker's job. handle is the body of the
// consume loop, so it must return promptly: every branch is either a
// log + return or a non-blocking enqueue.
func (a *Alerter) handle(ctx context.Context, channelName string, payload []byte) {
	a.handleEvent(ctx, channelName, payload, false)
}

// handleEvent is the body of handle. bypassDedup, when true, skips the
// dedup gate entirely and always fans out — used by ReconcileBoot for an
// active critical incident found at startup (WR-03): a gateway crash
// DURING an unacknowledged incident is the single most important moment
// to re-surface the page, and the live alerter's still-set 5-minute
// dedup key would otherwise silently suppress it. A duplicate page is an
// annoyance; a silenced active incident after a crash is an outage
// nobody is looking at.
func (a *Alerter) handleEvent(ctx context.Context, channelName string, payload []byte, bypassDedup bool) {
	sev, msg, err := severityFor(channelName, payload)
	if err != nil {
		// Malformed (or hostile) payload — log a WARN and move on. A bad
		// JSON byte sequence can never crash the loop or be reflected
		// into a Send (threat T-07-17).
		a.log.Warn("malformed alert event; dropping",
			"channel", channelName, "payload", string(payload), "err", err)
		return
	}

	// The dedup gate is checked even for info-tier events: it is the
	// reason the event is "logged but not re-sent". An info event fans
	// out to zero channels anyway, but running it through the gate keeps
	// the log line consistent and claims the fingerprint so a later
	// promotion of the same incident to warning/critical still dedups.
	//
	// bypassDedup skips the gate: ReconcileBoot must re-page an active
	// critical incident even when the live alerter already claimed the
	// fingerprint before the crash (WR-03).
	if !bypassDedup && !dedupShouldSend(ctx, a.rdb, sev, msg.Fingerprint) {
		a.log.Info("alert deduplicated; external channels skipped",
			"severity", string(sev), "fingerprint", msg.Fingerprint)
		return
	}
	if bypassDedup {
		a.log.Info("alert dedup gate bypassed; surfacing active incident",
			"severity", string(sev), "fingerprint", msg.Fingerprint)
	}

	targets := channelsFor(sev)
	if len(targets) == 0 {
		// info-tier (or unknown) — recorded for the dashboard/log only,
		// no external fan-out. This is not a drop; it is the matrix.
		a.log.Info("alert classified; no external channels for tier",
			"severity", string(sev), "fingerprint", msg.Fingerprint, "title", msg.Title)
		return
	}

	a.log.Info("alert classified; dispatching",
		"severity", string(sev), "fingerprint", msg.Fingerprint,
		"title", msg.Title, "channels", targets)

	for _, name := range targets {
		w, ok := a.workers[name]
		if !ok {
			// The tier wants this channel but it is not configured /
			// enabled — skip it. Not a drop (the queue is not full); the
			// channel simply does not exist in this deployment.
			a.log.Debug("alert channel not configured; skipping",
				"channel", name, "fingerprint", msg.Fingerprint)
			continue
		}
		select {
		case w.jobs <- channelJob{msg: msg}:
			// Enqueued — the worker will Send it. Consume loop moves on.
		default:
			// Worker queue full — the channel is draining slower than
			// alerts arrive. Drop THIS fan-out (the in-process loop is
			// authoritative; we never block it) and bump the
			// back-pressure counter so dashboards see the incident
			// (Pitfall 5 / threat T-07-15).
			obs.AlertDroppedTotal.Inc()
			a.log.Warn("alert channel queue full; dropping fan-out",
				"channel", name, "fingerprint", msg.Fingerprint)
		}
	}
}

// runWorker drains one channel's job queue and performs the actual
// Channel.Send. This is the ONLY callsite of Channel.Send in the
// alerter — isolating it here is what keeps the consume loop
// non-blocking. The worker exits when ctx is cancelled.
//
// A Send error is advisory: it is logged + counted (obs.AlertSendsTotal
// with result="err") but never propagated — a failed channel must not
// stall the others, and the concrete client's own circuit breaker is
// what makes a dead provider fail fast.
func (a *Alerter) runWorker(ctx context.Context, name string, w *channelWorker) {
	log := a.log.With("subsystem", "worker", "channel", name)
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-w.jobs:
			err := w.ch.Send(ctx, job.msg)
			result := "ok"
			if err != nil {
				result = "err"
				log.Warn("alert channel send failed",
					"fingerprint", job.msg.Fingerprint, "err", err)
			} else {
				log.Debug("alert channel send ok",
					"fingerprint", job.msg.Fingerprint)
			}
			obs.AlertSendsTotal.WithLabelValues(name, result).Inc()
		}
	}
}

// ReconcileBoot surfaces a critical alert if the gateway is restarting
// DURING an active emergency incident. Redis Pub/Sub is at-most-once: a
// transition into emergency_active that fired while this process was
// down (or before Run subscribed) is gone. ReconcileBoot closes that
// gap by reading the emergency FSM state mirror Hash (gw:emerg:state)
// once at boot and, if the state is an alert-worthy one, synthesising
// the corresponding event and pushing it through the same
// classify → fan-out path as a live event.
//
// WR-03 — dedup policy at boot is severity-dependent:
//
//   - critical state → BYPASS the dedup gate (always re-page). A gateway
//     crash during an unacknowledged critical incident is precisely the
//     moment the on-call most needs the page re-surfaced; the live
//     alerter's still-set 5-minute dedup key would otherwise silence it.
//     A duplicate page is strictly less bad than a silenced live outage.
//   - warning state → the dedup gate still applies: a fast restart
//     during a mere degraded state should not double-page the lower
//     tier, where alert fatigue is the larger risk.
//
// Best-effort: a Redis error or an absent / unparseable state Hash is
// logged and ignored — boot reconciliation must never block startup.
// Call ReconcileBoot once, BEFORE spawning Run's goroutine ideally, but
// after the worker goroutines exist (so the fan-out has somewhere to
// enqueue) — in practice main.go calls it right after `go a.Run(ctx)`
// and the small race is harmless.
func (a *Alerter) ReconcileBoot(ctx context.Context) {
	log := a.log.With("subsystem", "reconcile-boot")
	if a.rdb == nil {
		log.Warn("reconcile-boot skipped: nil Redis client")
		return
	}

	rctx, cancel := context.WithTimeout(ctx, dedupOpTimeout)
	state, err := a.rdb.HGetAll(rctx, redisx.EmergStateKey()).Result()
	cancel()
	if err != nil {
		log.Warn("reconcile-boot: failed to read emergency state mirror", "err", err)
		return
	}
	if len(state) == 0 {
		log.Debug("reconcile-boot: no emergency state mirror; nothing to reconcile")
		return
	}

	stateName := state["state"]
	if !emergCriticalStates[stateName] && !emergWarningStates[stateName] {
		log.Debug("reconcile-boot: emergency FSM in a benign state; nothing to surface",
			"state", stateName)
		return
	}

	// Synthesise the same EmergEvent shape a live "transition" would
	// publish, then run it through the identical handle() path so the
	// classification, dedup, and fan-out are exactly consistent with a
	// real event.
	ev := redisx.EmergEvent{
		Type:  "transition",
		State: stateName,
	}
	if lid := state["lifecycle_id"]; lid != "" {
		ev.Reason = "boot reconciliation (gateway restarted mid-incident)"
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		log.Warn("reconcile-boot: failed to marshal synthetic event", "err", err)
		return
	}
	// WR-03: bypass the dedup gate for an active CRITICAL state so a
	// crash-during-incident always re-pages; keep the gate for warning
	// states so a fast restart does not double-page the lower tier.
	bypassDedup := emergCriticalStates[stateName]
	log.Info("reconcile-boot: surfacing alert for active incident found at startup",
		"state", stateName, "bypass_dedup", bypassDedup)
	a.handleEvent(ctx, redisx.EmergEventsChannel, payload, bypassDedup)
}
