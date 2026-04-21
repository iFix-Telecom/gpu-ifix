// Package schedule (policy.go): pre-dispatch routing decision per tenant.
//
// DecideUpstreamTier returns the tier the dispatcher should target. 24/7
// mode always routes to the local tier-0 (Phase 3's normal fallback chain
// still applies on breaker open). peak mode routes to tier-0 inside the
// business window and skips DIRECTLY to tier-1 OpenRouter outside it
// (D-C2: tier-0 is explicitly skipped even if the breaker is CLOSED —
// the GPU may be suspended to save cost).
package schedule

import (
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

// Tier0 = local primary (VPS GPU); Tier1 = first external (OpenRouter
// Fireworks pin for chat, OpenAI direct for STT/embed per Phase 3 D-C1).
const (
	Tier0 = 0
	Tier1 = 1
)

// DecideUpstreamTier consults the tenant's Mode and clock-in-location to
// pick the target tier. Behaviour:
//
//   - mode != "peak"                                   → Tier0
//   - mode == "peak" AND Location == nil               → Tier0 (fail-open)
//   - mode == "peak" AND now in PeakWindow (inclusive
//     start, exclusive end)                            → Tier0
//   - mode == "peak" AND now outside window            → Tier1
//
// A nil Location fails open to Tier0 rather than Tier1 because we must
// not route an unknown-timezone tenant to external paid infra — the
// assumption would be unsafe.
func DecideUpstreamTier(cfg tenants.TenantConfig, now time.Time) int {
	if cfg.Mode != "peak" {
		return Tier0
	}
	if cfg.Location == nil {
		return Tier0
	}
	local := now.In(cfg.Location)
	if InWindow(local, cfg.PeakWindowStart, cfg.PeakWindowEnd) {
		return Tier0
	}
	return Tier1
}
