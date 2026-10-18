package admin

// quick 261018 — priority / fallback_models na API admin de model_aliases.
// Contrato: ausente = mantém o valor atual da linha; null = limpa; valor = grava.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func putAlias(t *testing.T, fake *fakeAliasQueries, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := newModelAliasAdminHandlerWithQueries(fake, &countingRefresher{}, discardLog())
	rec := httptest.NewRecorder()
	h.Upsert(rec, httptest.NewRequest(http.MethodPut, "/admin/model-aliases", strings.NewReader(body)))
	return rec
}

func TestModelAliasUpsert_PriorityAndFallbackSet(t *testing.T) {
	fake := newAliasFake()
	rec := putAlias(t, fake, `{"alias":"jev","upstream_name":"openrouter-chat","target":"deepseek/x","priority":1,"fallback_models":["google/gemini-2.5-flash"]}`)
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	call := fake.upsertCalls[0]
	if !call.Priority.Valid || call.Priority.Int32 != 1 {
		t.Errorf("priority = %+v", call.Priority)
	}
	if len(call.FallbackModels) != 1 || call.FallbackModels[0] != "google/gemini-2.5-flash" {
		t.Errorf("fallback = %v", call.FallbackModels)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["priority"] != float64(1) {
		t.Errorf("response priority = %v", out["priority"])
	}
}

func TestModelAliasUpsert_AbsentFieldsKeepCurrent(t *testing.T) {
	fake := newAliasFake()
	fake.rows[1].Priority = pgtype.Int4{Int32: 7, Valid: true}
	fake.rows[1].FallbackModels = []string{"google/gemini-2.5-flash"}
	// Dashboard antigo: só target + prefs, sem os campos novos.
	rec := putAlias(t, fake, `{"alias":"qwen","upstream_name":"openrouter-chat","target":"deepseek/deepseek-v4-flash:nitro","provider_prefs":{"order":["novita"]}}`)
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	call := fake.upsertCalls[0]
	if !call.Priority.Valid || call.Priority.Int32 != 7 {
		t.Errorf("priority must be kept, got %+v", call.Priority)
	}
	if len(call.FallbackModels) != 1 {
		t.Errorf("fallback must be kept, got %v", call.FallbackModels)
	}
}

func TestModelAliasUpsert_NullClears(t *testing.T) {
	fake := newAliasFake()
	fake.rows[1].Priority = pgtype.Int4{Int32: 7, Valid: true}
	fake.rows[1].FallbackModels = []string{"google/gemini-2.5-flash"}
	rec := putAlias(t, fake, `{"alias":"qwen","upstream_name":"openrouter-chat","target":"deepseek/deepseek-v4-flash:nitro","priority":null,"fallback_models":null}`)
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	call := fake.upsertCalls[0]
	if call.Priority.Valid || call.FallbackModels != nil {
		t.Errorf("null must clear: priority=%+v fallback=%v", call.Priority, call.FallbackModels)
	}
}

func TestModelAliasUpsert_RoutingRejects(t *testing.T) {
	cases := map[string]string{
		"priority not int":        `{"alias":"a","upstream_name":"openrouter-chat","target":"x","priority":"1"}`,
		"priority float":          `{"alias":"a","upstream_name":"openrouter-chat","target":"x","priority":1.5}`,
		"fallback not list":       `{"alias":"a","upstream_name":"openrouter-chat","target":"x","fallback_models":"a/b"}`,
		"fallback repeats target": `{"alias":"a","upstream_name":"openrouter-chat","target":"x","fallback_models":["x"]}`,
		"fallback on local-llm":   `{"alias":"a","upstream_name":"local-llm","target":"x","fallback_models":["a/b"]}`,
		"fallback whitespace":     `{"alias":"a","upstream_name":"openrouter-chat","target":"x","fallback_models":["a b"]}`,
	}
	for name, body := range cases {
		fake := newAliasFake()
		rec := putAlias(t, fake, body)
		if rec.Code != 400 {
			t.Errorf("%s: status %d want 400 (body %s)", name, rec.Code, rec.Body.String())
		}
		if len(fake.upsertCalls) != 0 {
			t.Errorf("%s: rejected request reached the DB", name)
		}
	}
}

func TestModelAliasUpsert_KeptFallbackInvalidForNewTarget(t *testing.T) {
	fake := newAliasFake()
	fake.rows[1].FallbackModels = []string{"google/gemini-2.5-flash"}
	// Novo target = item da lista guardada → recusa em vez de gravar lixo.
	rec := putAlias(t, fake, `{"alias":"qwen","upstream_name":"openrouter-chat","target":"google/gemini-2.5-flash"}`)
	if rec.Code != 400 || len(fake.upsertCalls) != 0 {
		t.Fatalf("status %d calls %d, want 400 and no write", rec.Code, len(fake.upsertCalls))
	}
}

func TestModelAliasList_ExposesPriorityAndFallback(t *testing.T) {
	fake := newAliasFake()
	fake.rows[1].Priority = pgtype.Int4{Int32: 2, Valid: true}
	fake.rows[1].FallbackModels = []string{"google/gemini-2.5-flash"}
	h := newModelAliasAdminHandlerWithQueries(fake, nil, discardLog())
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/admin/model-aliases", nil))
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out[0]["priority"] != nil || out[0]["fallback_models"] != nil {
		t.Errorf("row without values must serialize null: %v", out[0])
	}
	if out[1]["priority"] != float64(2) {
		t.Errorf("priority = %v", out[1]["priority"])
	}
	if fb, _ := out[1]["fallback_models"].([]any); len(fb) != 1 {
		t.Errorf("fallback_models = %v", out[1]["fallback_models"])
	}
}
