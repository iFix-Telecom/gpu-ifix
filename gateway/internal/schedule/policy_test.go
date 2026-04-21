package schedule_test

import (
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/schedule"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func TestDecide24x7_AlwaysTier0(t *testing.T) {
	cfg := tenants.TenantConfig{Mode: "24/7"}
	if got := schedule.DecideUpstreamTier(cfg, time.Now()); got != schedule.Tier0 {
		t.Errorf("24/7 mode should always be tier 0, got %d", got)
	}
}

func TestDecidePeak_InsideWindowTier0(t *testing.T) {
	loc := mustLocation(t, "America/Sao_Paulo")
	cfg := tenants.TenantConfig{
		Mode:            "peak",
		Location:        loc,
		PeakWindowStart: time.Date(1, 1, 1, 8, 0, 0, 0, time.UTC),
		PeakWindowEnd:   time.Date(1, 1, 1, 22, 0, 0, 0, time.UTC),
	}
	// 15:00 BRT (UTC-03:00) = 18:00 UTC — well inside 08:00-22:00 BRT.
	now := time.Date(2026, 4, 20, 18, 0, 0, 0, time.UTC)
	if got := schedule.DecideUpstreamTier(cfg, now); got != schedule.Tier0 {
		t.Errorf("peak in-window should be tier 0, got %d", got)
	}
}

func TestDecidePeak_OffHoursTier1(t *testing.T) {
	loc := mustLocation(t, "America/Sao_Paulo")
	cfg := tenants.TenantConfig{
		Mode:            "peak",
		Location:        loc,
		PeakWindowStart: time.Date(1, 1, 1, 8, 0, 0, 0, time.UTC),
		PeakWindowEnd:   time.Date(1, 1, 1, 22, 0, 0, 0, time.UTC),
	}
	// 23:00 BRT = 02:00 UTC next day — off-hours in BRT, so route external.
	now := time.Date(2026, 4, 21, 2, 0, 0, 0, time.UTC)
	if got := schedule.DecideUpstreamTier(cfg, now); got != schedule.Tier1 {
		t.Errorf("peak off-hours should be tier 1, got %d", got)
	}
}

func TestDecidePeak_NilLocationFailsOpenToTier0(t *testing.T) {
	cfg := tenants.TenantConfig{Mode: "peak", Location: nil}
	if got := schedule.DecideUpstreamTier(cfg, time.Now()); got != schedule.Tier0 {
		t.Errorf("peak with nil Location should fail-open to tier 0, got %d", got)
	}
}

func TestDecidePeak_WrapAroundWindow(t *testing.T) {
	loc := mustLocation(t, "America/Sao_Paulo")
	// 22:00-08:00 overnight window; midnight BRT is inside.
	cfg := tenants.TenantConfig{
		Mode:            "peak",
		Location:        loc,
		PeakWindowStart: time.Date(1, 1, 1, 22, 0, 0, 0, time.UTC),
		PeakWindowEnd:   time.Date(1, 1, 1, 8, 0, 0, 0, time.UTC),
	}
	// 00:00 BRT = 03:00 UTC — inside overnight window.
	now := time.Date(2026, 4, 21, 3, 0, 0, 0, time.UTC)
	if got := schedule.DecideUpstreamTier(cfg, now); got != schedule.Tier0 {
		t.Errorf("wrap-around in-window should be tier 0, got %d", got)
	}
	// 15:00 BRT = 18:00 UTC — outside overnight window.
	now2 := time.Date(2026, 4, 21, 18, 0, 0, 0, time.UTC)
	if got := schedule.DecideUpstreamTier(cfg, now2); got != schedule.Tier1 {
		t.Errorf("wrap-around off-hours should be tier 1, got %d", got)
	}
}
