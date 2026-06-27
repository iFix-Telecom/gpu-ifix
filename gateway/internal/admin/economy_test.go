package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// fakeEconomyQueries is an in-memory economyQueries double — no pgxpool. It
// returns canned aggregate/series/lifecycle rows regardless of the range
// params (the handler's window math is exercised by the live operations test;
// here we assert the economy reduction in isolation).
type fakeEconomyQueries struct {
	dayRows  []gen.SumPhantomAllTenantsByDateRow
	rangeRow gen.SumBillingAllTenantsRangeRow
	lcRows   []gen.ListPrimaryLifecyclesInRangeRow
	err      error
}

func (f *fakeEconomyQueries) SumPhantomAllTenantsByDate(_ context.Context, _ gen.SumPhantomAllTenantsByDateParams) ([]gen.SumPhantomAllTenantsByDateRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.dayRows, nil
}

func (f *fakeEconomyQueries) SumBillingAllTenantsRange(_ context.Context, _ gen.SumBillingAllTenantsRangeParams) (gen.SumBillingAllTenantsRangeRow, error) {
	if f.err != nil {
		return gen.SumBillingAllTenantsRangeRow{}, f.err
	}
	return f.rangeRow, nil
}

func (f *fakeEconomyQueries) ListPrimaryLifecyclesInRange(_ context.Context, _ gen.ListPrimaryLifecyclesInRangeParams) ([]gen.ListPrimaryLifecyclesInRangeRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.lcRows, nil
}

// econDate builds a valid pgtype.Date for the series rows.
func econDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t, Valid: true}
}

// econResponse is the decode target. ROIMultiplier/PctServidoLocal are
// pointers so a test can distinguish JSON null from 0.
type econResponse struct {
	Range struct {
		From     string `json:"from"`
		To       string `json:"to"`
		Timezone string `json:"timezone"`
	} `json:"range"`
	Summary struct {
		PhantomBRL         float64  `json:"phantom_brl"`
		VastBRL            float64  `json:"vast_brl"`
		EconomiaLiquidaBRL float64  `json:"economia_liquida_brl"`
		ROIMultiplier      *float64 `json:"roi_multiplier"`
		CustoOpenRouterBRL float64  `json:"custo_openrouter_brl"`
		PctServidoLocal    *float64 `json:"pct_servido_local"`
		HorasPodUp         float64  `json:"horas_pod_up"`
	} `json:"summary"`
	Series []struct {
		Date        string  `json:"date"`
		PhantomBrl  float64 `json:"phantom_brl"`
		VastBrl     float64 `json:"vast_brl"`
		EconomiaBrl float64 `json:"economia_brl"`
	} `json:"series"`
}

func doEconomyRequest(t *testing.T, h *EconomyHandler, query string) (*httptest.ResponseRecorder, econResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	url := "/admin/economy"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest("GET", url, nil)
	h.ServeHTTP(rec, req)
	var body econResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
		}
	}
	return rec, body
}

const econRange = "from=2026-06-01&to=2026-06-30"

