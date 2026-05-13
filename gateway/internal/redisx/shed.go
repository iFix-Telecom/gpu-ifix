// Package redisx (shed.go): Redis helpers for the Phase 5 shedding
// subsystem mirror introduced by Plan 05-05 (CONTEXT.md D-C3).
//
// Key layout (namespace "gw:shed:"):
//   - gw:shed:{upstream}       — Hash holding {state, since_unix, reason,
//     inflight, p95_ms, vram_mib} for the
//     authoritative in-process FSM state.
//   - gw:shed:events           — Pub/Sub channel for state transitions
//     consumed cross-replica.
//   - gw:shed:force:{upstream} — Operator override shadow key with TTL.
//     Holds "off"|"on" while active.
//
// Semantics: the in-process FSM is authoritative; Redis is a mirror only.
// Redis-down does NOT stop the FSM from operating (CONTEXT.md D-C3, same
// philosophy as the breaker mirror in breaker.go above). All helpers use
// a 2-second op timeout; callers do NOT block the hot path on Redis I/O
// and bump GatewayShedMirrorFailures on error.
//
// Pub/Sub is at-most-once [redis.io/docs/latest/develop/pubsub]; the
// periodic HGETALL reconcile loop in shed/reconcile.go (RESEARCH
// Pitfall 3) closes the convergence gap for Phase 6 forward-compat.
package redisx

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// ShedEventsChannel is the Pub/Sub channel name used for FSM
	// transitions. Cross-replica subscribers consume this channel and
	// call shed.Set.ApplyRemoteEvent on the unmarshaled payload.
	ShedEventsChannel = "gw:shed:events"

	// shedStateKeyPrefix + {upstream} = Hash holding the per-upstream
	// current state mirror.
	shedStateKeyPrefix = "gw:shed:"

	// shedForceKeyPrefix + {upstream} = operator override shadow key
	// with TTL (1h ceiling). Holds the literal target state string
	// "off" or "on".
	shedForceKeyPrefix = "gw:shed:force:"

	// redisOpTimeout caps individual Redis calls. Matches breaker.go.
	redisOpTimeout = 2 * time.Second

	// shedForceTTLCeiling is the maximum TTL accepted by WriteShedForce.
	// Operator overrides MUST expire eventually so a forgotten "force off"
	// during an incident cannot disable shedding permanently
	// (threat T-05-09 mitigation).
	shedForceTTLCeiling = 1 * time.Hour
)

// ShedEvent is the JSON payload published on ShedEventsChannel whenever
// a local FSM transitions. Kept flat + small so the unmarshal cost is
// negligible on high-frequency transitions.
//
// Signals may be nil if the publisher could not capture them (e.g.,
// synthetic transitions from gatewayctl shed-force).
type ShedEvent struct {
	Upstream  string            `json:"upstream"`
	State     string            `json:"state"`      // "off" | "armed" | "on" | "recovering"
	SinceUnix int64             `json:"since_unix"` // time.Now().Unix() at transition
	Reason    string            `json:"reason,omitempty"`
	Signals   *ShedEventSignals `json:"signals,omitempty"`
}

// ShedEventSignals is the optional observed-signals snapshot the
// publisher captured at transition time. Used by the Phase 7 dashboard
// to render "FSM tripped because inflight=8, p95=2500ms, vram=22000 MiB".
type ShedEventSignals struct {
	Inflight int64  `json:"inflight,omitempty"`
	P95Ms    uint32 `json:"p95_ms,omitempty"`
	VramMiB  int64  `json:"vram_mib,omitempty"`
}

// ShedStateKey returns "gw:shed:{upstream}".
func ShedStateKey(upstream string) string { return shedStateKeyPrefix + upstream }

// ShedForceKey returns "gw:shed:force:{upstream}".
func ShedForceKey(upstream string) string { return shedForceKeyPrefix + upstream }

// WriteShedState mirrors the current FSM state to Redis as a Hash.
// Best-effort with a 2-second timeout; callers log failures via
// GatewayShedMirrorFailures and continue with the in-process FSM.
//
// Returns an error on nil client so wiring bugs (mirror constructor
// invoked before NewClient) fail loud at test time.
func WriteShedState(ctx context.Context, rdb *redis.Client, upstream, state, reason string, sinceUnix int64, sig *ShedEventSignals) error {
	if rdb == nil {
		return fmt.Errorf("redisx: nil client")
	}
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	fields := map[string]any{
		"state":      state,
		"since_unix": sinceUnix,
		"reason":     reason,
	}
	if sig != nil {
		fields["inflight"] = sig.Inflight
		fields["p95_ms"] = sig.P95Ms
		fields["vram_mib"] = sig.VramMiB
	}
	return rdb.HSet(ctx, ShedStateKey(upstream), fields).Err()
}

