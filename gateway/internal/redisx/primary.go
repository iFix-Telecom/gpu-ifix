// Package redisx (primary.go): helpers for the cross-replica primary
// pod mirror introduced by Phase 6.6 (06.6-CONTEXT.md D-08 + 06.6-PATTERNS.md
// §redisx/primary.go).
//
// Authoritative primary FSM state lives in-process in
// gateway/internal/primary.Reconciler (Plan 06.6-06a); these helpers only
// persist a *mirror* in Redis (Hash `gw:primary:state` + Pub/Sub
// `gw:primary:events`) so other replicas + gatewayctl can observe the live
// FSM without reaching into the leader's process memory.
//
// `gw:primary:lock` is a redsync v4 distributed mutex; the leader-elected
// reconciler in internal/primary holds it. NewPrimaryRedsync wraps go-redsync
// v4 so callers do not import redsyncredis/v9 directly — single point of
// truth for the goredis pool adapter (parity with NewEmergRedsync).
//
// Namespace isolation invariant: gw:primary:* keys MUST NOT collide with
// gw:emerg:* keys. Verified by TestPrimaryLockKey_SeparateFromEmergLockKey.
// The two FSMs run side-by-side on the same Redis cluster and a collision
// would cross-clobber leader election + state mirroring.
//
// All helpers use the shared 2-second redisOpTimeout (declared in shed.go) —
// Redis SHOULD NEVER block the reconciler hot path. Callers ignore errors at
// the hot path level and bump a mirror-failures Prometheus counter at the
// publish site (mirror philosophy from breaker.go + shed.go + emerg.go).
package redisx

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-redsync/redsync/v4"
	redsyncredis "github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/redis/go-redis/v9"
)

const (
	// PrimaryEventsChannel is the Pub/Sub channel name for primary FSM
	// transitions and lifecycle events. Cross-replica subscribers consume
	// this channel and feed events into the local view via Plan 06.6-06a
	// (internal/primary/subscribe.go analog).
	PrimaryEventsChannel = "gw:primary:events"

	// primaryStateKeyPrefix + "state" is the single Hash key holding the
	// authoritative-replica's mirror of the live FSM. Single Hash (5
	// fields) — NOT one Hash per upstream like shed.go — because there is
	// only ever 1 primary lifecycle live (primary_live_singleton invariant
	// from migration 0023).
	primaryStateKeyPrefix = "gw:primary:"

	// primaryLockKey is the redsync v4 distributed mutex key. The
	// leader-elected reconciler holds it; non-leader replicas observe
	// state via Pub/Sub. MUST be distinct from emergLockKey ("gw:emerg:lock")
	// so the two reconcilers do not cross-clobber leadership.
	primaryLockKey = "gw:primary:lock"

	// redisOpTimeout is intentionally NOT redeclared here — the
	// package-level constant lives in shed.go (= 2 * time.Second).
	// Re-declaring would be a compile error AND a divergence risk if
	// the value ever changes.
)

// PrimaryStateKey returns the canonical "gw:primary:state" Hash key.
// Wrapped in a function (vs an exported const) to mirror EmergStateKey
// and to leave room for a per-replica key-shard rollout later without
// breaking callsites.
func PrimaryStateKey() string { return primaryStateKeyPrefix + "state" }

// PrimaryLockKey returns the redsync mutex key. Exposed via getter so
// gatewayctl can pretty-print "leader holds gw:primary:lock" without
// reaching into the unexported constant.
func PrimaryLockKey() string { return primaryLockKey }

