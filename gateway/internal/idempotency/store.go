package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// entryTTL is the Stripe-standard 24h window before an idempotency key
// is forgotten (CONTEXT.md D-C2).
const entryTTL = 24 * time.Hour

// inFlightTTL covers worst-case first-token + generation time. The IN_FLIGHT
// sentinel (stored via SET NX EX) holds this TTL; on completion the winner
// replaces the sentinel with the real Entry via SET (overwrite) which then
// inherits entryTTL (24h). If the winner crashes / the sentinel expires,
// subsequent requests retry from scratch (SET NX on expired key succeeds).
const inFlightTTL = 30 * time.Second

// waitPollBudget bounds how long a losing racer will wait for the winner
// before giving up and returning HTTP 409. Matches inFlightTTL — no reason
// to wait longer than the winner could be alive (Codex review [MEDIUM] 02-06).
var waitPollBudget = 30 * time.Second

// waitPollInterval is the backoff between Get polls in the losers loop.
// 100ms keeps latency bounded while sparing Redis — 300 polls over 30s per
// waiter. For N concurrent losers this is N × 300 Redis GETs worst-case.
var waitPollInterval = 100 * time.Millisecond

// inFlightPrefix is the sentinel value prefix written by the winner. Format:
//
//	IN_FLIGHT:{winner_request_id}|hash={sha256_of_body}
//
// Losers parse this to detect hash mismatch (immediate 422) vs matching
// hash (enter wait-poll).
const inFlightPrefix = "IN_FLIGHT:"

// HeaderWhitelist is the set of response headers we persist into the
// cached entry. All other headers are re-generated on replay (notably
// X-Request-ID — replays get a NEW request id from the replayer's request).
var HeaderWhitelist = []string{
	"Content-Type",
	"Content-Length",
	"OpenAI-Organization",
	"OpenAI-Processing-Ms",
}

// Entry is the cached response of a previously completed request.
type Entry struct {
	Status      int               `json:"status"`
	Headers     map[string]string `json:"headers"`
	Body        []byte            `json:"body"`
	RequestHash string            `json:"request_hash"`
	StoredAt    time.Time         `json:"stored_at"`
}

// Store is the Redis-backed idempotency store.
type Store struct {
	redis *redis.Client
}

func NewStore(rdb *redis.Client) *Store {
	return &Store{redis: rdb}
}

// keyFor returns the Redis key for (tenantID, idemKey). Scoped per-tenant
// (D-C1) so two tenants reusing the same idempotency key don't collide.
func keyFor(tenantID, idemKey string) string {
	return fmt.Sprintf("gw:idem:%s:%s", tenantID, idemKey)
}

// SlotKind describes what Get observed at the key:
//
//	SlotEmpty      - no value (first request should acquire)
//	SlotInFlight   - IN_FLIGHT sentinel present (loser should wait-poll or 422 on hash mismatch)
//	SlotCompleted  - real Entry present (replay path)
type SlotKind int

const (
	SlotEmpty SlotKind = iota
	SlotInFlight
	SlotCompleted
)

// Slot is what Get returns. If Kind=InFlight, SentinelHash holds the
// request_hash declared by the winner. If Kind=Completed, Entry holds the
// winner's cached response.
type Slot struct {
	Kind         SlotKind
	Entry        Entry
	SentinelHash string // present only when Kind=InFlight
	WinnerReqID  string // present only when Kind=InFlight (request_id of winner)
}

// Get returns the current state of the idempotency slot. Codex review
// [MEDIUM] 02-06: distinguishes IN_FLIGHT from completed for serialization.
func (s *Store) Get(ctx context.Context, tenantID, idemKey string) (Slot, error) {
	raw, err := s.redis.Get(ctx, keyFor(tenantID, idemKey)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Slot{Kind: SlotEmpty}, nil
	}
	if err != nil {
		return Slot{}, err
	}
	rawStr := string(raw)
	if strings.HasPrefix(rawStr, inFlightPrefix) {
		// Format: IN_FLIGHT:{winner_request_id}|hash={sha256_of_body}
		rest := strings.TrimPrefix(rawStr, inFlightPrefix)
		reqID, hash := "", ""
		if idx := strings.Index(rest, "|hash="); idx >= 0 {
			reqID = rest[:idx]
			hash = rest[idx+len("|hash="):]
		} else {
			reqID = rest
		}
		return Slot{Kind: SlotInFlight, SentinelHash: hash, WinnerReqID: reqID}, nil
	}
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return Slot{}, err
	}
	return Slot{Kind: SlotCompleted, Entry: e}, nil
}

// AcquireInFlight tries to write the IN_FLIGHT sentinel using SET NX EX.
// Returns (true, nil) on successful acquisition (this request is winner);
// (false, nil) if the slot is already occupied (another request is in-flight
// OR completed). Callers must Get immediately after to see what's there.
// Codex review [MEDIUM] 02-06.
func (s *Store) AcquireInFlight(ctx context.Context, tenantID, idemKey, winnerReqID, requestHash string) (bool, error) {
	sentinel := inFlightPrefix + winnerReqID + "|hash=" + requestHash
	return s.redis.SetNX(ctx, keyFor(tenantID, idemKey), sentinel, inFlightTTL).Result()
}

// Complete atomically replaces the IN_FLIGHT sentinel with the real Entry.
// Uses plain SET (overwrite allowed), inheriting entryTTL (24h). Returns
// nil on success; error on Redis/encode failure.
func (s *Store) Complete(ctx context.Context, tenantID, idemKey string, e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return s.redis.Set(ctx, keyFor(tenantID, idemKey), b, entryTTL).Err()
}

// Abort removes the IN_FLIGHT sentinel on upstream-error paths so subsequent
// requests retry (don't get stuck waiting or cache a transient 502).
func (s *Store) Abort(ctx context.Context, tenantID, idemKey string) error {
	return s.redis.Del(ctx, keyFor(tenantID, idemKey)).Err()
}

// WaitForComplete polls Get every waitPollInterval up to waitPollBudget,
// returning the winner's Entry when the slot transitions to Completed.
// Returns:
//
//	(entry, nil)                       — winner completed successfully
//	(Entry{}, ErrConflict)             — completed but hash differs (caller → 422)
//	(Entry{}, ErrInFlightTimeout)      — budget exceeded (caller → 409 + Retry-After)
//	(Entry{}, ctx.Err())               — caller context cancelled
//
// If the in-flight sentinel hash differs from the caller's requestHash at
// ANY point during polling, returns ErrConflict immediately (no waiting).
func (s *Store) WaitForComplete(ctx context.Context, tenantID, idemKey, callerHash string) (Entry, error) {
	deadline := time.Now().Add(waitPollBudget)
	for {
		slot, err := s.Get(ctx, tenantID, idemKey)
		if err != nil {
			return Entry{}, err
		}
		switch slot.Kind {
		case SlotCompleted:
			if slot.Entry.RequestHash != callerHash {
				return Entry{}, ErrConflict
			}
			return slot.Entry, nil
		case SlotInFlight:
			if slot.SentinelHash != "" && slot.SentinelHash != callerHash {
				// In-flight winner has DIFFERENT body — immediate 422, don't wait.
				return Entry{}, ErrConflict
			}
		case SlotEmpty:
			// Sentinel expired / winner aborted — caller should retry as a fresh request.
			return Entry{}, nil
		}
		if time.Now().After(deadline) {
			return Entry{}, ErrInFlightTimeout
		}
		select {
		case <-ctx.Done():
			return Entry{}, ctx.Err()
		case <-time.After(waitPollInterval):
		}
	}
}
