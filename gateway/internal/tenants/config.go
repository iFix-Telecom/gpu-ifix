// Package tenants: per-tenant configuration snapshot consumed by quota,
// schedule, and dispatcher middleware. The loader builds new TenantConfig
// instances on each Refresh — the struct is intentionally value-typed (no
// pointers, no maps) so it is safe to read lock-free from the atomic
// snapshot (D-C4).
package tenants

import (
	"time"

	"github.com/google/uuid"
)

// TenantConfig is the snapshot row for a single tenant. PeakWindowStart /
// PeakWindowEnd carry only the time-of-day (Hour / Minute) — the date
// portion is always 0001-01-01 UTC and must NOT be consulted.
//
// Location is resolved once by the loader from ScheduleTimezone (IANA
// name) so the request hot path skips time.LoadLocation(). A nil
// Location means the loader could not resolve the zone; schedule/policy.go
// fails-open to Tier0 in that case.
type TenantConfig struct {
	ID        uuid.UUID
	Slug      string
	Name      string
	DataClass string // "normal" | "sensitive" (tenant-scoped, D-C1)
	Status    string // "active" | "disabled"

	// Schedule (D-C1 / D-C4).
	Mode             string         // "24/7" | "peak"
	PeakWindowStart  time.Time      // time-of-day only
	PeakWindowEnd    time.Time      // time-of-day only
	ScheduleTimezone string         // IANA name, e.g. "America/Sao_Paulo"
	Location         *time.Location // cached; nil if LoadLocation failed

	// Quota envelope (per-dimension hard limits; 0 disables).
	DailyQuotaTokens         int64
	MonthlyQuotaTokens       int64
	DailyQuotaAudioMinutes   int
	MonthlyQuotaAudioMinutes int
	DailyQuotaEmbeds         int
	MonthlyQuotaEmbeds       int

	// Rate limits feeding quota.BucketConfig.
	RPSLimit int
	RPMLimit int
}