// PrimaryEvent is the JSON payload published on PrimaryEventsChannel whenever
// the primary FSM transitions or a lifecycle/control event fires. Kept flat +
// small so the JSON unmarshal cost is negligible on rare high-frequency events
// (e.g., schedule_window_entered burst at peak-hour boundary).
//
// PrimaryEvent.Type valid values (06.6-PATTERNS.md §redisx/primary.go +
// 06.6-REVIEWS.md action #3):
//
//   - "schedule_up_fired"     — reconciler scheduler detected window entry
//   - "provisioning_started"  — vast.create_instance call dispatched
//   - "primary_ready"         — pod /health first returned healthy
//   - "draining_started"      — ramp-down window entered
//     (PRIMARY_POD_SCHEDULE_GRACE_RAMP_DOWN_SECONDS)
//   - "destroyed"             — vast.destroy_instance succeeded
//   - "force_up_request"      — reviews #3: consumed by
//     primary.Reconciler.handleForceUpRequest in Plan 06.6-06a
//   - "force_down_request"    — reviews #3: consumed by
//     handleForceDownRequest in Plan 06.6-06a
//   - "cancel_in_flight"      — leader cancelled mid-provisioning
//
// Unlike EmergEvent, PrimaryEvent has no Payload map (Phase 6.6 doesn't carry
// per-event extensions; offer/instance IDs live in the Postgres lifecycle row,
// not in the event channel). Fields stay zero-valued when not relevant.
type PrimaryEvent struct {
	Type        string `json:"type"`
	State       string `json:"state"`
	LifecycleID int64  `json:"lifecycle_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
	SinceUnix   int64  `json:"since_unix"`
	ReplicaID   string `json:"replica_id"`
}

// WritePrimaryState mirrors the current FSM state to Redis as a Hash with
// five fields: state, lifecycle_id, pod_url, pod_instance_id, entered_at.
// Best-effort with a 2-second timeout; callers log failures via a
// mirror-failures counter and continue with the in-process FSM (fail-soft
// philosophy parity with WriteEmergState).
//
// Returns an error on nil client so wiring bugs (mirror constructor invoked
// before NewClient) fail loud at test time.
func WritePrimaryState(ctx context.Context, rdb *redis.Client, state, lifecycleID, podURL, podInstanceID string, enteredUnix int64) error {
	if rdb == nil {
		return fmt.Errorf("redisx: nil client")
	}
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	return rdb.HSet(ctx, PrimaryStateKey(), map[string]any{
		"state":           state,
		"lifecycle_id":    lifecycleID,
		"pod_url":         podURL,
		"pod_instance_id": podInstanceID,
		"entered_at":      enteredUnix,
	}).Err()
}

// PublishPrimaryEvent marshals the event JSON and PUBLISHes to
// PrimaryEventsChannel. 2-second timeout; failures increment the
// mirror-failures counter at the call site.
//
// The Type field encodes the event semantics — see PrimaryEvent doc above
// for the canonical set. Reviews #3 explicitly enumerates force_up_request
// and force_down_request as the gatewayctl manual-override contract.
func PublishPrimaryEvent(ctx context.Context, rdb *redis.Client, ev PrimaryEvent) error {
	if rdb == nil {
		return fmt.Errorf("redisx: nil client")
	}
	ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return rdb.Publish(ctx, PrimaryEventsChannel, payload).Err()
}

// SubscribePrimaryEvents returns a *redis.PubSub attached to
// PrimaryEventsChannel. The caller owns the PubSub and MUST Close() it on
// shutdown or reconnect — the subscribe loop in Plan 06.6-06a handles
// reconnect semantics with a 1-second backoff (mirrors emerg/subscribe.go).
func SubscribePrimaryEvents(ctx context.Context, rdb *redis.Client) *redis.PubSub {
	return rdb.Subscribe(ctx, PrimaryEventsChannel)
}

// NewPrimaryRedsync wraps go-redsync v4 with the goredis/v9 pool adapter
// and returns a *redsync.Redsync ready to mint mutexes for primaryLockKey.
// Single point of truth for the adapter import — Plan 06.6-06a callers use
//
//	rs := redisx.NewPrimaryRedsync(rdb)
//	mtx := rs.NewMutex(redisx.PrimaryLockKey(),
//	    redsync.WithExpiry(30*time.Second),
//	    redsync.WithTries(1),
//	    redsync.WithRetryDelay(0),
//	)
//
// without ever importing "github.com/go-redsync/redsync/v4/redis/goredis/v9"
// themselves.
func NewPrimaryRedsync(rdb *redis.Client) *redsync.Redsync {
	pool := redsyncredis.NewPool(rdb)
	return redsync.New(pool)
}
