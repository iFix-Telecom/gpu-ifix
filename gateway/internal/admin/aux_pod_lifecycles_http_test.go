package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// fakeAuxPodRow espelha uma linha de ai_gateway.aux_pod_lifecycles.
type fakeAuxPodRow struct {
	id           int64
	startedAt    time.Time
	endedAt      pgtype.Timestamptz
	dphUsd       pgtype.Numeric
	totalCostUsd pgtype.Numeric
	totalCostBrl pgtype.Numeric
}

// fakeAuxPodQueries emula em memória a semântica SQL das queries de
// aux_pod_lifecycles.sql (ON CONFLICT com LEAST/COALESCE; close guardado por
// ended_at IS NULL).
type fakeAuxPodQueries struct {
	rows   map[string]*fakeAuxPodRow
	nextID int64
	err    error
}

func newFakeAuxPodQueries() *fakeAuxPodQueries {
	return &fakeAuxPodQueries{rows: map[string]*fakeAuxPodRow{}, nextID: 1}
}

func auxKey(pod string, inst pgtype.Int8) string { return fmt.Sprintf("%s/%d", pod, inst.Int64) }

func (f *fakeAuxPodQueries) StartAuxPodLifecycle(_ context.Context, arg gen.StartAuxPodLifecycleParams) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	k := auxKey(arg.Pod, arg.VastInstanceID)
	if r, ok := f.rows[k]; ok {
		if arg.StartedAt.Before(r.startedAt) {
			r.startedAt = arg.StartedAt
		}
		if arg.DphUsd.Valid {
			r.dphUsd = arg.DphUsd
		}
		return r.id, nil
	}
	r := &fakeAuxPodRow{id: f.nextID, startedAt: arg.StartedAt, dphUsd: arg.DphUsd}
	f.nextID++
	f.rows[k] = r
	return r.id, nil
}

func (f *fakeAuxPodQueries) GetAuxPodLifecycle(_ context.Context, arg gen.GetAuxPodLifecycleParams) (gen.GetAuxPodLifecycleRow, error) {
	if f.err != nil {
		return gen.GetAuxPodLifecycleRow{}, f.err
	}
	r, ok := f.rows[auxKey(arg.Pod, arg.VastInstanceID)]
	if !ok {
		return gen.GetAuxPodLifecycleRow{}, pgx.ErrNoRows
	}
	return gen.GetAuxPodLifecycleRow{ID: r.id, StartedAt: r.startedAt, EndedAt: r.endedAt, DphUsd: r.dphUsd}, nil
}

func (f *fakeAuxPodQueries) CloseAuxPodLifecycle(_ context.Context, arg gen.CloseAuxPodLifecycleParams) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	r, ok := f.rows[auxKey(arg.Pod, arg.VastInstanceID)]
	if !ok || r.endedAt.Valid {
		return 0, nil
	}
	r.endedAt = arg.EndedAt
	r.totalCostUsd = arg.TotalCostUsd
	r.totalCostBrl = arg.TotalCostBrl
	return 1, nil
}

type auxResp struct {
	ID            int64  `json:"id"`
	Event         string `json:"event"`
	AlreadyClosed bool   `json:"already_closed"`
}

func postAux(t *testing.T, h *AuxPodLifecycleHandler, body string) (*httptest.ResponseRecorder, auxResp) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/aux-pod-lifecycles", strings.NewReader(body))
	h.ServeHTTP(rec, req)
	var out auxResp
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
		}
	}
	return rec, out
}

func numF(t *testing.T, n pgtype.Numeric) float64 {
	t.Helper()
	p := numericPtr(n)
	if p == nil {
		t.Fatalf("numeric nulo/ inválido")
	}
	return *p
}

