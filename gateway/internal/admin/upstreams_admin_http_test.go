package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

type fakeUpstreamQueries struct {
	rows     []gen.ListAllUpstreamsRow
	setCalls []gen.SetUpstreamEnabledParams
}

func (f *fakeUpstreamQueries) ListAllUpstreams(context.Context) ([]gen.ListAllUpstreamsRow, error) {
	return f.rows, nil
}

func (f *fakeUpstreamQueries) GetUpstreamByName(_ context.Context, name string) (gen.GetUpstreamByNameRow, error) {
	for _, r := range f.rows {
		if r.Name == name {
			return gen.GetUpstreamByNameRow{Name: r.Name, Role: r.Role, Tier: r.Tier, Enabled: r.Enabled}, nil
		}
	}
	return gen.GetUpstreamByNameRow{}, pgx.ErrNoRows
}

func (f *fakeUpstreamQueries) SetUpstreamEnabled(_ context.Context, arg gen.SetUpstreamEnabledParams) error {
	f.setCalls = append(f.setCalls, arg)
	return nil
}

func newUpstreamFake() *fakeUpstreamQueries {
	return &fakeUpstreamQueries{rows: []gen.ListAllUpstreamsRow{
		{Name: "local-llm", Role: "llm", Tier: 0, Enabled: true, UrlEnv: "UPSTREAM_LLM_URL",
			LastProbeStatus: pgtype.Text{String: "ok", Valid: true}, LastProbeMs: pgtype.Int4{Int32: 12, Valid: true}},
		{Name: "openrouter-chat", Role: "llm", Tier: 1, Enabled: true, UrlEnv: "UPSTREAM_LLM_OPENROUTER_URL",
			AuthBearerEnv: pgtype.Text{String: "UPSTREAM_LLM_OPENROUTER_AUTH_BEARER", Valid: true}},
		{Name: "kokoro-tts", Role: "tts", Tier: 0, Enabled: true, UrlEnv: "UPSTREAM_TTS_KOKORO_URL"},
		{Name: "local-tts", Role: "tts", Tier: 1, Enabled: false, UrlEnv: "UPSTREAM_TTS_URL"},
	}}
}

func withName(r *http.Request, name string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", name)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestUpstreamList_TypedRowsNoSecrets(t *testing.T) {
	h := newUpstreamAdminHandlerWithQueries(newUpstreamFake(), discardLog())
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/admin/upstreams", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("rows = %d", len(out))
	}
	if out[1]["has_auth"] != true || out[0]["has_auth"] != false {
		t.Errorf("has_auth wrong: %v / %v", out[0]["has_auth"], out[1]["has_auth"])
	}
	if strings.Contains(rec.Body.String(), "auth_bearer_env") {
		t.Errorf("auth env NAME must not be exposed as a field: %s", rec.Body.String())
	}
	if out[0]["last_probe_status"] != "ok" || out[1]["last_probe_status"] != nil {
		t.Errorf("probe status mapping wrong: %v / %v", out[0]["last_probe_status"], out[1]["last_probe_status"])
	}
}

func TestUpstreamSetEnabled_DisableWithSiblingOK(t *testing.T) {
	fake := newUpstreamFake()
	h := newUpstreamAdminHandlerWithQueries(fake, discardLog())
	rec := httptest.NewRecorder()
	h.SetEnabled(rec, withName(httptest.NewRequest(http.MethodPost, "/admin/upstreams/openrouter-chat/enabled", strings.NewReader(`{"enabled":false}`)), "openrouter-chat"))
	if rec.Code != 204 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if len(fake.setCalls) != 1 || fake.setCalls[0].Enabled || fake.setCalls[0].Name != "openrouter-chat" {
		t.Errorf("set calls = %+v", fake.setCalls)
	}
}

func TestUpstreamSetEnabled_RefusesLastEnabledOfRole(t *testing.T) {
	fake := newUpstreamFake()
	h := newUpstreamAdminHandlerWithQueries(fake, discardLog())
	rec := httptest.NewRecorder()
	// kokoro-tts is the ONLY enabled tts upstream (local-tts is disabled).
	h.SetEnabled(rec, withName(httptest.NewRequest(http.MethodPost, "/admin/upstreams/kokoro-tts/enabled", strings.NewReader(`{"enabled":false}`)), "kokoro-tts"))
	if rec.Code != 409 {
		t.Fatalf("status %d want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if len(fake.setCalls) != 0 {
		t.Errorf("guard must block the write; calls=%+v", fake.setCalls)
	}
	// Re-enabling is always allowed.
	rec = httptest.NewRecorder()
	h.SetEnabled(rec, withName(httptest.NewRequest(http.MethodPost, "/admin/upstreams/local-tts/enabled", strings.NewReader(`{"enabled":true}`)), "local-tts"))
	if rec.Code != 204 || len(fake.setCalls) != 1 || !fake.setCalls[0].Enabled {
		t.Errorf("enable: status %d calls %+v", rec.Code, fake.setCalls)
	}
}

func TestUpstreamSetEnabled_BadInputs(t *testing.T) {
	fake := newUpstreamFake()
	h := newUpstreamAdminHandlerWithQueries(fake, discardLog())
	for name, tc := range map[string]struct {
		upstream, body string
		code           int
	}{
		"missing enabled": {"local-llm", `{}`, 400},
		"bad json":        {"local-llm", `{`, 400},
		"unknown":         {"ghost", `{"enabled":true}`, 404},
	} {
		rec := httptest.NewRecorder()
		h.SetEnabled(rec, withName(httptest.NewRequest(http.MethodPost, "/admin/upstreams/x/enabled", strings.NewReader(tc.body)), tc.upstream))
		if rec.Code != tc.code {
			t.Errorf("%s: status %d want %d", name, rec.Code, tc.code)
		}
	}
	if len(fake.setCalls) != 0 {
		t.Errorf("rejected inputs reached the DB: %+v", fake.setCalls)
	}
}
