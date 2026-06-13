package upstreams

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// UpstreamConfig is one row of ai_gateway.upstreams resolved to live
// runtime values. URL and AuthBearer are filled from os.Getenv at Refresh
// time; they are NOT persisted in the DB — only the env var names are.
//
// AuthBearer is tagged json:"-" so it is NEVER serialized into log lines,
// /v1/health/upstreams payloads, or gatewayctl JSON output (T-03-04-03
// mitigation in 03-04-PLAN.md threat model).
//
// IsEmergency is set ONLY in the ephemeral copy returned by Resolve when
// a tier-0 emergency override is active (Plan 06-08, D-E3). Persisted
// snapshot rows always have IsEmergency=false. Dispatcher consults this
// flag to decide whether to call EmergTrafficRegistrar.RegisterTraffic
// (replaces fragile string-prefix matching on Name per W9 revision).
type UpstreamConfig struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Role string    `json:"role"` // "llm" | "stt" | "embed" | "tts"
	Tier int       `json:"tier"` // 0 = primary, 1 = fallback
	// TierPriority orders rows that share the same (Role, Tier). Lower
	// wins. Phase 11.2 (D-B5′/D-B6′) — STT cascade has 3 tier-1 rows
	// ordered gemini-stt(10) → groq-whisper(15) → openai-whisper(20).
	// Other roles default to 0 (single tier-1 row, backward-compat).
	TierPriority  int           `json:"tier_priority"`
	URL           string        `json:"url"`
	AuthBearer    string        `json:"-"` // resolved; NEVER log/serialize
	AuthBearerEnv string        `json:"auth_bearer_env,omitempty"`
	Enabled       bool          `json:"enabled"`
	Weight        *int32        `json:"weight,omitempty"` // Phase 5 populates
	CircuitConfig CircuitConfig `json:"circuit_config"`

	// IsEmergency is true when this UpstreamConfig was synthesised by
	// Resolve from an active tier-0 override (Loader.OverrideTier0). The
	// snapshot stored in Loader NEVER carries this flag — it is added in
	// the ephemeral return value only. Dispatcher reads it to register
	// traffic with the emergency reconciler (idle-grace detection).
	IsEmergency bool `json:"-"`
}

// CircuitConfig overrides breaker defaults for a specific upstream. Loaded
// from the JSONB column ai_gateway.upstreams.circuit_config. Zero values
// (0 failures, 0 cooldown) mean "use defaults" — the dispatcher merges
// with breaker.DefaultOptions() at Execute time.
//
// CooldownS is the on-disk representation (seconds, JSON-friendly);
// Cooldown is the parsed time.Duration computed by parseCircuitConfig and
// is NOT serialized back to JSON (json:"-").
//
// Phase 5 saturation fields (Shed*) follow the same wire pattern: the
// *Seconds variant is what migration 0017 writes to JSONB; the derived
// time.Duration fields (ShedArm, ShedRecover) are computed in
// parseCircuitConfig and are NOT serialized back. Zero values disable the
// shed feature for the upstream (shed.fsm.Evaluate skips evaluation when
// ShedInflightMax <= 0).
//
// CRITICAL UNIT NOTE: ShedVramUsedMiB is in MiB (DCGM_FI_DEV_FB_USED
// native unit; RESEARCH.md Pitfall 1). Migration 0017 writes 21504 (=21 GB)
// — never write a bytes value here.
type CircuitConfig struct {
	Failures  uint32        `json:"failures,omitempty"`
	Cooldown  time.Duration `json:"-"`
	CooldownS int           `json:"cooldown_s,omitempty"` // DB stores seconds

	// Phase 5 — saturation thresholds (D-A4). Zero values disable shedding
	// for this upstream. shed_vram_used_mib is the JSON wire name (NOT
	// shed_vram_used_bytes — see RESEARCH.md Pitfall 1).
	ShedInflightMax    int           `json:"shed_inflight_max,omitempty"`
	ShedP95Ms          int           `json:"shed_p95_ms,omitempty"`
	ShedVramUsedMiB    int64         `json:"shed_vram_used_mib,omitempty"`
	ShedArmSeconds     int           `json:"shed_arm_seconds,omitempty"`
	ShedRecoverSeconds int           `json:"shed_recover_seconds,omitempty"`
	ShedArm            time.Duration `json:"-"` // derived from ShedArmSeconds
	ShedRecover        time.Duration `json:"-"` // derived from ShedRecoverSeconds
}

// RoleTier keys the by-role-tier snapshot map. String-serializable for
// debug printing via fmt.Stringer.
type RoleTier struct {
	Role string
	Tier int
}

// String implements fmt.Stringer so RoleTier can be logged inline as
// e.g. "llm:0" without manual concatenation.
func (rt RoleTier) String() string {
	return rt.Role + ":" + strconv.Itoa(rt.Tier)
}

// parseCircuitConfig unmarshals the JSONB column into a CircuitConfig.
// Empty bytes or invalid JSON → zero-value CircuitConfig (dispatcher uses
// breaker defaults). Cooldown is computed from CooldownS (seconds) so
// downstream callers can use the time.Duration directly.
func parseCircuitConfig(raw []byte) CircuitConfig {
	var cc CircuitConfig
	if len(raw) == 0 {
		return cc
	}
	if err := json.Unmarshal(raw, &cc); err != nil {
		return CircuitConfig{} // swallow parse errors; defaults will apply
	}
	cc.Cooldown = time.Duration(cc.CooldownS) * time.Second
	// Phase 5 — derive time.Duration helpers from on-disk seconds.
	// Zero ShedArmSeconds / ShedRecoverSeconds means "disabled" — the FSM
	// hand-rolled in internal/shed/fsm.go skips evaluation in that case.
	cc.ShedArm = time.Duration(cc.ShedArmSeconds) * time.Second
	cc.ShedRecover = time.Duration(cc.ShedRecoverSeconds) * time.Second
	return cc
}