// ReadShedState reads the Hash mirror back. Returns (map, nil) on
// success and (nil, nil) when the key does not exist — a fresh boot
// before any local transition has happened is the typical case.
func ReadShedState(ctx context.Context, rdb *redis.Client, upstream string) (map[string]string, error) {
	if rdb == nil {
		return nil, fmt.Errorf("redisx: nil client")
	}
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	m, err := rdb.HGetAll(ctx, ShedStateKey(upstream)).Result()
	if err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}

// PublishShedEvent marshals the event JSON and PUBLISHes to
// ShedEventsChannel. 2-second timeout; failures increment
// GatewayShedMirrorFailures at the call site.
func PublishShedEvent(ctx context.Context, rdb *redis.Client, ev ShedEvent) error {
	if rdb == nil {
		return fmt.Errorf("redisx: nil client")
	}
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return rdb.Publish(ctx, ShedEventsChannel, payload).Err()
}

// SubscribeShedEvents returns a *redis.PubSub attached to
// ShedEventsChannel. The caller owns the PubSub and MUST Close() it on
// shutdown or reconnect — the shed/subscribe.go consumer loop handles
// reconnect semantics with a 1-second backoff.
func SubscribeShedEvents(ctx context.Context, rdb *redis.Client) *redis.PubSub {
	return rdb.Subscribe(ctx, ShedEventsChannel)
}

// WriteShedForce sets the operator override shadow key with a bounded
// TTL. state must be "off" or "on" — the ticker (shed/tick.go) reads
// this key on every iteration and drives the FSM to the override state
// before the 2-of-3 evaluation runs (CONTEXT.md D-C5).
//
// TTL ceiling is 1 hour: a forgotten "force off" during an incident
// MUST NOT permanently disable shedding (threat T-05-09 mitigation).
func WriteShedForce(ctx context.Context, rdb *redis.Client, upstream, state string, ttl time.Duration) error {
	if rdb == nil {
		return fmt.Errorf("redisx: nil client")
	}
	if state != "off" && state != "on" {
		return fmt.Errorf("redisx: invalid shed-force state %q (want off|on)", state)
	}
	if ttl <= 0 || ttl > shedForceTTLCeiling {
		return fmt.Errorf("redisx: shed-force TTL %s out of range (0 < ttl <= %s)", ttl, shedForceTTLCeiling)
	}
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	return rdb.Set(ctx, ShedForceKey(upstream), state, ttl).Err()
}

// GetShedForce returns (state, remainingTTL, true) when an override is
// active for the upstream, or ("", 0, false) when no key is present or
// any Redis error occurs. Errors are silently treated as "no override"
// — the FSM continues with its normal evaluation, which is the safe
// default when the mirror is degraded (CONTEXT.md D-C3 fail-open).
func GetShedForce(ctx context.Context, rdb *redis.Client, upstream string) (string, time.Duration, bool) {
	if rdb == nil {
		return "", 0, false
	}
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	state, err := rdb.Get(ctx, ShedForceKey(upstream)).Result()
	if err != nil {
		return "", 0, false
	}
	ttl, err := rdb.PTTL(ctx, ShedForceKey(upstream)).Result()
	if err != nil || ttl < 0 {
		ttl = 0
	}
	return state, ttl, true
}

// DeleteShedForce clears an active override. Used by
// `gatewayctl shed-force clear` (Plan 05-07) and by tests.
func DeleteShedForce(ctx context.Context, rdb *redis.Client, upstream string) error {
	if rdb == nil {
		return fmt.Errorf("redisx: nil client")
	}
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	return rdb.Del(ctx, ShedForceKey(upstream)).Err()
}

// AllShedStateKeys returns every "gw:shed:{upstream}" Hash key currently
// present in Redis, excluding the "gw:shed:force:*" namespace. Used by
// the periodic reconcile loop (shed/reconcile.go) to detect upstream
// state keys produced by other replicas.
//
// Uses SCAN (not KEYS) to avoid blocking Redis on large keyspaces.
func AllShedStateKeys(ctx context.Context, rdb *redis.Client) ([]string, error) {
	if rdb == nil {
		return nil, fmt.Errorf("redisx: nil client")
	}
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	var keys []string
	var cursor uint64
	for {
		batch, next, err := rdb.Scan(ctx, cursor, "gw:shed:*", 100).Result()
		if err != nil {
			return nil, err
		}
		for _, k := range batch {
			// Filter out force keys ("gw:shed:force:*") that share the
			// "gw:shed:*" prefix but are NOT state keys.
			if strings.HasPrefix(k, shedForceKeyPrefix) {
				continue
			}
			keys = append(keys, k)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return keys, nil
}