func TestAuxPodLifecycle_StartIdempotent(t *testing.T) {
	q := newFakeAuxPodQueries()
	h := newAuxPodLifecycleHandlerWithQueries(q, opTestCfg(), discardLog())
	body := `{"pod":"vast-3060","event":"start","vast_instance_id":1,"dph_usd":0.12}`
	rec1, r1 := postAux(t, h, body)
	if rec1.Code != http.StatusOK || r1.ID == 0 || r1.Event != "start" {
		t.Fatalf("1º start: %d %+v (%s)", rec1.Code, r1, rec1.Body.String())
	}
	rec2, r2 := postAux(t, h, body)
	if rec2.Code != http.StatusOK || r2.ID != r1.ID {
		t.Fatalf("start repetido: %d id=%d want id=%d", rec2.Code, r2.ID, r1.ID)
	}
	if len(q.rows) != 1 {
		t.Fatalf("linhas = %d, want 1", len(q.rows))
	}
}

func TestAuxPodLifecycle_StartKeepsOldestStartedAt(t *testing.T) {
	q := newFakeAuxPodQueries()
	h := newAuxPodLifecycleHandlerWithQueries(q, opTestCfg(), discardLog())
	postAux(t, h, `{"pod":"vast-3060","event":"start","vast_instance_id":7,"dph_usd":0.1,"at":"2026-10-15T10:00:00Z"}`)
	postAux(t, h, `{"pod":"vast-3060","event":"start","vast_instance_id":7,"at":"2026-10-15T09:00:00Z"}`)
	r := q.rows["vast-3060/7"]
	if want := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC); !r.startedAt.Equal(want) {
		t.Fatalf("started_at = %v, want %v (mais antigo)", r.startedAt, want)
	}
	postAux(t, h, `{"pod":"vast-3060","event":"start","vast_instance_id":7,"at":"2026-10-15T11:00:00Z"}`)
	if want := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC); !r.startedAt.Equal(want) {
		t.Fatalf("started_at = %v após at mais novo, want mantido %v", r.startedAt, want)
	}
	if got := numF(t, r.dphUsd); got != 0.1 {
		t.Fatalf("dph_usd = %v, want 0.1 preservado (retry sem dph)", got)
	}
}

func TestAuxPodLifecycle_StopWithTotal(t *testing.T) {
	q := newFakeAuxPodQueries()
	h := newAuxPodLifecycleHandlerWithQueries(q, opTestCfg(), discardLog())
	_, s := postAux(t, h, `{"pod":"vast-3060","event":"start","vast_instance_id":1,"dph_usd":0.12}`)
	rec, r := postAux(t, h, `{"pod":"vast-3060","event":"stop","vast_instance_id":1,"total_cost_usd":1.5}`)
	if rec.Code != http.StatusOK || r.ID != s.ID || r.Event != "stop" || r.AlreadyClosed {
		t.Fatalf("stop: %d %+v (%s)", rec.Code, r, rec.Body.String())
	}
	row := q.rows["vast-3060/1"]
	if !row.endedAt.Valid {
		t.Fatal("ended_at não gravado")
	}
	if got := numF(t, row.totalCostUsd); got != 1.5 {
		t.Errorf("total_cost_usd = %v, want 1.5", got)
	}
	if got := numF(t, row.totalCostBrl); math.Abs(got-7.5) > 1e-9 {
		t.Errorf("total_cost_brl = %v, want 7.5 (1.5 × 5)", got)
	}
}

