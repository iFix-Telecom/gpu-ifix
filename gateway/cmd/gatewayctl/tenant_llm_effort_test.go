package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

type fakeLLMEffortUpdater struct {
	calls []gen.UpdateTenantLLMEffortParams
	rows  int64
	err   error
}

func (f *fakeLLMEffortUpdater) UpdateTenantLLMEffort(_ context.Context, arg gen.UpdateTenantLLMEffortParams) (int64, error) {
	f.calls = append(f.calls, arg)
	return f.rows, f.err
}

func runLLMEffortWith(t *testing.T, f *fakeLLMEffortUpdater, args ...string) (int, string, string, bool) {
	t.Helper()
	var out, errb bytes.Buffer
	connected := false
	code := setTenantLLMEffort(context.Background(), args, &out, &errb, func(context.Context) (llmEffortUpdater, func(), error) {
		connected = true
		return f, func() {}, nil
	})
	return code, out.String(), errb.String(), connected
}

func TestTenantSetLLMEffort_Set(t *testing.T) {
	f := &fakeLLMEffortUpdater{rows: 1}
	code, out, _, _ := runLLMEffortWith(t, f, "--slug", "chat-ifix", "--effort", "medium")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if len(f.calls) != 1 || f.calls[0].Slug != "chat-ifix" || !f.calls[0].LlmEffort.Valid || f.calls[0].LlmEffort.String != "medium" {
		t.Fatalf("calls=%+v", f.calls)
	}
	if strings.TrimSpace(out) != "tenant set-llm-effort: slug=chat-ifix llm_effort=medium" {
		t.Errorf("stdout=%q", out)
	}
}

func TestTenantSetLLMEffort_Clear(t *testing.T) {
	f := &fakeLLMEffortUpdater{rows: 1}
	code, out, _, _ := runLLMEffortWith(t, f, "--slug", "x", "--clear")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if len(f.calls) != 1 || f.calls[0].LlmEffort.Valid {
		t.Fatalf("calls=%+v, want Valid=false", f.calls)
	}
	if !strings.Contains(out, "llm_effort=-") {
		t.Errorf("stdout=%q", out)
	}
}

func TestTenantSetLLMEffort_InvalidoNaoTocaBanco(t *testing.T) {
	f := &fakeLLMEffortUpdater{rows: 1}
	code, _, errOut, connected := runLLMEffortWith(t, f, "--slug", "x", "--effort", "turbo")
	if code == 0 {
		t.Fatal("esperava exit != 0")
	}
	if connected || len(f.calls) != 0 {
		t.Fatal("não deveria conectar no banco")
	}
	for _, v := range []string{"off", "low", "medium", "high", "xhigh", "max"} {
		if !strings.Contains(errOut, v) {
			t.Errorf("mensagem não lista %q: %q", v, errOut)
		}
	}
}

func TestTenantSetLLMEffort_EffortEClearJuntos(t *testing.T) {
	f := &fakeLLMEffortUpdater{rows: 1}
	code, _, _, connected := runLLMEffortWith(t, f, "--slug", "x", "--effort", "low", "--clear")
	if code == 0 || connected {
		t.Fatalf("code=%d connected=%v", code, connected)
	}
}

func TestTenantSetLLMEffort_NenhumDosDois(t *testing.T) {
	f := &fakeLLMEffortUpdater{rows: 1}
	code, _, _, connected := runLLMEffortWith(t, f, "--slug", "x")
	if code == 0 || connected {
		t.Fatalf("code=%d connected=%v", code, connected)
	}
}

func TestTenantSetLLMEffort_SemSlug(t *testing.T) {
	f := &fakeLLMEffortUpdater{rows: 1}
	code, _, _, connected := runLLMEffortWith(t, f, "--effort", "low")
	if code == 0 || connected {
		t.Fatalf("code=%d connected=%v", code, connected)
	}
}

func TestTenantSetLLMEffort_TenantNotFound(t *testing.T) {
	f := &fakeLLMEffortUpdater{rows: 0}
	code, _, errOut, _ := runLLMEffortWith(t, f, "--slug", "nao-existe", "--effort", "low")
	if code == 0 {
		t.Fatal("esperava exit != 0")
	}
	if !strings.Contains(errOut, "tenant not found") {
		t.Errorf("stderr=%q", errOut)
	}
}

func TestTenantSetLLMEffort_ErroDoBanco(t *testing.T) {
	f := &fakeLLMEffortUpdater{err: errors.New("boom")}
	code, _, _, _ := runLLMEffortWith(t, f, "--slug", "x", "--effort", "low")
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
}
