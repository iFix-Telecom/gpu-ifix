package models

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

type fakeAliasQueries struct{ rows []gen.ListModelAliasesRow }

func (f *fakeAliasQueries) ListModelAliases(context.Context) ([]gen.ListModelAliasesRow, error) {
	return f.rows, nil
}

func TestValidateProviderPrefs_AcceptsScreenshotShape(t *testing.T) {
	raw := []byte(`{
	  "only": ["provedor-a","provedor-b","provedor-c"],
	  "order": ["provedor-a","provedor-b","provedor-c"],
	  "allow_fallbacks": true,
	  "quantizations": ["bf16","fp16"],
	  "max_price": {"prompt": 0.14, "completion": 0.50},
	  "preferred_min_throughput": {"p90": 50},
	  "preferred_max_latency": {"p90": 3},
	  "data_collection": "deny",
	  "zdr": true
	}`)
	out, err := ValidateProviderPrefs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("canonical output not JSON: %v", err)
	}
	for _, k := range []string{"only", "order", "allow_fallbacks", "quantizations", "max_price", "preferred_min_throughput", "preferred_max_latency", "data_collection", "zdr"} {
		if _, ok := m[k]; !ok {
			t.Errorf("canonical output lost key %q: %s", k, out)
		}
	}
	if m["data_collection"] != "deny" {
		t.Errorf("data_collection = %v", m["data_collection"])
	}
}

func TestValidateProviderPrefs_Rejects(t *testing.T) {
	cases := map[string]string{
		"unknown field":       `{"orderr":["x"]}`,
		"bad data_collection": `{"data_collection":"maybe"}`,
		"bad quantization":    `{"quantizations":["q4"]}`,
		"negative price":      `{"max_price":{"prompt":-1}}`,
		"empty max_price":     `{"max_price":{}}`,
		"empty list":          `{"order":[]}`,
		"whitespace slug":     `{"only":["nov ita"]}`,
		"bad sort":            `{"sort":"cheapest"}`,
		"bad sort partition":  `{"sort":{"by":"price","partition":"foo"}}`,
		"bad percentile":      `{"preferred_max_latency":{"p42":3}}`,
		"negative threshold":  `{"preferred_min_throughput":-5}`,
		"empty object":        `{}`,
		"empty":               ``,
		"not object":          `[1,2]`,
		"trailing":            `{"zdr":true} x`,
	}
	for name, raw := range cases {
		if _, err := ValidateProviderPrefs([]byte(raw)); err == nil {
			t.Errorf("%s: expected error for %s", name, raw)
		}
	}
	big := `{"order":["` + strings.Repeat("a", ProviderPrefsMaxBytes) + `"]}`
	if _, err := ValidateProviderPrefs([]byte(big)); err == nil {
		t.Errorf("expected size cap error")
	}
}

func TestValidateProviderPrefs_SortAndThresholdForms(t *testing.T) {
	ok := []string{
		`{"sort":"price"}`,
		`{"sort":{"by":"throughput","partition":"none"}}`,
		`{"preferred_min_throughput":50}`,
		`{"preferred_max_latency":{"p50":1,"p99":5}}`,
		`{"require_parameters":true,"ignore":["deepinfra"]}`,
	}
	for _, raw := range ok {
		if _, err := ValidateProviderPrefs([]byte(raw)); err != nil {
			t.Errorf("%s: unexpected error %v", raw, err)
		}
	}
}

func TestResolver_ProviderPrefsFromRefresh(t *testing.T) {
	q := &fakeAliasQueries{rows: []gen.ListModelAliasesRow{
		{Alias: "qwen", UpstreamName: "openrouter-chat", Target: "deepseek/x", ProviderPrefs: []byte(`{"order":["novita"]}`)},
		{Alias: "qwen", UpstreamName: "local-llm", Target: "qwen"},
	}}
	r := &Resolver{q: q, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.ProviderPrefs("qwen", "openrouter-chat"); string(got) != `{"order":["novita"]}` {
		t.Errorf("prefs = %s", got)
	}
	if got := r.ProviderPrefs("qwen", "local-llm"); got != nil {
		t.Errorf("expected nil prefs for row without column, got %s", got)
	}
	if got := r.ProviderPrefs("nope", "openrouter-chat"); got != nil {
		t.Errorf("expected nil prefs for unknown alias, got %s", got)
	}
}