func TestAuxPodLifecycle_StopComputesFromDph(t *testing.T) {
	q := newFakeAuxPodQueries()
	h := newAuxPodLifecycleHandlerWithQueries(q, opTestCfg(), discardLog())
	postAux(t, h, `{"pod":"vast-3060","event":"start","vast_instance_id":2,"dph_usd":0.2,"at":"2026-10-15T10:00:00Z"}`)
	rec, _ := postAux(t, h, `{"pod":"vast-3060","event":"stop","vast_instance_id":2,"at":"2026-10-15T20:00:00Z"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop: %d (%s)", rec.Code, rec.Body.String())
	}
	row := q.rows["vast-3060/2"]
	if got := numF(t, row.totalCostUsd); math.Abs(got-2.0) > 1e-9 {
		t.Errorf("total_cost_usd = %v, want 2.0 (0.2 × 10h)", got)
	}
	if got := numF(t, row.totalCostBrl); math.Abs(got-10.0) > 1e-9 {
		t.Errorf("total_cost_brl = %v, want 10.0", got)
	}
}

func TestAuxPodLifecycle_StopRepeatedAlreadyClosed(t *testing.T) {
	q := newFakeAuxPodQueries()
	h := newAuxPodLifecycleHandlerWithQueries(q, opTestCfg(), discardLog())
	_, s := postAux(t, h, `{"pod":"vast-3060","event":"start","vast_instance_id":3,"dph_usd":0.12}`)
	postAux(t, h, `{"pod":"vast-3060","event":"stop","vast_instance_id":3,"total_cost_usd":1.5}`)
	rec, r := postAux(t, h, `{"pod":"vast-3060","event":"stop","vast_instance_id":3,"total_cost_usd":99}`)
	if rec.Code != http.StatusOK || r.ID != s.ID || !r.AlreadyClosed || r.Event != "stop" {
		t.Fatalf("stop repetido: %d %+v (%s)", rec.Code, r, rec.Body.String())
	}
	if got := numF(t, q.rows["vast-3060/3"].totalCostUsd); got != 1.5 {
		t.Fatalf("total_cost_usd = %v, want 1.5 inalterado", got)
	}
}

func TestAuxPodLifecycle_StopNeverStarted404(t *testing.T) {
	h := newAuxPodLifecycleHandlerWithQueries(newFakeAuxPodQueries(), opTestCfg(), discardLog())
	rec, _ := postAux(t, h, `{"pod":"vast-3060","event":"stop","vast_instance_id":42}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestAuxPodLifecycle_Validation400(t *testing.T) {
	h := newAuxPodLifecycleHandlerWithQueries(newFakeAuxPodQueries(), opTestCfg(), discardLog())
	cases := map[string]string{
		"pod vazio":          `{"pod":"","event":"start","vast_instance_id":1}`,
		"event inválido":     `{"pod":"vast-3060","event":"pause","vast_instance_id":1}`,
		"instance zero":      `{"pod":"vast-3060","event":"start","vast_instance_id":0}`,
		"instance negativo":  `{"pod":"vast-3060","event":"start","vast_instance_id":-3}`,
		"dph negativo":       `{"pod":"vast-3060","event":"start","vast_instance_id":1,"dph_usd":-0.1}`,
		"total negativo":     `{"pod":"vast-3060","event":"stop","vast_instance_id":1,"total_cost_usd":-1}`,
		"at inválido":        `{"pod":"vast-3060","event":"start","vast_instance_id":1,"at":"ontem"}`,
		"json quebrado":      `{"pod":`,
		"campo desconhecido": `{"pod":"vast-3060","event":"start","vast_instance_id":1,"x":1}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec, _ := postAux(t, h, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAuxPodLifecycle_BodyTooLarge400(t *testing.T) {
	h := newAuxPodLifecycleHandlerWithQueries(newFakeAuxPodQueries(), opTestCfg(), discardLog())
	// JSON válido com 70 KiB de espaços: só o MaxBytesReader pode recusar.
	big := `{"pod":"vast-3060","event":"start","vast_instance_id":1` + strings.Repeat(" ", 70<<10) + `}`
	rec, _ := postAux(t, h, big)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAuxPodLifecycle_DBError500(t *testing.T) {
	q := newFakeAuxPodQueries()
	q.err = fmt.Errorf("boom")
	h := newAuxPodLifecycleHandlerWithQueries(q, opTestCfg(), discardLog())
	rec, _ := postAux(t, h, `{"pod":"vast-3060","event":"start","vast_instance_id":1}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// TestAuxPodLifecycle_NoOwnAuth documenta o contrato: o handler NÃO valida
// X-Admin-Key — a autenticação é do adminRouter onde o 23-14 monta a rota
// (T-23-34). Sem header, o handler sozinho responde 200.
func TestAuxPodLifecycle_NoOwnAuth(t *testing.T) {
	h := newAuxPodLifecycleHandlerWithQueries(newFakeAuxPodQueries(), opTestCfg(), discardLog())
	rec, _ := postAux(t, h, `{"pod":"vast-3060","event":"start","vast_instance_id":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (auth é do adminRouter)", rec.Code)
	}
}
