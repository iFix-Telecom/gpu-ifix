package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
)

// discardLog is the package-internal (package admin) test logger, shared
// by metrics_test.go and audit_test.go. It is distinct from the
// identically named helper in middleware_test.go (package admin_test) —
// the two test packages do not share symbols.
func discardLog() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// fakeMetricsQueries is an in-memory metricsQueries double — no pgxpool.
// It records the ts argument so a test can assert the window math.
type fakeMetricsQueries struct {
	rows   []gen.TenantLatencyPercentilesRow
	err    error
	gotTs  time.Time
	called bool
}

func (f *fakeMetricsQueries) TenantLatencyPercentiles(_ context.Context, ts time.Time) ([]gen.TenantLatencyPercentilesRow, error) {
	f.called = true
	f.gotTs = ts
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// TestMetricsHandler_OK_JSONShape: a valid request returns 200 and the
// MetricsResponse shape — per-tenant percentile rows, the FSM state, and
// the window echoed back.
func TestMetricsHandler_OK_JSONShape(t *testing.T) {
	tid := uuid.New()
	fake := &fakeMetricsQueries{
		rows: []gen.TenantLatencyPercentilesRow{
			{
				TenantID:   tid,
				TenantSlug: pgtype.Text{String: "converseai", Valid: true},
				TenantName: pgtype.Text{String: "ConverseAI", Valid: true},
				Route:      "/v1/chat/completions",
				P50:        42.0,
				P95:        180.0,
				P99:        420.0,
				Requests:   100,
				ErrorRate:  0.02,
			},
		},
	}
	fsm := emerg.NewFSM(discardLog(), nil) // starts at StateHealthy
	h := newMetricsHandlerWithQueries(fake, fsm, discardLog())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/metrics", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: want application/json, got %q", ct)
	}

	var resp MetricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode MetricsResponse: %v", err)
	}
	if resp.FSMState != "healthy" {
		t.Errorf("fsm_state: want healthy, got %q", resp.FSMState)
	}
	if resp.Window != (5 * time.Minute).String() {
		t.Errorf("window: want 5m0s, got %q", resp.Window)
	}
	if len(resp.Tenants) != 1 {
		t.Fatalf("tenants: want 1 row, got %d", len(resp.Tenants))
	}
	row := resp.Tenants[0]
	if row.TenantID != tid.String() {
		t.Errorf("tenant_id: want %s, got %s", tid, row.TenantID)
	}
	// WR-10: the human-readable slug/name from the tenants LEFT JOIN are
	// surfaced as non-nil *string when the join matched.
	if row.TenantSlug == nil || *row.TenantSlug != "converseai" {
		t.Errorf("tenant_slug: want converseai, got %v", row.TenantSlug)
	}
	if row.TenantName == nil || *row.TenantName != "ConverseAI" {
		t.Errorf("tenant_name: want ConverseAI, got %v", row.TenantName)
	}
	if row.P95 != 180.0 || row.ErrorRate != 0.02 {
		t.Errorf("percentile/error_rate not echoed: p95=%v error_rate=%v", row.P95, row.ErrorRate)
	}
	if !fake.called {
		t.Error("TenantLatencyPercentiles was not called")
	}
	// Default window: the query ts should be ~5 minutes in the past.
	if ago := time.Since(fake.gotTs); ago < 4*time.Minute || ago > 6*time.Minute {
		t.Errorf("default window ts: want ~5m ago, got %v ago", ago)
	}
}

// TestMetricsHandler_BadWindow_400Envelope: a malformed ?window param is
// rejected with the OpenAI error envelope and never reaches the query.
func TestMetricsHandler_BadWindow_400Envelope(t *testing.T) {
	for _, bad := range []string{"not-a-duration", "-5m", "0", "48h"} {
		fake := &fakeMetricsQueries{}
		fsm := emerg.NewFSM(discardLog(), nil)
		h := newMetricsHandlerWithQueries(fake, fsm, discardLog())

		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/admin/metrics?window="+bad, nil)
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("window=%q: status want 400, got %d", bad, rec.Code)
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
				Type string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("window=%q: decode envelope: %v", bad, err)
		}
		if body.Error.Type != "invalid_request_error" {
			t.Errorf("window=%q: error.type want invalid_request_error, got %q", bad, body.Error.Type)
		}
		if body.Error.Code != "invalid_query_param" {
			t.Errorf("window=%q: error.code want invalid_query_param, got %q", bad, body.Error.Code)
		}
		if fake.called {
			t.Errorf("window=%q: query ran on a rejected request", bad)
		}
	}
}

// TestMetricsHandler_CustomWindow: a valid ?window overrides the default
// and is echoed back + applied to the query ts.
func TestMetricsHandler_CustomWindow(t *testing.T) {
	fake := &fakeMetricsQueries{}
	fsm := emerg.NewFSM(discardLog(), nil)
	h := newMetricsHandlerWithQueries(fake, fsm, discardLog())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/metrics?window=15m", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	var resp MetricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Window != (15 * time.Minute).String() {
		t.Errorf("window: want 15m0s, got %q", resp.Window)
	}
	if ago := time.Since(fake.gotTs); ago < 14*time.Minute || ago > 16*time.Minute {
		t.Errorf("custom window ts: want ~15m ago, got %v ago", ago)
	}
}
