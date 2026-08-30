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

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

type fakeAliasQueries struct {
	rows        []gen.ListModelAliasesRow
	upstreams   map[string]string // name → role
	upsertCalls []gen.UpsertModelAliasParams
	deleteCalls int
}

func (f *fakeAliasQueries) ListModelAliases(context.Context) ([]gen.ListModelAliasesRow, error) {
	return f.rows, nil
}

func (f *fakeAliasQueries) GetModelAlias(_ context.Context, arg gen.GetModelAliasParams) (gen.GetModelAliasRow, error) {
	for _, r := range f.rows {
		if r.Alias == arg.Alias && r.UpstreamName == arg.UpstreamName {
			return gen.GetModelAliasRow{Alias: r.Alias, Upstream: r.Upstream, Target: r.Target, UpstreamName: r.UpstreamName, ProviderPrefs: r.ProviderPrefs}, nil
		}
	}
	return gen.GetModelAliasRow{}, pgx.ErrNoRows
}

func (f *fakeAliasQueries) UpsertModelAlias(_ context.Context, arg gen.UpsertModelAliasParams) error {
	f.upsertCalls = append(f.upsertCalls, arg)
	return nil
}

func (f *fakeAliasQueries) DeleteModelAlias(context.Context, gen.DeleteModelAliasParams) error {
	f.deleteCalls++
	return nil
}

func (f *fakeAliasQueries) GetUpstreamByName(_ context.Context, name string) (gen.GetUpstreamByNameRow, error) {
	role, ok := f.upstreams[name]
	if !ok {
		return gen.GetUpstreamByNameRow{}, pgx.ErrNoRows
	}
	return gen.GetUpstreamByNameRow{Name: name, Role: role, Enabled: true}, nil
}

type countingRefresher struct{ calls int }

func (c *countingRefresher) Refresh(context.Context) error { c.calls++; return nil }

func newAliasFake() *fakeAliasQueries {
	return &fakeAliasQueries{
		rows: []gen.ListModelAliasesRow{
			{Alias: "qwen", Upstream: "llm", Target: "qwen", UpstreamName: "local-llm"},
			{Alias: "qwen", Upstream: "llm", Target: "deepseek/deepseek-v4-flash:nitro", UpstreamName: "openrouter-chat", ProviderPrefs: []byte(`{"order":["novita"]}`)},
		},
		upstreams: map[string]string{"local-llm": "llm", "openrouter-chat": "llm", "openai-embed": "embed"},
	}
}

func withAliasParams(r *http.Request, alias, upstream string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("alias", alias)
	rctx.URLParams.Add("upstream", upstream)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestModelAliasList_IncludesPrefsAsJSONOrNull(t *testing.T) {
	h := newModelAliasAdminHandlerWithQueries(newAliasFake(), nil, discardLog())
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/admin/model-aliases", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("rows = %d", len(out))
	}
	if out[0]["provider_prefs"] != nil {
		t.Errorf("row without prefs must serialize null, got %v", out[0]["provider_prefs"])
	}
	prefs, _ := out[1]["provider_prefs"].(map[string]any)
	if prefs == nil || prefs["order"] == nil {
		t.Errorf("row with prefs must serialize the object, got %v", out[1]["provider_prefs"])
	}
	if out[1]["role"] != "llm" {
		t.Errorf("role = %v", out[1]["role"])
	}
}