// TestEconomyHandler_GatewayWideSum: the range summary is the gateway-wide
// phantom sum and the series carries one entry per BRT day.
func TestEconomyHandler_GatewayWideSum(t *testing.T) {
	d1 := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	fake := &fakeEconomyQueries{
		dayRows: []gen.SumPhantomAllTenantsByDateRow{
			{Date: econDate(d1), PhantomBrl: opNumeric(10)},
			{Date: econDate(d2), PhantomBrl: opNumeric(15)},
		},
		rangeRow: gen.SumBillingAllTenantsRangeRow{
			PhantomBrl:    opNumeric(25),
			ExternalBrl:   opNumeric(0),
			LocalRequests: 5,
			TotalRequests: 5,
		},
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, body := doEconomyRequest(t, h, econRange)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	if body.Summary.PhantomBRL != 25 {
		t.Errorf("phantom_brl = %v, want 25", body.Summary.PhantomBRL)
	}
	if len(body.Series) != 2 {
		t.Fatalf("series len = %d, want 2", len(body.Series))
	}
	if body.Series[0].Date != "2026-06-10" || body.Series[1].Date != "2026-06-11" {
		t.Errorf("series dates = %q,%q want 2026-06-10,2026-06-11", body.Series[0].Date, body.Series[1].Date)
	}
	if body.Range.Timezone != "America/Sao_Paulo" {
		t.Errorf("timezone = %q, want America/Sao_Paulo", body.Range.Timezone)
	}
}

// TestEconomyHandler_EconomiaAndROI: phantom>0, vast>0 → economia = phantom −
// vast and roi = phantom / vast.
func TestEconomyHandler_EconomiaAndROI(t *testing.T) {
	now := time.Now()
	fake := &fakeEconomyQueries{
		rangeRow: gen.SumBillingAllTenantsRangeRow{
			PhantomBrl: opNumeric(25), ExternalBrl: opNumeric(0),
			LocalRequests: 5, TotalRequests: 5,
		},
		lcRows: []gen.ListPrimaryLifecyclesInRangeRow{
			{
				ID:           1,
				StartedAt:    now.Add(-5 * time.Hour),
				EndedAt:      opTimestamptz(now.Add(-2 * time.Hour)),
				TotalCostBrl: opNumeric(5),
				AcceptedDph:  opNumeric(0.4),
			},
		},
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, body := doEconomyRequest(t, h, econRange)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Summary.VastBRL != 5 {
		t.Errorf("vast_brl = %v, want 5", body.Summary.VastBRL)
	}
	if body.Summary.EconomiaLiquidaBRL != 20 {
		t.Errorf("economia_liquida_brl = %v, want 20", body.Summary.EconomiaLiquidaBRL)
	}
	if body.Summary.ROIMultiplier == nil {
		t.Fatal("roi_multiplier = nil, want 5")
	}
	if *body.Summary.ROIMultiplier != 5 {
		t.Errorf("roi_multiplier = %v, want 5", *body.Summary.ROIMultiplier)
	}
}

// TestEconomyHandler_ROINilWhenVastZero: phantom>0, zero lifecycles → roi nil
// (JSON null), economia == phantom, no Inf/NaN.
func TestEconomyHandler_ROINilWhenVastZero(t *testing.T) {
	fake := &fakeEconomyQueries{
		rangeRow: gen.SumBillingAllTenantsRangeRow{
			PhantomBrl: opNumeric(25), ExternalBrl: opNumeric(0),
			LocalRequests: 5, TotalRequests: 5,
		},
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, body := doEconomyRequest(t, h, econRange)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Summary.VastBRL != 0 {
		t.Errorf("vast_brl = %v, want 0", body.Summary.VastBRL)
	}
	if body.Summary.ROIMultiplier != nil {
		t.Errorf("roi_multiplier = %v, want nil", *body.Summary.ROIMultiplier)
	}
	if body.Summary.EconomiaLiquidaBRL != 25 {
		t.Errorf("economia_liquida_brl = %v, want 25", body.Summary.EconomiaLiquidaBRL)
	}
}

// TestEconomyHandler_CustoOpenRouter: external_brl flows into
// custo_openrouter_brl unchanged.
func TestEconomyHandler_CustoOpenRouter(t *testing.T) {
	fake := &fakeEconomyQueries{
		rangeRow: gen.SumBillingAllTenantsRangeRow{
			PhantomBrl: opNumeric(0), ExternalBrl: opNumeric(7),
			LocalRequests: 0, TotalRequests: 3,
		},
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, body := doEconomyRequest(t, h, econRange)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Summary.CustoOpenRouterBRL != 7 {
		t.Errorf("custo_openrouter_brl = %v, want 7", body.Summary.CustoOpenRouterBRL)
	}
}

// TestEconomyHandler_PctServidoLocal: local/total when total>0, nil when
// total==0.
func TestEconomyHandler_PctServidoLocal(t *testing.T) {
	fake := &fakeEconomyQueries{
		rangeRow: gen.SumBillingAllTenantsRangeRow{
			PhantomBrl: opNumeric(10), ExternalBrl: opNumeric(0),
			LocalRequests: 8, TotalRequests: 10,
		},
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, body := doEconomyRequest(t, h, econRange)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Summary.PctServidoLocal == nil {
		t.Fatal("pct_servido_local = nil, want 0.8")
	}
	if *body.Summary.PctServidoLocal != 0.8 {
		t.Errorf("pct_servido_local = %v, want 0.8", *body.Summary.PctServidoLocal)
	}

	fakeZero := &fakeEconomyQueries{
		rangeRow: gen.SumBillingAllTenantsRangeRow{
			PhantomBrl: opNumeric(0), ExternalBrl: opNumeric(0),
			LocalRequests: 0, TotalRequests: 0,
		},
	}
	hZero := newEconomyHandlerWithQueries(fakeZero, opTestCfg(), discardLog())
	_, bodyZero := doEconomyRequest(t, hZero, econRange)
	if bodyZero.Summary.PctServidoLocal != nil {
		t.Errorf("pct_servido_local = %v, want nil when total_requests==0", *bodyZero.Summary.PctServidoLocal)
	}
}

// TestEconomyHandler_HorasPodUp: closed (ended−started=3h) + open
// (now−started=2h) → horas_pod_up == 5 (±tolerance).
func TestEconomyHandler_HorasPodUp(t *testing.T) {
	now := time.Now()
	fake := &fakeEconomyQueries{
		rangeRow: gen.SumBillingAllTenantsRangeRow{
			PhantomBrl: opNumeric(0), ExternalBrl: opNumeric(0),
		},
		lcRows: []gen.ListPrimaryLifecyclesInRangeRow{
			{
				ID:           1,
				StartedAt:    now.Add(-5 * time.Hour),
				EndedAt:      opTimestamptz(now.Add(-2 * time.Hour)), // 3h closed
				TotalCostBrl: opNumeric(4),
			},
			{
				ID:          2,
				StartedAt:   now.Add(-2 * time.Hour), // 2h open
				EndedAt:     pgtype.Timestamptz{Valid: false},
				AcceptedDph: opNumeric(0.4),
			},
		},
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, body := doEconomyRequest(t, h, econRange)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if diff := body.Summary.HorasPodUp - 5; diff > 0.05 || diff < -0.05 {
		t.Errorf("horas_pod_up = %v, want ~5", body.Summary.HorasPodUp)
	}
}

// TestEconomyHandler_OpenLifecycleAccrual: open lifecycle vast cost ==
// dph * hours * USDToBRLRate (opTestCfg = 5.0).
func TestEconomyHandler_OpenLifecycleAccrual(t *testing.T) {
	now := time.Now()
	fake := &fakeEconomyQueries{
		rangeRow: gen.SumBillingAllTenantsRangeRow{
			PhantomBrl: opNumeric(0), ExternalBrl: opNumeric(0),
		},
		lcRows: []gen.ListPrimaryLifecyclesInRangeRow{
			{
				ID:          1,
				StartedAt:   now.Add(-2 * time.Hour),
				EndedAt:     pgtype.Timestamptz{Valid: false},
				AcceptedDph: opNumeric(0.4),
			},
		},
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, body := doEconomyRequest(t, h, econRange)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	// 0.4 dph * ~2h * 5.0 = ~4.0 BRL (tolerance for elapsed-time drift).
	if diff := body.Summary.VastBRL - 4.0; diff > 0.1 || diff < -0.1 {
		t.Errorf("vast_brl = %v, want ~4.0", body.Summary.VastBRL)
	}
}

// TestEconomyHandler_BadDate: a malformed date param yields 400 invalid_date.
func TestEconomyHandler_BadDate(t *testing.T) {
	fake := &fakeEconomyQueries{}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, _ := doEconomyRequest(t, h, "from=not-a-date&to=2026-06-30")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}
