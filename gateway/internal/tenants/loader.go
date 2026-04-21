// Package tenants: atomic.Pointer-snapshot loader for the ai_gateway.tenants
// table. Mirrors the upstreams.Loader pattern in gateway/internal/upstreams/
// loader.go: Refresh rebuilds the snapshot from scratch and Stores the new
// pointer; Get / GetBySlug read the snapshot lock-free.
package tenants

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// snapshot is the immutable view stored under atomic.Pointer. Replaced
// wholesale on each Refresh; never mutated in place.
type snapshot struct {
	byID   map[uuid.UUID]TenantConfig
	bySlug map[string]TenantConfig
}

// loaderQueries isolates the sqlc surface for testability (mirrors the
// pattern in upstreams.loaderQueries).
type loaderQueries interface {
	ListTenantsForLoader(ctx context.Context) ([]gen.ListTenantsForLoaderRow, error)
	CountSensitivePeakInvariant(ctx context.Context) (int64, error)
}

// Loader holds the in-memory authoritative snapshot of the tenants table.
// Refresh is called at boot and on each LISTEN/NOTIFY tenants_changed event.
type Loader struct {
	pool      *pgxpool.Pool
	q         loaderQueries
	snap      atomic.Pointer[snapshot]
	log       *slog.Logger
	defaultTZ *time.Location
}

// NewLoader constructs the Loader and performs the initial Refresh. Boot
// MUST fail-fast if the initial SELECT cannot complete (returns error).
//
// defaultTZ is used when a tenant's schedule_timezone fails LoadLocation;
// pass time.UTC if no specific fallback is appropriate.
func NewLoader(ctx context.Context, pool *pgxpool.Pool, defaultTZ *time.Location, log *slog.Logger) (*Loader, error) {
	l := &Loader{
		pool:      pool,
		q:         gen.New(pool),
		log:       log.With("module", "TENANTS"),
		defaultTZ: defaultTZ,
	}
	if err := l.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("tenants: initial refresh: %w", err)
	}
	return l, nil
}

// Refresh loads all active rows, builds a fresh snapshot, and atomically
// swaps it in. Emits obs.GatewayTenantsReload with result="ok" on success
// or "error" on failure.
func (l *Loader) Refresh(ctx context.Context) error {
	rows, err := l.q.ListTenantsForLoader(ctx)
	if err != nil {
		obs.GatewayTenantsReload.WithLabelValues("error").Inc()
		return fmt.Errorf("tenants: list: %w", err)
	}
	s := &snapshot{
		byID:   make(map[uuid.UUID]TenantConfig, len(rows)),
		bySlug: make(map[string]TenantConfig, len(rows)),
	}
	for _, r := range rows {
		tz := l.defaultTZ
		if r.ScheduleTimezone != "" {
			if loc, lerr := time.LoadLocation(r.ScheduleTimezone); lerr == nil {
				tz = loc
			} else {
				l.log.Warn("tenant has invalid timezone, falling back",
					"tenant_slug", r.Slug, "tz", r.ScheduleTimezone, "err", lerr)
			}
		}
		cfg := TenantConfig{
			ID:                       r.ID,
			Slug:                     r.Slug,
			Name:                     r.Name,
			DataClass:                coerceDataClass(r.DataClass),
			Status:                   r.Status,
			Mode:                     r.Mode,
			PeakWindowStart:          pgTimeToClock(r.PeakWindowStart),
			PeakWindowEnd:            pgTimeToClock(r.PeakWindowEnd),
			ScheduleTimezone:         r.ScheduleTimezone,
			Location:                 tz,
			DailyQuotaTokens:         r.DailyQuotaTokens,
			MonthlyQuotaTokens:       r.MonthlyQuotaTokens,
			DailyQuotaAudioMinutes:   int(r.DailyQuotaAudioMinutes),
			MonthlyQuotaAudioMinutes: int(r.MonthlyQuotaAudioMinutes),
			DailyQuotaEmbeds:         int(r.DailyQuotaEmbeds),
			MonthlyQuotaEmbeds:       int(r.MonthlyQuotaEmbeds),
			RPSLimit:                 int(r.RpsLimit),
			RPMLimit:                 int(r.RpmLimit),
		}
		s.byID[r.ID] = cfg
		s.bySlug[r.Slug] = cfg
	}
	l.snap.Store(s)
	obs.GatewayTenantsReload.WithLabelValues("ok").Inc()
	l.log.Info("tenants refreshed", "rows", len(s.byID))
	return nil
}

// Get returns the snapshot row for tenantID. Lock-free. Returns
// ErrTenantNotFound if the snapshot is nil (loader never successfully
// completed a Refresh) or if the ID is absent.
func (l *Loader) Get(tenantID uuid.UUID) (TenantConfig, error) {
	s := l.snap.Load()
	if s == nil {
		return TenantConfig{}, ErrTenantNotFound
	}
	cfg, ok := s.byID[tenantID]
	if !ok {
		return TenantConfig{}, ErrTenantNotFound
	}
	return cfg, nil
}

// GetBySlug returns the snapshot row for the given slug. Lock-free.
func (l *Loader) GetBySlug(slug string) (TenantConfig, error) {
	s := l.snap.Load()
	if s == nil {
		return TenantConfig{}, ErrTenantNotFound
	}
	cfg, ok := s.bySlug[slug]
	if !ok {
		return TenantConfig{}, ErrTenantNotFound
	}
	return cfg, nil
}

// All returns a defensive copy of every tenant in the current snapshot,
// sorted nowhere in particular (map iteration order). Used by gatewayctl
// and /admin/* handlers that list tenants.
func (l *Loader) All() []TenantConfig {
	s := l.snap.Load()
	if s == nil {
		return nil
	}
	out := make([]TenantConfig, 0, len(s.byID))
	for _, cfg := range s.byID {
		out = append(out, cfg)
	}
	return out
}

// CheckSensitivePeakInvariant runs the boot-time LGPD check (D-C1 path 3).
// Returns ErrSensitivePeakInvariant if any row violates mode='peak' AND
// data_class='sensitive'. Caller (cmd/gateway/main.go) should os.Exit(1)
// when this error is observed.
func (l *Loader) CheckSensitivePeakInvariant(ctx context.Context) error {
	n, err := l.q.CountSensitivePeakInvariant(ctx)
	if err != nil {
		return fmt.Errorf("tenants: invariant check: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("%w: %d row(s) found", ErrSensitivePeakInvariant, n)
	}
	return nil
}

// pgTimeToClock converts a pgtype.Time (microseconds since midnight) to a
// time.Time whose Hour and Minute reflect the stored time-of-day. The date
// portion is 0001-01-01 UTC; callers MUST only read Hour/Minute.
//
// If the pgtype.Time is !Valid, returns the zero Time (midnight).
func pgTimeToClock(t pgtype.Time) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	totalSec := int(t.Microseconds / 1_000_000)
	h := totalSec / 3600
	m := (totalSec % 3600) / 60
	return time.Date(1, 1, 1, h, m, 0, 0, time.UTC)
}

// coerceDataClass turns sqlc's interface{}-typed data_class column (driven
// by the ai_gateway.data_class ENUM; pgx delivers it as []byte or string)
// into a plain Go string. Unknown shapes return "" — callers should treat
// that as "normal" per D-C1 default.
func coerceDataClass(v interface{}) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	}
	return ""
}
