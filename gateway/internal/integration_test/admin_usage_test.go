//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/admin"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// TestAdminUsageResponseShape — SC-3 response shape: the handler must emit
// the exact fields the roadmap specifies so dashboards/apps can consume it
// without further contract churn.
//
// The handler resolves tenants authoritatively from billing_events (NOT
// usage_counters), so this test seeds 3 billing_events rows and asserts
// the summary + at-least-one rows entry carry SC-3 fields with non-zero
// values.
func TestAdminUsageResponseShape(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	// Seed 3 billing_events rows at different hours of today. Use SP-local
	// midnight (not UTC-truncated) so the handler's from=today filter
	// (which ParseInLocation into SP) covers all seed timestamps.
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	nowSP := time.Now().In(loc)
	todaySP := time.Date(nowSP.Year(), nowSP.Month(), nowSP.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < 3; i++ {
		// 04:00 / 05:00 / 06:00 SP — safely inside today regardless of
		// the actual wall clock at test start.
		ts := time.Date(nowSP.Year(), nowSP.Month(), nowSP.Day(), 4+i, 0, 0, 0, loc)
		if _, err := pool.Exec(ctx, `
			INSERT INTO ai_gateway.billing_events
				(request_id, ts, tenant_id, route, upstream, model,
				 tokens_in, tokens_out, audio_seconds, embeds_count,
				 cost_local_brl, cost_local_phantom_brl, cost_external_brl, source)
			VALUES ($1, $2, $3, 'chat', 'local-llm', 'qwen3.5-27b',
			        100, 200, 0, 0,
			        0, 0.500000, 0, 'final')
		`, uuid.New(), ts, seed.ConverseAITenantID); err != nil {
			t.Fatalf("seed billing_events row %d: %v", i, err)
		}
	}

	handler := admin.NewUsageHandler(gen.New(pool), discardLogger())

	from := todaySP.Format("2006-01-02")
	to := from
	u := "/admin/usage?tenant=" + seed.ConverseAITenantID.String() +
		"&from=" + from + "&to=" + to
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", u, nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	for _, key := range []string{"tenant", "range", "summary", "rows"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("response missing top-level key: %s", key)
		}
	}
	sum, _ := resp["summary"].(map[string]any)
	for _, key := range []string{
		"tokens_in", "tokens_out", "audio_seconds", "embeds_count",
		"cost_local_brl", "cost_local_phantom_brl", "cost_external_brl",
		"cost_total_brl", "requests_count",
	} {
		if _, ok := sum[key]; !ok {
			t.Errorf("summary missing key: %s", key)
		}
	}
	// Spot-check magnitudes: 3 rows × (100 in, 200 out, 0.5 phantom BRL).
	if tIn, _ := sum["tokens_in"].(float64); tIn != 300 {
		t.Errorf("summary.tokens_in: want 300, got %v", sum["tokens_in"])
	}
	if tOut, _ := sum["tokens_out"].(float64); tOut != 600 {
		t.Errorf("summary.tokens_out: want 600, got %v", sum["tokens_out"])
	}
	if requests, _ := sum["requests_count"].(float64); requests != 3 {
		t.Errorf("summary.requests_count: want 3, got %v", sum["requests_count"])
	}
	rows, _ := resp["rows"].([]any)
	if len(rows) == 0 {
		t.Error("rows: expected at least one daily row for today")
	}
}

// TestAdminUsageAuthBCrypt — D-D3 auth contract: missing → 401
// missing_admin_key; invalid → 401 invalid_admin_key; valid → 200.
func TestAdminUsageAuthBCrypt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	v := admin.NewVerifier(gen.New(pool), rdb, discardLogger())
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	chain := admin.Middleware(v, discardLogger())(inner)

	cases := []struct {
		name    string
		header  string
		want    int
		wantErr string
	}{
		{"missing header", "", http.StatusUnauthorized, "missing_admin_key"},
		{"invalid key", "ifix_admin_does_not_exist_" + uuid.NewString(), http.StatusUnauthorized, "invalid_admin_key"},
		{"valid key", seed.AdminKeyRaw, http.StatusOK, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/admin/usage", nil)
			if c.header != "" {
				req.Header.Set("X-Admin-Key", c.header)
			}
			chain.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("status: want %d, got %d body=%s",
					c.want, rec.Code, rec.Body.String())
			}
			if c.wantErr == "" {
				return
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if body.Error.Code != c.wantErr {
				t.Errorf("error.code: want %q, got %q", c.wantErr, body.Error.Code)
			}
		})
	}
}
