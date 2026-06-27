package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// fakeAuditQueries is an in-memory auditQueries double — no pgxpool. It
// records the params so a test can assert the limit/offset math.
type fakeAuditQueries struct {
	rows      []gen.ListAuditStateChangesRow
	err       error
	gotParams gen.ListAuditStateChangesParams
	called    bool

	countResult int64
	countErr    error
	gotCount    gen.CountAuditStateChangesParams
	countCalled bool
}

func (f *fakeAuditQueries) ListAuditStateChanges(_ context.Context, arg gen.ListAuditStateChangesParams) ([]gen.ListAuditStateChangesRow, error) {
	f.called = true
	f.gotParams = arg
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func (f *fakeAuditQueries) CountAuditStateChanges(_ context.Context, arg gen.CountAuditStateChangesParams) (int64, error) {
	f.countCalled = true
	f.gotCount = arg
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.countResult, nil
}

// currentMonthBoundsBRT mirrors the handler's default window: [first-of-month
// 00:00 BRT, first-of-next-month 00:00 BRT) — Pitfall 6 (partitions only
// cover recent months).
func currentMonthBoundsBRT(t *testing.T) (time.Time, time.Time) {
	t.Helper()
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	now := time.Now().In(loc)
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	return from, from.AddDate(0, 1, 0)
}

func auditRow(kind string, ts time.Time) gen.ListAuditStateChangesRow {
	return gen.ListAuditStateChangesRow{
		Ts:         ts,
		RequestID:  uuid.New(),
		TenantID:   uuid.New(),
		Route:      "/internal/emerg",
		Method:     "INTERNAL",
		Upstream:   pgtype.Text{String: "local-llm", Valid: true},
		StatusCode: 200,
		LatencyMs:  0,
		ErrorCode:  pgtype.Text{Valid: false},
		EventKind:  pgtype.Text{String: kind, Valid: true},
	}
}

// TestAuditHandler_OK_PaginatedOrder: a valid request returns 200 and the
// rows in exactly the order the query returns them (newest-first — the
// handler does not re-sort).
func TestAuditHandler_OK_PaginatedOrder(t *testing.T) {
	newest := time.Now()
	older := newest.Add(-time.Hour)
	fake := &fakeAuditQueries{
		rows: []gen.ListAuditStateChangesRow{
			auditRow("fsm_transition_failed_over", newest),
			auditRow("fsm_transition_healthy", older),
		},
	}
	h := newAuditHandlerWithQueries(fake, discardLog())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/audit", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: want application/json, got %q", ct)
	}

	var resp AuditResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode AuditResponse: %v", err)
	}
	if resp.Limit != 50 || resp.Offset != 0 {
		t.Errorf("default pagination: want limit=50 offset=0, got limit=%d offset=%d", resp.Limit, resp.Offset)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items: want 2, got %d", len(resp.Items))
	}
	// The handler must preserve the query's newest-first order.
	if resp.Items[0].EventKind == nil || *resp.Items[0].EventKind != "fsm_transition_failed_over" {
		t.Errorf("items[0].event_kind: want fsm_transition_failed_over, got %v", resp.Items[0].EventKind)
	}
	if resp.Items[1].EventKind == nil || *resp.Items[1].EventKind != "fsm_transition_healthy" {
		t.Errorf("items[1].event_kind: want fsm_transition_healthy, got %v", resp.Items[1].EventKind)
	}
	// A NULL Postgres column renders as JSON null (*string nil).
	if resp.Items[0].ErrorCode != nil {
		t.Errorf("items[0].error_code: want nil for a NULL column, got %v", *resp.Items[0].ErrorCode)
	}
	// Default params reach the query unchanged.
	if fake.gotParams.Limit != 50 || fake.gotParams.Offset != 0 {
		t.Errorf("query params: want limit=50 offset=0, got %+v", fake.gotParams)
	}
}

// TestAuditHandler_LimitCappedAt200: a limit above the cap is clamped to
// 200, not rejected.
func TestAuditHandler_LimitCappedAt200(t *testing.T) {
	fake := &fakeAuditQueries{}
	h := newAuditHandlerWithQueries(fake, discardLog())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/audit?limit=5000", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	if fake.gotParams.Limit != 200 {
		t.Errorf("limit cap: want query limit clamped to 200, got %d", fake.gotParams.Limit)
	}
	var resp AuditResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Limit != 200 {
		t.Errorf("response limit: want 200, got %d", resp.Limit)
	}
}

// TestAuditHandler_BadParams_400Envelope: a negative limit or offset, or a
// non-numeric value, is rejected with the OpenAI error envelope and never
// reaches the query.
func TestAuditHandler_BadParams_400Envelope(t *testing.T) {
	cases := []string{
		"/admin/audit?limit=-1",
		"/admin/audit?limit=0",
		"/admin/audit?limit=abc",
		"/admin/audit?offset=-1",
		"/admin/audit?offset=xyz",
	}
	for _, url := range cases {
		fake := &fakeAuditQueries{}
		h := newAuditHandlerWithQueries(fake, discardLog())

		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", url, nil)
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status want 400, got %d", url, rec.Code)
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
				Type string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: decode envelope: %v", url, err)
		}
		if body.Error.Type != "invalid_request_error" {
			t.Errorf("%s: error.type want invalid_request_error, got %q", url, body.Error.Type)
		}
		if body.Error.Code != "invalid_query_param" {
			t.Errorf("%s: error.code want invalid_query_param, got %q", url, body.Error.Code)
		}
		if fake.called {
			t.Errorf("%s: query ran on a rejected request", url)
		}
	}
}

