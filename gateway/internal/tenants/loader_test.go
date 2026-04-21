// Unit tests for the tenants loader. Integration coverage against a real
// PG16 testcontainer (boot refresh, NOTIFY-driven reload, atomic snapshot
// swap under concurrent Get calls) lives in Plan 04-08.
package tenants

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

func silentLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeLoaderQueries implements loaderQueries for unit tests.
type fakeLoaderQueries struct {
	rows    []gen.ListTenantsForLoaderRow
	listErr error
	invN    int64
	invErr  error
}

func (f *fakeLoaderQueries) ListTenantsForLoader(ctx context.Context) ([]gen.ListTenantsForLoaderRow, error) {
	return f.rows, f.listErr
}
func (f *fakeLoaderQueries) CountSensitivePeakInvariant(ctx context.Context) (int64, error) {
	return f.invN, f.invErr
}

func TestGet_NilSnapshotReturnsNotFound(t *testing.T) {
	l := &Loader{log: silentLog()}
	_, err := l.Get(uuid.New())
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("Get on uninitialized loader: want ErrTenantNotFound, got %v", err)
	}
	_, err = l.GetBySlug("anything")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("GetBySlug on uninitialized loader: want ErrTenantNotFound, got %v", err)
	}
}

func TestAll_NilSnapshotReturnsNil(t *testing.T) {
	l := &Loader{log: silentLog()}
	if out := l.All(); out != nil {
		t.Errorf("All on uninitialized loader: want nil, got %v", out)
	}
}

func TestRefresh_PopulatesByIDAndBySlug(t *testing.T) {
	id := uuid.New()
	hhmm := func(h, m int) pgtype.Time {
		return pgtype.Time{Microseconds: int64((h*3600 + m*60) * 1_000_000), Valid: true}
	}
	rows := []gen.ListTenantsForLoaderRow{{
		ID:                       id,
		Slug:                     "acme",
		Name:                     "Acme Inc",
		DataClass:                "normal",
		Status:                   "active",
		Mode:                     "peak",
		PeakWindowStart:          hhmm(8, 0),
		PeakWindowEnd:            hhmm(22, 0),
		ScheduleTimezone:         "America/Sao_Paulo",
		DailyQuotaTokens:         1_000_000,
		MonthlyQuotaTokens:       30_000_000,
		DailyQuotaAudioMinutes:   600,
		MonthlyQuotaAudioMinutes: 18000,
		DailyQuotaEmbeds:         100_000,
		MonthlyQuotaEmbeds:       3_000_000,
		RpsLimit:                 20,
		RpmLimit:                 600,
	}}
	l := &Loader{
		q:         &fakeLoaderQueries{rows: rows},
		log:       silentLog(),
		defaultTZ: time.UTC,
	}
	if err := l.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	cfg, err := l.Get(id)
	if err != nil {
		t.Fatalf("Get(id): %v", err)
	}
	if cfg.Slug != "acme" || cfg.Name != "Acme Inc" {
		t.Errorf("unexpected cfg: %+v", cfg)
	}
	if cfg.Mode != "peak" {
		t.Errorf("Mode: want peak, got %q", cfg.Mode)
	}
	if cfg.PeakWindowStart.Hour() != 8 || cfg.PeakWindowEnd.Hour() != 22 {
		t.Errorf("peak window: want 08:00-22:00, got %s-%s",
			cfg.PeakWindowStart.Format("15:04"), cfg.PeakWindowEnd.Format("15:04"))
	}
	if cfg.Location == nil || cfg.Location.String() != "America/Sao_Paulo" {
		t.Errorf("Location: want America/Sao_Paulo, got %v", cfg.Location)
	}
	if cfg.DataClass != "normal" {
		t.Errorf("DataClass: want normal, got %q", cfg.DataClass)
	}
	if cfg.RPSLimit != 20 || cfg.RPMLimit != 600 {
		t.Errorf("RPS/RPM: want 20/600, got %d/%d", cfg.RPSLimit, cfg.RPMLimit)
	}

	// Lookup by slug must return the same config.
	bySlug, err := l.GetBySlug("acme")
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if bySlug.ID != id {
		t.Errorf("GetBySlug id mismatch")
	}
}

func TestRefresh_InvalidTimezoneFallsBackToDefault(t *testing.T) {
	rows := []gen.ListTenantsForLoaderRow{{
		ID:               uuid.New(),
		Slug:             "broken-tz",
		Name:             "Broken TZ",
		DataClass:        "normal",
		Status:           "active",
		Mode:             "24/7",
		ScheduleTimezone: "Europe/NowhereLand",
	}}
	def := time.UTC
	l := &Loader{q: &fakeLoaderQueries{rows: rows}, log: silentLog(), defaultTZ: def}
	if err := l.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	cfg, err := l.GetBySlug("broken-tz")
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if cfg.Location != def {
		t.Errorf("Location: want default UTC, got %v", cfg.Location)
	}
}

func TestRefresh_PropagatesListError(t *testing.T) {
	boom := errors.New("db down")
	l := &Loader{q: &fakeLoaderQueries{listErr: boom}, log: silentLog()}
	err := l.Refresh(context.Background())
	if err == nil || !errors.Is(err, boom) {
		t.Errorf("want wrapped listErr, got %v", err)
	}
}

func TestCheckSensitivePeakInvariant(t *testing.T) {
	l := &Loader{q: &fakeLoaderQueries{invN: 0}, log: silentLog()}
	if err := l.CheckSensitivePeakInvariant(context.Background()); err != nil {
		t.Errorf("no rows: want nil, got %v", err)
	}

	l.q = &fakeLoaderQueries{invN: 2}
	err := l.CheckSensitivePeakInvariant(context.Background())
	if !errors.Is(err, ErrSensitivePeakInvariant) {
		t.Errorf("invN=2: want ErrSensitivePeakInvariant, got %v", err)
	}
}

func TestPgTimeToClock(t *testing.T) {
	cases := []struct {
		in       pgtype.Time
		wantH    int
		wantM    int
		wantZero bool
	}{
		{pgtype.Time{Valid: false}, 0, 0, true},
		{pgtype.Time{Microseconds: 0, Valid: true}, 0, 0, false},
		{pgtype.Time{Microseconds: 8 * 3600 * 1_000_000, Valid: true}, 8, 0, false},
		{pgtype.Time{Microseconds: (22*3600 + 30*60) * 1_000_000, Valid: true}, 22, 30, false},
	}
	for _, c := range cases {
		got := pgTimeToClock(c.in)
		if c.wantZero {
			if !got.IsZero() {
				t.Errorf("want zero time, got %v", got)
			}
			continue
		}
		if got.Hour() != c.wantH || got.Minute() != c.wantM {
			t.Errorf("pgTimeToClock(%v) = %02d:%02d, want %02d:%02d",
				c.in, got.Hour(), got.Minute(), c.wantH, c.wantM)
		}
	}
}

func TestCoerceDataClass(t *testing.T) {
	cases := []struct {
		in   interface{}
		want string
	}{
		{"normal", "normal"},
		{[]byte("sensitive"), "sensitive"},
		{nil, ""},
		{42, ""},
	}
	for _, c := range cases {
		if got := coerceDataClass(c.in); got != c.want {
			t.Errorf("coerceDataClass(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
