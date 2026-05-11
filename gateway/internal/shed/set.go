// Package shed (set.go): registry of per-upstream FSMs + the
// remote-state overlay used by the cross-replica subscribe loop
// (CONTEXT.md D-C3). Mirrors the exact pattern of
// gateway/internal/breaker/breaker.go Set struct + Rebuild + Get.
//
// Authoritative state per process is the in-process FSM. Redis is a
// mirror (Plan 05-04 mirror.go), never the source of truth. Redis-down
// does NOT stop FSMs from operating — same philosophy as the breaker
// mirror (PATTERNS.md §set.go, CONTEXT.md D-C3).
package shed

import (
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"
)

// Options configures a new Set. DefaultArmSeconds and
// DefaultRecoverSeconds are used when an FSM is constructed for an
// upstream whose circuit_config JSONB does not yet have shed_*
// thresholds populated (D-A4 fallback). 30/60 are the CONTEXT.md D-C1
// strict defaults.
//
// OnChange receives upstream + from/to/reason on every successful FSM
// transition (after CAS) and is the hook the Plan 05-05 tick goroutine
// uses to publish to Redis + Sentry breadcrumbs (D-D4).
type Options struct {
	DefaultArmSeconds     int64
	DefaultRecoverSeconds int64
	OnChange              func(upstream string, from, to State, reason string)
}

// Set aggregates per-upstream FSMs and the remote-state map used by
// the cross-replica subscribe loop. The remoteState entry is set by
// ApplyRemoteEvent and consulted by gatewayctl shed-state for the
// dashboard "remote replica reports…" line — but the dispatcher hot
// path NEVER reads it (the in-process FSM is authoritative; D-C3).
type Set struct {
	rdb     *redis.Client
	log     *slog.Logger // module=SHED — used for Set-level logs
	fsmRoot *slog.Logger // root logger (no module attr) — passed to NewFSM so SHED_FSM is the only module field

	mu          sync.RWMutex
	fsms        map[string]*FSM
	remoteState map[string]State
	onChange    func(upstream string, from, to State, reason string)
	defaultCfg  Config
}

// NewSet creates an empty Set. Call Rebuild to populate FSMs from the
// upstream loader's Names() list. rdb may be nil in tests where the
// Redis mirror is not needed (the in-process FSMs are fully functional
// without Redis — same as the breaker test pattern).
func NewSet(rdb *redis.Client, log *slog.Logger, opt Options) *Set {
	if log == nil {
		log = slog.Default()
	}
	if opt.DefaultArmSeconds == 0 {
		opt.DefaultArmSeconds = 30
	}
	if opt.DefaultRecoverSeconds == 0 {
		opt.DefaultRecoverSeconds = 60
	}
	return &Set{
		rdb:         rdb,
		log:         log.With("module", "SHED"),
		fsmRoot:     log,
		fsms:        make(map[string]*FSM),
		remoteState: make(map[string]State),
		onChange:    opt.OnChange,
		defaultCfg: Config{
			ArmSeconds:     opt.DefaultArmSeconds,
			RecoverSeconds: opt.DefaultRecoverSeconds,
		},
	}
}

// Rebuild adds FSMs for names not yet present and removes FSMs whose
// names no longer appear. Existing FSMs keep their state — this is the
// hot-reload invariant (D-C5): updating one upstream's tier-0 URL must
// not reset another upstream's FSM back to StateOff.
//
// Removed upstreams also drop their remoteState entry so a stale
// "upstream X reports ON" cannot linger after the upstream is deleted.
func (s *Set) Rebuild(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	for n := range s.fsms {
		if !want[n] {
			delete(s.fsms, n)
			delete(s.remoteState, n)
		}
	}
	for _, n := range names {
		if _, ok := s.fsms[n]; !ok {
			name := n // capture for closure
			cb := func(from, to State, reason string) {
				if s.onChange != nil {
					s.onChange(name, from, to, reason)
				}
			}
			cfg := s.defaultCfg
			cfg.Upstream = n
			s.fsms[n] = NewFSM(n, cfg, cb, s.fsmRoot)
		}
	}
}

// Get returns the FSM for name + a found flag. The pointer returned is
// stable across hot-reloads (same pointer for unchanged names) — the
// dispatcher can safely cache it for the request lifetime.
func (s *Set) Get(name string) (*FSM, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.fsms[name]
	return f, ok
}

// State is a hot-path convenience: returns the current state for name,
// or StateOff if the upstream is unknown. Defensive return — a missing
// upstream means "no shedding policy applies" rather than "fail closed".
func (s *Set) State(name string) State {
	if f, ok := s.Get(name); ok {
		return f.State()
	}
	return StateOff
}

// ForEach iterates all FSMs under the RLock. The callback MUST NOT
// block (the lock is held) and MUST NOT mutate the Set (use a separate
// Rebuild after collection if needed).
func (s *Set) ForEach(fn func(upstream string, f *FSM)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for n, f := range s.fsms {
		fn(n, f)
	}
}

// Names returns a snapshot of the currently managed upstream names.
// Used by the tick goroutine (Plan 05-05) for iteration order and by
// gatewayctl shed-state for the dashboard list.
func (s *Set) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.fsms))
	for n := range s.fsms {
		names = append(names, n)
	}
	return names
}

// ApplyRemoteEvent records a state transition reported by another
// replica via the Pub/Sub channel (Plan 05-04 subscribe.go). The
// in-process FSM is NOT forced to that state — the remoteState map is
// used by gatewayctl shed-state for the dashboard "peer reports…" line
// only. Convergence between replicas happens via the periodic
// reconcile loop documented in RESEARCH §Pitfall 3 (Plan 05-04 owns it).
func (s *Set) ApplyRemoteEvent(upstream string, state State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteState[upstream] = state
}

// RemoteState returns the last state reported by any replica for the
// given upstream + a found flag. Used by gatewayctl shed-state to
// render the cross-replica view.
func (s *Set) RemoteState(upstream string) (State, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.remoteState[upstream]
	return st, ok
}