// TestAuditHandler_DefaultRange_CurrentMonth: with no from/to the handler
// defaults to the current BRT month bounds and threads them — plus the
// "%" no-search sentinel — into BOTH ListAuditStateChanges and the new
// CountAuditStateChanges (Pitfall 6).
func TestAuditHandler_DefaultRange_CurrentMonth(t *testing.T) {
	fake := &fakeAuditQueries{}
	h := newAuditHandlerWithQueries(fake, discardLog())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/audit", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	wantFrom, wantTo := currentMonthBoundsBRT(t)
	if !fake.gotParams.Ts.Equal(wantFrom) {
		t.Errorf("list from: want %v, got %v", wantFrom, fake.gotParams.Ts)
	}
	if !fake.gotParams.Ts_2.Equal(wantTo) {
		t.Errorf("list to: want %v, got %v", wantTo, fake.gotParams.Ts_2)
	}
	if fake.gotParams.Column3 != "%" {
		t.Errorf("list search: want %q, got %v", "%", fake.gotParams.Column3)
	}
	if !fake.countCalled {
		t.Fatalf("CountAuditStateChanges was not called")
	}
	if !fake.gotCount.Ts.Equal(wantFrom) || !fake.gotCount.Ts_2.Equal(wantTo) {
		t.Errorf("count bounds: want [%v,%v), got [%v,%v)", wantFrom, wantTo, fake.gotCount.Ts, fake.gotCount.Ts_2)
	}
	if fake.gotCount.Column3 != "%" {
		t.Errorf("count search: want %q, got %v", "%", fake.gotCount.Column3)
	}
}

// TestAuditHandler_ExplicitRangeAndSearch: explicit from/to/search are
// parsed (to is exclusive end = +24h) and the "%term%" pattern is threaded
// into both queries — never string-concatenated into SQL (T-15-05).
func TestAuditHandler_ExplicitRangeAndSearch(t *testing.T) {
	fake := &fakeAuditQueries{}
	h := newAuditHandlerWithQueries(fake, discardLog())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/audit?from=2026-06-01&to=2026-06-15&search=failover", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	wantFrom := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	wantTo := time.Date(2026, 6, 15, 0, 0, 0, 0, loc).Add(24 * time.Hour)
	if !fake.gotParams.Ts.Equal(wantFrom) {
		t.Errorf("list from: want %v, got %v", wantFrom, fake.gotParams.Ts)
	}
	if !fake.gotParams.Ts_2.Equal(wantTo) {
		t.Errorf("list to (exclusive): want %v, got %v", wantTo, fake.gotParams.Ts_2)
	}
	if fake.gotParams.Column3 != "%failover%" {
		t.Errorf("list search: want %q, got %v", "%failover%", fake.gotParams.Column3)
	}
	if fake.gotCount.Column3 != "%failover%" {
		t.Errorf("count search: want %q, got %v", "%failover%", fake.gotCount.Column3)
	}
	if !fake.gotCount.Ts.Equal(wantFrom) || !fake.gotCount.Ts_2.Equal(wantTo) {
		t.Errorf("count bounds: want [%v,%v), got [%v,%v)", wantFrom, wantTo, fake.gotCount.Ts, fake.gotCount.Ts_2)
	}
}

// TestAuditHandler_TotalInResponse: the COUNT(*) result surfaces as
// AuditResponse.Total so the dashboard derives honest pager bounds
// (offset+limit < total) instead of a heuristic canNext.
func TestAuditHandler_TotalInResponse(t *testing.T) {
	fake := &fakeAuditQueries{
		rows:        []gen.ListAuditStateChangesRow{auditRow("fsm_transition_healthy", time.Now())},
		countResult: 137,
	}
	h := newAuditHandlerWithQueries(fake, discardLog())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/audit?limit=50&offset=0", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	var resp AuditResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 137 {
		t.Errorf("total: want 137, got %d", resp.Total)
	}
	// canNext is derivable: offset+limit < total → 0+50 < 137 == true.
	if !(resp.Offset+resp.Limit < int(resp.Total)) {
		t.Errorf("canNext should be true for offset=0 limit=50 total=137")
	}
}

// TestAuditHandler_BadDate_400: an unparseable from/to is rejected with the
// invalid_date envelope and never reaches either query (T-15-06).
func TestAuditHandler_BadDate_400(t *testing.T) {
	cases := []string{
		"/admin/audit?from=not-a-date",
		"/admin/audit?to=2026-13-99",
	}
	for _, url := range cases {
		fake := &fakeAuditQueries{}
		h := newAuditHandlerWithQueries(fake, discardLog())

		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", url, nil)
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status want 400, got %d", url, rec.Code)
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
				Type string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: decode envelope: %v", url, err)
		}
		if body.Error.Code != "invalid_date" {
			t.Errorf("%s: error.code want invalid_date, got %q", url, body.Error.Code)
		}
		if fake.called || fake.countCalled {
			t.Errorf("%s: a query ran on a rejected request", url)
		}
	}
}
