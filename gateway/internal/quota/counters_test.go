// Unit tests for QuotaChecker + ErrorCode. Uses a hand-rolled fake that
// implements the (unexported) countersQueries interface via export_test.go
// indirection. Integration coverage (real Postgres) lives in Plan 04-08.
package quota

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// fakeQueries implements the countersQueries interface for unit tests.
type fakeQueries struct {
	today    gen.GetUsageCountersTodayRow
	todayErr error
	month    gen.GetUsageCountersMonthRow
	monthErr error
}

func (f *fakeQueries) GetUsageCountersToday(ctx context.Context, tenantID uuid.UUID) (gen.GetUsageCountersTodayRow, error) {
	return f.today, f.todayErr
}
func (f *fakeQueries) GetUsageCountersMonth(ctx context.Context, tenantID uuid.UUID) (gen.GetUsageCountersMonthRow, error) {
	return f.month, f.monthErr
}

func silentChecker(f *fakeQueries) *QuotaChecker {
	return NewQuotaChecker(f, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestCheckQuotaToday_BelowLimit(t *testing.T) {
	c := silentChecker(&fakeQueries{
		today: gen.GetUsageCountersTodayRow{TokensIn: 500, TokensOut: 200, AudioSeconds: 0, EmbedsCount: 0},
	})
	err := c.CheckQuotaToday(context.Background(), uuid.New(),
		QuotaLimits{DailyTokens: 1000, DailyAudioMinutes: 10, DailyEmbeds: 100})
	if err == nil {
		// Expected: 500+200 = 700 < 1000 → nil
		return
	}
	t.Fatalf("want nil, got %v", err)
}

func TestCheckQuotaToday_AtLimit(t *testing.T) {
	c := silentChecker(&fakeQueries{
		today: gen.GetUsageCountersTodayRow{TokensIn: 500, TokensOut: 500},
	})
	err := c.CheckQuotaToday(context.Background(), uuid.New(),
		QuotaLimits{DailyTokens: 1000})
	if !errors.Is(err, ErrQuotaExceededDailyTokens) {
		t.Errorf("want ErrQuotaExceededDailyTokens, got %v", err)
	}
}

func TestCheckQuotaToday_AudioMinutesExceeded(t *testing.T) {
	c := silentChecker(&fakeQueries{
		today: gen.GetUsageCountersTodayRow{AudioSeconds: 600}, // 10 min
	})
	err := c.CheckQuotaToday(context.Background(), uuid.New(),
		QuotaLimits{DailyAudioMinutes: 10})
	if !errors.Is(err, ErrQuotaExceededDailyAudioMinutes) {
		t.Errorf("want ErrQuotaExceededDailyAudioMinutes, got %v", err)
	}
}

func TestCheckQuotaToday_EmbedsExceeded(t *testing.T) {
	c := silentChecker(&fakeQueries{
		today: gen.GetUsageCountersTodayRow{EmbedsCount: 100},
	})
	err := c.CheckQuotaToday(context.Background(), uuid.New(),
		QuotaLimits{DailyEmbeds: 100})
	if !errors.Is(err, ErrQuotaExceededDailyEmbeds) {
		t.Errorf("want ErrQuotaExceededDailyEmbeds, got %v", err)
	}
}

func TestCheckQuotaToday_NoRowsTreatedAsUnderLimit(t *testing.T) {
	c := silentChecker(&fakeQueries{todayErr: pgx.ErrNoRows})
	err := c.CheckQuotaToday(context.Background(), uuid.New(),
		QuotaLimits{DailyTokens: 1000})
	if err != nil {
		t.Errorf("pgx.ErrNoRows should be treated as under-limit, got %v", err)
	}
}

func TestCheckQuotaToday_FailClosed(t *testing.T) {
	c := silentChecker(&fakeQueries{todayErr: errors.New("boom: postgres unreachable")})
	err := c.CheckQuotaToday(context.Background(), uuid.New(),
		QuotaLimits{DailyTokens: 1000})
	if !errors.Is(err, ErrQuotaCheckUnavailable) {
		t.Errorf("want ErrQuotaCheckUnavailable (D-A2 fail-closed), got %v", err)
	}
}

func TestCheckQuotaToday_ZeroLimitDisablesDimension(t *testing.T) {
	c := silentChecker(&fakeQueries{
		today: gen.GetUsageCountersTodayRow{TokensIn: 99999, TokensOut: 99999, AudioSeconds: 99999, EmbedsCount: 99999},
	})
	// All limits 0 → all dimensions disabled → nil.
	err := c.CheckQuotaToday(context.Background(), uuid.New(), QuotaLimits{})
	if err != nil {
		t.Errorf("zero QuotaLimits should disable all checks, got %v", err)
	}
}

func TestCheckQuotaMonth_AtLimit(t *testing.T) {
	c := silentChecker(&fakeQueries{
		month: gen.GetUsageCountersMonthRow{TokensIn: 3_000_000, TokensOut: 0},
	})
	err := c.CheckQuotaMonth(context.Background(), uuid.New(),
		QuotaLimits{MonthlyTokens: 3_000_000})
	if !errors.Is(err, ErrQuotaExceededMonthlyTokens) {
		t.Errorf("want ErrQuotaExceededMonthlyTokens, got %v", err)
	}
}

func TestCheckQuotaMonth_FailClosed(t *testing.T) {
	c := silentChecker(&fakeQueries{monthErr: errors.New("query timeout")})
	err := c.CheckQuotaMonth(context.Background(), uuid.New(),
		QuotaLimits{MonthlyTokens: 1_000_000})
	if !errors.Is(err, ErrQuotaCheckUnavailable) {
		t.Errorf("want ErrQuotaCheckUnavailable (D-A2 fail-closed), got %v", err)
	}
}

// --- Plan 16-02: end-to-end quota-trip proof for the audio + embed
// dimensions. Before Plan 16-01/16-02 the source counters (usage_counters
// .audio_seconds / .embeds_count) were ALWAYS 0 because no STT/embed proxy
// was wired to the usage producer — so these dimensions could never gate.
// Now that the producer Stores AudioSecondsMs10 / EmbedsCount and the
// proxies are wired (Plan 16-02 main.go), the consumer end (CheckQuota*)
// must actually block when the populated counter crosses the limit. These
// tests assert the consumer contract with boundary coverage (TEN-04 payoff).

func TestCheckQuotaTodayAudioMinutesTrips(t *testing.T) {
	// 120s = 2 min == limit 2 → trip (>= comparison).
	c := silentChecker(&fakeQueries{
		today: gen.GetUsageCountersTodayRow{AudioSeconds: 120},
	})
	err := c.CheckQuotaToday(context.Background(), uuid.New(),
		QuotaLimits{DailyAudioMinutes: 2})
	if !errors.Is(err, ErrQuotaExceededDailyAudioMinutes) {
		t.Errorf("AudioSeconds=120 (2min) limit 2 → want ErrQuotaExceededDailyAudioMinutes, got %v", err)
	}

	// Boundary: 119s → int(119/60)=1 < limit 2 → under limit (nil).
	cBoundary := silentChecker(&fakeQueries{
		today: gen.GetUsageCountersTodayRow{AudioSeconds: 119},
	})
	if err := cBoundary.CheckQuotaToday(context.Background(), uuid.New(),
		QuotaLimits{DailyAudioMinutes: 2}); err != nil {
		t.Errorf("AudioSeconds=119 (int 119/60=1) limit 2 → want nil, got %v", err)
	}
}

func TestCheckQuotaTodayEmbedsTrips(t *testing.T) {
	// 50 embeds == limit 50 → trip.
	c := silentChecker(&fakeQueries{
		today: gen.GetUsageCountersTodayRow{EmbedsCount: 50},
	})
	err := c.CheckQuotaToday(context.Background(), uuid.New(),
		QuotaLimits{DailyEmbeds: 50})
	if !errors.Is(err, ErrQuotaExceededDailyEmbeds) {
		t.Errorf("EmbedsCount=50 limit 50 → want ErrQuotaExceededDailyEmbeds, got %v", err)
	}

	// Boundary: 49 < 50 → nil.
	cBoundary := silentChecker(&fakeQueries{
		today: gen.GetUsageCountersTodayRow{EmbedsCount: 49},
	})
	if err := cBoundary.CheckQuotaToday(context.Background(), uuid.New(),
		QuotaLimits{DailyEmbeds: 50}); err != nil {
		t.Errorf("EmbedsCount=49 limit 50 → want nil, got %v", err)
	}
}

func TestCheckQuotaMonthAudioAndEmbedsTrip(t *testing.T) {
	// Monthly audio: 7200s = 120 min == limit 120 → trip.
	cAudio := silentChecker(&fakeQueries{
		month: gen.GetUsageCountersMonthRow{AudioSeconds: 7200},
	})
	if err := cAudio.CheckQuotaMonth(context.Background(), uuid.New(),
		QuotaLimits{MonthlyAudioMinutes: 120}); !errors.Is(err, ErrQuotaExceededMonthlyAudioMinutes) {
		t.Errorf("AudioSeconds=7200 (120min) limit 120 → want ErrQuotaExceededMonthlyAudioMinutes, got %v", err)
	}

	// Monthly embeds: 1000 == limit 1000 → trip.
	cEmbeds := silentChecker(&fakeQueries{
		month: gen.GetUsageCountersMonthRow{EmbedsCount: 1000},
	})
	if err := cEmbeds.CheckQuotaMonth(context.Background(), uuid.New(),
		QuotaLimits{MonthlyEmbeds: 1000}); !errors.Is(err, ErrQuotaExceededMonthlyEmbeds) {
		t.Errorf("EmbedsCount=1000 limit 1000 → want ErrQuotaExceededMonthlyEmbeds, got %v", err)
	}

	// Boundary: monthly audio 7199s → int(7199/60)=119 < 120 → nil.
	cBoundary := silentChecker(&fakeQueries{
		month: gen.GetUsageCountersMonthRow{AudioSeconds: 7199, EmbedsCount: 999},
	})
	if err := cBoundary.CheckQuotaMonth(context.Background(), uuid.New(),
		QuotaLimits{MonthlyAudioMinutes: 120, MonthlyEmbeds: 1000}); err != nil {
		t.Errorf("AudioSeconds=7199 (119min) + EmbedsCount=999, limits 120/1000 → want nil, got %v", err)
	}
}

func TestErrorCode_AllSentinels(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{ErrRateLimitRPS, "rate_limit_rps"},
		{ErrRateLimitRPM, "rate_limit_rpm"},
		{ErrQuotaExceededDailyTokens, "quota_exceeded_daily_tokens"},
		{ErrQuotaExceededDailyAudioMinutes, "quota_exceeded_daily_audio_minutes"},
		{ErrQuotaExceededDailyEmbeds, "quota_exceeded_daily_embeds"},
		{ErrQuotaExceededMonthlyTokens, "quota_exceeded_monthly_tokens"},
		{ErrQuotaExceededMonthlyAudioMinutes, "quota_exceeded_monthly_audio_minutes"},
		{ErrQuotaExceededMonthlyEmbeds, "quota_exceeded_monthly_embeds"},
		{ErrQuotaCheckUnavailable, "quota_check_unavailable"},
	}
	for _, c := range cases {
		if got := ErrorCode(c.err); got != c.code {
			t.Errorf("ErrorCode(%v) = %q, want %q", c.err, got, c.code)
		}
	}
	// Unknown sentinel falls through to non-empty.
	if got := ErrorCode(pgx.ErrNoRows); got == "" {
		t.Error("unknown sentinel should return non-empty fallback")
	}
}