func TestModelAliasUpsert_ValidPrefs_WritesCanonicalAndRefreshes(t *testing.T) {
	fake := newAliasFake()
	ref := &countingRefresher{}
	h := newModelAliasAdminHandlerWithQueries(fake, ref, discardLog())
	body := `{"alias":"gpt-4o","upstream_name":"openrouter-chat","target":"deepseek/deepseek-v4-flash","provider_prefs":{"only":["novita","deepinfra"],"zdr":true,"max_price":{"prompt":0.14,"completion":0.5}}}`
	rec := httptest.NewRecorder()
	h.Upsert(rec, httptest.NewRequest(http.MethodPut, "/admin/model-aliases", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if len(fake.upsertCalls) != 1 {
		t.Fatalf("upsert calls = %d", len(fake.upsertCalls))
	}
	call := fake.upsertCalls[0]
	if call.Upstream != "llm" || call.UpstreamName != "openrouter-chat" || call.Target != "deepseek/deepseek-v4-flash" {
		t.Errorf("upsert params = %+v", call)
	}
	var prefs map[string]any
	if err := json.Unmarshal(call.ProviderPrefs, &prefs); err != nil || prefs["zdr"] != true {
		t.Errorf("stored prefs = %s (err %v)", call.ProviderPrefs, err)
	}
	if ref.calls != 1 {
		t.Errorf("resolver refresh calls = %d, want 1", ref.calls)
	}
}

func TestModelAliasUpsert_RejectsBeforeWrite(t *testing.T) {
	cases := map[string]struct {
		body string
		code int
	}{
		"invalid json":            {`{`, 400},
		"whitespace alias":        {`{"alias":"my alias","upstream_name":"openrouter-chat","target":"x"}`, 400},
		"missing target":          {`{"alias":"a","upstream_name":"openrouter-chat"}`, 400},
		"bad prefs field":         {`{"alias":"a","upstream_name":"openrouter-chat","target":"x","provider_prefs":{"nope":1}}`, 400},
		"bad prefs enum":          {`{"alias":"a","upstream_name":"openrouter-chat","target":"x","provider_prefs":{"data_collection":"maybe"}}`, 400},
		"prefs on non-openrouter": {`{"alias":"a","upstream_name":"local-llm","target":"x","provider_prefs":{"zdr":true}}`, 400},
		"unknown upstream":        {`{"alias":"a","upstream_name":"ghost","target":"x"}`, 404},
	}
	for name, tc := range cases {
		fake := newAliasFake()
		ref := &countingRefresher{}
		h := newModelAliasAdminHandlerWithQueries(fake, ref, discardLog())
		rec := httptest.NewRecorder()
		h.Upsert(rec, httptest.NewRequest(http.MethodPut, "/admin/model-aliases", strings.NewReader(tc.body)))
		if rec.Code != tc.code {
			t.Errorf("%s: status %d want %d (body %s)", name, rec.Code, tc.code, rec.Body.String())
		}
		if len(fake.upsertCalls) != 0 || ref.calls != 0 {
			t.Errorf("%s: rejected request reached the DB/refresher", name)
		}
	}
}

func TestModelAliasUpsert_NullPrefsClears(t *testing.T) {
	fake := newAliasFake()
	h := newModelAliasAdminHandlerWithQueries(fake, nil, discardLog())
	rec := httptest.NewRecorder()
	h.Upsert(rec, httptest.NewRequest(http.MethodPut, "/admin/model-aliases",
		strings.NewReader(`{"alias":"qwen","upstream_name":"openrouter-chat","target":"deepseek/x","provider_prefs":null}`)))
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fake.upsertCalls[0].ProviderPrefs != nil {
		t.Errorf("null prefs must store NULL, got %s", fake.upsertCalls[0].ProviderPrefs)
	}
}

func TestModelAliasDelete(t *testing.T) {
	fake := newAliasFake()
	ref := &countingRefresher{}
	h := newModelAliasAdminHandlerWithQueries(fake, ref, discardLog())

	rec := httptest.NewRecorder()
	h.Delete(rec, withAliasParams(httptest.NewRequest(http.MethodDelete, "/admin/model-aliases/qwen/openrouter-chat", nil), "qwen", "openrouter-chat"))
	if rec.Code != 204 || fake.deleteCalls != 1 || ref.calls != 1 {
		t.Errorf("delete existing: status %d deletes %d refresh %d", rec.Code, fake.deleteCalls, ref.calls)
	}

	rec = httptest.NewRecorder()
	h.Delete(rec, withAliasParams(httptest.NewRequest(http.MethodDelete, "/admin/model-aliases/ghost/openrouter-chat", nil), "ghost", "openrouter-chat"))
	if rec.Code != 404 || fake.deleteCalls != 1 {
		t.Errorf("delete missing: status %d deletes %d", rec.Code, fake.deleteCalls)
	}
}
