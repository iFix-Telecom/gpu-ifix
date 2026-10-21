package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus/testutil"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
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

	// Phase 23 (23-11): agregado de tokens/créditos e lifecycles da 3060.
	tokensRow gen.SumEconomyTokensRangeRow
	tokensErr error
	auxRows   []gen.ListAuxPodLifecyclesInRangeRow
	auxErr    error
}

func (f *fakeEconomyQueries) SumEconomyTokensRange(_ context.Context, _ gen.SumEconomyTokensRangeParams) (gen.SumEconomyTokensRangeRow, error) {
	if f.err != nil {
		return gen.SumEconomyTokensRangeRow{}, f.err
	}
	if f.tokensErr != nil {
		return gen.SumEconomyTokensRangeRow{}, f.tokensErr
	}
	return f.tokensRow, nil
}

func (f *fakeEconomyQueries) ListAuxPodLifecyclesInRange(_ context.Context, _ gen.ListAuxPodLifecyclesInRangeParams) ([]gen.ListAuxPodLifecyclesInRangeRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.auxErr != nil {
		return nil, f.auxErr
	}
	return f.auxRows, nil
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

		PctServidoLocalTokens *float64 `json:"pct_servido_local_tokens"`
		PctHaikuTokens        *float64 `json:"pct_haiku_tokens"`
		CustoCreditosHaikuBRL float64  `json:"custo_creditos_haiku_brl"`
		CustoExternoCaixaBRL  float64  `json:"custo_externo_caixa_brl"`
		Vast3060BRL           float64  `json:"vast_3060_brl"`
	} `json:"summary"`
	Series []struct {
		Date             string  `json:"date"`
		PhantomBrl       float64 `json:"phantom_brl"`
		VastBrl          float64 `json:"vast_brl"`
		EconomiaBrl      float64 `json:"economia_brl"`
		CreditosHaikuBrl float64 `json:"creditos_haiku_brl"`
		CaixaExternoBrl  float64 `json:"caixa_externo_brl"`
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

// --- Phase 23 (23-11, HAIKU-09): painel Economia fidedigno ---

// TestEconomyHandler_PctServidoLocalTokens: % por tokens na mesma escala 0–1
// do pct_servido_local (por requests); null quando total_tokens == 0.
func TestEconomyHandler_PctServidoLocalTokens(t *testing.T) {
	fake := &fakeEconomyQueries{
		rangeRow:  gen.SumBillingAllTenantsRangeRow{PhantomBrl: opNumeric(0), ExternalBrl: opNumeric(0), TotalRequests: 4, LocalRequests: 1},
		tokensRow: gen.SumEconomyTokensRangeRow{LocalTokens: 1000, TotalTokens: 4000, CreditsCoveredBrl: opNumeric(0), ExternalCashBrl: opNumeric(0)},
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, body := doEconomyRequest(t, h, econRange)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Summary.PctServidoLocalTokens == nil || *body.Summary.PctServidoLocalTokens != 0.25 {
		t.Fatalf("pct_servido_local_tokens = %v, want 0.25", body.Summary.PctServidoLocalTokens)
	}

	fakeZero := &fakeEconomyQueries{
		rangeRow:  gen.SumBillingAllTenantsRangeRow{PhantomBrl: opNumeric(0), ExternalBrl: opNumeric(0)},
		tokensRow: gen.SumEconomyTokensRangeRow{CreditsCoveredBrl: opNumeric(0), ExternalCashBrl: opNumeric(0)},
	}
	_, bodyZero := doEconomyRequest(t, newEconomyHandlerWithQueries(fakeZero, opTestCfg(), discardLog()), econRange)
	if bodyZero.Summary.PctServidoLocalTokens != nil {
		t.Errorf("pct_servido_local_tokens = %v, want nil quando total_tokens == 0", *bodyZero.Summary.PctServidoLocalTokens)
	}
}

// TestEconomyHandler_CreditosHaikuECaixa: passo ① (coberto por créditos)
// separado do externo em caixa; custo_openrouter_brl segue = total externo.
func TestEconomyHandler_CreditosHaikuECaixa(t *testing.T) {
	fake := &fakeEconomyQueries{
		rangeRow:  gen.SumBillingAllTenantsRangeRow{PhantomBrl: opNumeric(0), ExternalBrl: opNumeric(15), TotalRequests: 3},
		tokensRow: gen.SumEconomyTokensRangeRow{CreditsCoveredBrl: opNumeric(10), ExternalCashBrl: opNumeric(5), TotalTokens: 10},
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, body := doEconomyRequest(t, h, econRange)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Summary.CustoCreditosHaikuBRL != 10 {
		t.Errorf("custo_creditos_haiku_brl = %v, want 10", body.Summary.CustoCreditosHaikuBRL)
	}
	if body.Summary.CustoExternoCaixaBRL != 5 {
		t.Errorf("custo_externo_caixa_brl = %v, want 5", body.Summary.CustoExternoCaixaBRL)
	}
	if body.Summary.CustoOpenRouterBRL != 15 {
		t.Errorf("custo_openrouter_brl = %v, want 15 (total externo, compatível)", body.Summary.CustoOpenRouterBRL)
	}
}

// TestEconomyHandler_SeriesCreditosECaixa: a série diária carrega o split
// créditos Haiku × caixa externo de cada dia (gráfico de tendência).
func TestEconomyHandler_SeriesCreditosECaixa(t *testing.T) {
	d1 := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	fake := &fakeEconomyQueries{
		dayRows: []gen.SumPhantomAllTenantsByDateRow{
			{Date: econDate(d1), PhantomBrl: opNumeric(1), CreditsCoveredBrl: opNumeric(12.5), ExternalCashBrl: opNumeric(3.25)},
		},
		rangeRow: gen.SumBillingAllTenantsRangeRow{PhantomBrl: opNumeric(1), ExternalBrl: opNumeric(15.75), TotalRequests: 1},
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, body := doEconomyRequest(t, h, econRange)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if len(body.Series) != 1 {
		t.Fatalf("series len = %d, want 1", len(body.Series))
	}
	if got := body.Series[0].CreditosHaikuBrl; got != 12.5 {
		t.Errorf("creditos_haiku_brl = %v, want 12.5", got)
	}
	if got := body.Series[0].CaixaExternoBrl; got != 3.25 {
		t.Errorf("caixa_externo_brl = %v, want 3.25", got)
	}
}

// TestEconomyHandler_PctHaikuTokens: 30 de 120 tokens pelo passo ① → 0,25;
// total 0 → null.
func TestEconomyHandler_PctHaikuTokens(t *testing.T) {
	fake := &fakeEconomyQueries{
		rangeRow:  gen.SumBillingAllTenantsRangeRow{TotalRequests: 1},
		tokensRow: gen.SumEconomyTokensRangeRow{TotalTokens: 120, HaikuTokens: 30},
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	_, body := doEconomyRequest(t, h, econRange)
	if body.Summary.PctHaikuTokens == nil || *body.Summary.PctHaikuTokens != 0.25 {
		t.Errorf("pct_haiku_tokens = %v, want 0.25", body.Summary.PctHaikuTokens)
	}

	fake.tokensRow = gen.SumEconomyTokensRangeRow{}
	_, body = doEconomyRequest(t, h, econRange)
	if body.Summary.PctHaikuTokens != nil {
		t.Errorf("pct_haiku_tokens = %v, want null com total 0", *body.Summary.PctHaikuTokens)
	}
}

// TestEconomyHandler_Vast3060: lifecycle fechado (total_cost_usd 2) + aberto
// (dph 0.1 há 10 h), USDToBRLRate 5 → vast_3060_brl = 10 + 5 = 15; vast_brl
// = 3090 + 3060; economia_liquida_brl = phantom − vast_brl.
func TestEconomyHandler_Vast3060(t *testing.T) {
	now := time.Now()
	fake := &fakeEconomyQueries{
		rangeRow:  gen.SumBillingAllTenantsRangeRow{PhantomBrl: opNumeric(100), ExternalBrl: opNumeric(0)},
		tokensRow: gen.SumEconomyTokensRangeRow{CreditsCoveredBrl: opNumeric(0), ExternalCashBrl: opNumeric(0)},
		lcRows: []gen.ListPrimaryLifecyclesInRangeRow{{
			ID:           1,
			StartedAt:    now.Add(-5 * time.Hour),
			EndedAt:      opTimestamptz(now.Add(-2 * time.Hour)),
			TotalCostBrl: opNumeric(5),
		}},
		auxRows: []gen.ListAuxPodLifecyclesInRangeRow{
			{
				ID:           1,
				Pod:          "vast-3060",
				StartedAt:    now.Add(-30 * time.Hour),
				EndedAt:      opTimestamptz(now.Add(-20 * time.Hour)),
				DphUsd:       opNumeric(0.1),
				TotalCostUsd: opNumeric(2),
				TotalCostBrl: opNumeric(10),
			},
			{
				ID:        2,
				Pod:       "vast-3060",
				StartedAt: now.Add(-10 * time.Hour),
				EndedAt:   pgtype.Timestamptz{Valid: false},
				DphUsd:    opNumeric(0.1),
			},
		},
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, body := doEconomyRequest(t, h, econRange)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if d := body.Summary.Vast3060BRL - 15; d > 0.05 || d < -0.05 {
		t.Errorf("vast_3060_brl = %v, want ~15", body.Summary.Vast3060BRL)
	}
	if d := body.Summary.VastBRL - 20; d > 0.05 || d < -0.05 {
		t.Errorf("vast_brl = %v, want ~20 (3090 5 + 3060 15)", body.Summary.VastBRL)
	}
	if d := body.Summary.EconomiaLiquidaBRL - 80; d > 0.05 || d < -0.05 {
		t.Errorf("economia_liquida_brl = %v, want ~80", body.Summary.EconomiaLiquidaBRL)
	}
	// horas_pod_up segue sendo só o primary (3090).
	if d := body.Summary.HorasPodUp - 3; d > 0.05 || d < -0.05 {
		t.Errorf("horas_pod_up = %v, want ~3 (só 3090)", body.Summary.HorasPodUp)
	}
	// A série diária também carrega a 3060 (soma das colunas vast = vast_brl).
	var seriesVast float64
	for _, r := range body.Series {
		seriesVast += r.VastBrl
	}
	if d := seriesVast - body.Summary.VastBRL; d > 0.05 || d < -0.05 {
		t.Errorf("soma vast da série = %v, want = vast_brl %v", seriesVast, body.Summary.VastBRL)
	}
}

// TestEconomyHandler_TokensQueryError500: falha em SumEconomyTokensRange →
// 500 billing_query_failed + métrica 5xx.
func TestEconomyHandler_TokensQueryError500(t *testing.T) {
	fake := &fakeEconomyQueries{
		rangeRow:  gen.SumBillingAllTenantsRangeRow{PhantomBrl: opNumeric(0), ExternalBrl: opNumeric(0)},
		tokensErr: errors.New("boom"),
	}
	before := testutil.ToFloat64(obs.GatewayAdminRequests.WithLabelValues("/admin/economy", "5xx"))
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, _ := doEconomyRequest(t, h, econRange)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "billing_query_failed") {
		t.Errorf("body = %s, want billing_query_failed", rec.Body.String())
	}
	if after := testutil.ToFloat64(obs.GatewayAdminRequests.WithLabelValues("/admin/economy", "5xx")); after != before+1 {
		t.Errorf("métrica 5xx = %v, want %v", after, before+1)
	}
}

// TestEconomyHandler_AuxQueryError500: falha em ListAuxPodLifecyclesInRange → 500.
func TestEconomyHandler_AuxQueryError500(t *testing.T) {
	fake := &fakeEconomyQueries{
		rangeRow:  gen.SumBillingAllTenantsRangeRow{PhantomBrl: opNumeric(0), ExternalBrl: opNumeric(0)},
		tokensRow: gen.SumEconomyTokensRangeRow{CreditsCoveredBrl: opNumeric(0), ExternalCashBrl: opNumeric(0)},
		auxErr:    errors.New("boom"),
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, _ := doEconomyRequest(t, h, econRange)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// TestEconomyHandler_CreditosHaikuContamComoEconomia: decisão 2026-10-20 —
// economia = phantom + créditos Haiku − vast, no resumo e na série; ROI segue
// phantom / vast.
func TestEconomyHandler_CreditosHaikuContamComoEconomia(t *testing.T) {
	now := time.Now()
	day := pgtype.Date{Time: now.In(time.FixedZone("BRT", -3*3600)), Valid: true}
	fake := &fakeEconomyQueries{
		rangeRow:  gen.SumBillingAllTenantsRangeRow{PhantomBrl: opNumeric(10), TotalRequests: 1},
		tokensRow: gen.SumEconomyTokensRangeRow{CreditsCoveredBrl: opNumeric(30), ExternalCashBrl: opNumeric(2)},
		dayRows: []gen.SumPhantomAllTenantsByDateRow{{
			Date: day, PhantomBrl: opNumeric(10), CreditsCoveredBrl: opNumeric(30), ExternalCashBrl: opNumeric(2),
		}},
		lcRows: []gen.ListPrimaryLifecyclesInRangeRow{{
			ID:           1,
			StartedAt:    now.Add(-5 * time.Hour),
			EndedAt:      opTimestamptz(now.Add(-2 * time.Hour)),
			TotalCostBrl: opNumeric(5),
		}},
	}
	h := newEconomyHandlerWithQueries(fake, opTestCfg(), discardLog())
	rec, body := doEconomyRequest(t, h, econRange)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if body.Summary.EconomiaLiquidaBRL != 35 {
		t.Errorf("economia_liquida_brl = %v, want 35 (10 + 30 − 5)", body.Summary.EconomiaLiquidaBRL)
	}
	if body.Summary.ROIMultiplier == nil || *body.Summary.ROIMultiplier != 2 {
		t.Errorf("roi_multiplier = %v, want 2 (só GPU)", body.Summary.ROIMultiplier)
	}
	var seriesEco float64
	for _, r := range body.Series {
		seriesEco += r.EconomiaBrl
	}
	if d := seriesEco - 35; d > 0.05 || d < -0.05 {
		t.Errorf("soma economia da série = %v, want 35", seriesEco)
	}
}
