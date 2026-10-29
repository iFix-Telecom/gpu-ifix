package alert

// dedup.go — the Redis SET NX EX 300 fingerprint dedup gate for the
// Phase 7 alerting goroutine (OBS-06). One incident = one notification:
// before the alerter fans an alert out to Chatwoot / ClickUp / Brevo, it
// asks dedupShouldSend whether this fingerprint has already been alerted
// inside the 5-minute window.
//
// This file owns the dedup *policy* (the SET NX call, the TTL, the
// fail-open vs fail-closed decision); redisx/alert.go owns the *key
// layout* (the gw:alert:dedup: namespace + the AlertDedupKey builder).
// The split mirrors how shed.go / emerg.go keep their key builders in
// redisx and their FSM logic in their own packages.
//
// # Fail-open vs fail-closed (07-RESEARCH.md Pattern 2)
//
// When Redis itself errors, dedupShouldSend cannot know whether this is
// a duplicate — so it falls back to a severity-dependent default:
//
//   - critical → fail-OPEN  (return true, send anyway): a Redis hiccup
//     must never silence a GPU-down page. A duplicate page is an
//     annoyance; a missed one is an outage nobody noticed.
//   - warning / info → fail-CLOSED (return false, suppress): for the
//     lower tiers, alert fatigue is the larger risk — a Redis outage
//     that also flapped a warning should not flood the on-call inbox.
//
// This is threat T-07-19's documented, accepted tradeoff.

import (
	"context"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

const (
	// dedupTTL is the dedup window. The first time a fingerprint is seen
	// the key is claimed for this long; repeat fingerprints inside the
	// window are suppressed. 5 minutes is long enough to collapse a
	// flapping-incident storm into one notification, short enough that a
	// genuinely recurring problem re-alerts rather than being silently
	// forgotten (07-RESEARCH.md).
	dedupTTL = 5 * time.Minute

	// longCtxDedupTTL é a janela dos fingerprints de contexto longo: um loop
	// de tools gera centenas de requests seguidos; um alerta por hora por
	// tenant basta (card 17u65kmhj0r).
	longCtxDedupTTL = 1 * time.Hour

	// dedupOpTimeout caps the single SET NX Redis call. Matches the 2s
	// redisOpTimeout convention used throughout redisx (shed.go) — the
	// dedup gate must never block the alerter's consume loop for long on
	// a degraded Redis.
	dedupOpTimeout = 2 * time.Second
)

// dedupShouldSend reports whether an alert with the given fingerprint
// should be delivered to external channels, claiming the dedup key as a
// side effect on the first occurrence.
//
//   - SET NX succeeds (key did not exist) → true  (first occurrence; the
//     key is now claimed for dedupTTL)
//   - SET NX reports the key already set  → false (duplicate within the
//     window; skip external channels — the alerter still logs the event)
//   - Redis errors                        → fail-open for SeverityCritical
//     (true), fail-closed for warning / info (false), per T-07-19
//
// The bool is the whole decision — there is no error return. The
// alerter logs a dedup-gate failure separately so the fail-open /
// fail-closed fallback is observable without coupling the caller to
// Redis error handling.
func dedupShouldSend(ctx context.Context, rdb *redis.Client, sev Severity, fingerprint string) bool {
	// A nil client is a wiring bug, but the alerter must still function
	// — treat it like a Redis outage and apply the same fail policy.
	if rdb == nil {
		return sev == SeverityCritical
	}
	ctx, cancel := context.WithTimeout(ctx, dedupOpTimeout)
	defer cancel()

	ttl := dedupTTL
	if strings.HasPrefix(fingerprint, LongContextFingerprintPrefix) {
		ttl = longCtxDedupTTL
	}
	ok, err := rdb.SetNX(ctx, redisx.AlertDedupKey(fingerprint), "1", ttl).Result()
	if err != nil {
		// Redis is degraded — fall back to the severity-dependent default.
		// critical fails OPEN (send anyway); warning/info fail CLOSED.
		return sev == SeverityCritical
	}
	// ok == true  → key was newly set, this is the first occurrence → send.
	// ok == false → key already existed, duplicate within window → suppress.
	return ok
}
