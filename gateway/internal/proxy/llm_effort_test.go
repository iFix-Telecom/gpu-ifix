package proxy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
)

// fakeEffortLookup implementa TenantPrefsLookup e TenantEffortLookup (como o
// *tenants.Loader real) com um mapa tenantID → effort.
type fakeEffortLookup map[string]string

func (f fakeEffortLookup) ProviderPrefsByTenantID(string) []byte { return nil }
func (f fakeEffortLookup) LLMEffortByTenantID(id string) string  { return f[id] }

const effortTestTenant = "11111111-1111-1111-1111-111111111111"

func effortCtx() context.Context {
	return auth.WithContext(context.Background(), auth.AuthContext{TenantID: effortTestTenant})
}

func bodyMap(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	return m
}

func TestResolveLLMEffort(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		tenant     string
		nilLookup  bool
		noAuth     bool
		wantEffort string
		wantSource string
	}{
		{"sem_reasoning_lookup_nil", `{"model":"x"}`, "", true, false, "high", "default"},
		{"tenant_low", `{"model":"x"}`, "low", false, false, "low", "tenant"},
		{"cliente_reasoning_effort_vence_tenant", `{"reasoning_effort":"medium"}`, "low", false, false, "medium", "client"},
		{"cliente_enabled_false", `{"reasoning":{"enabled":false}}`, "high", false, false, "off", "client"},
		{"cliente_reasoning_effort_obj", `{"reasoning":{"effort":"xhigh"}}`, "", false, false, "xhigh", "client"},
		{"cliente_invalido_cai_default", `{"reasoning_effort":"turbo"}`, "", false, false, "high", "default"},
		{"cliente_invalido_cai_tenant", `{"reasoning":{"effort":"turbo"}}`, "medium", false, false, "medium", "tenant"},
		{"tenant_invalido_cai_default", `{"model":"x"}`, "turbo", false, false, "high", "default"},
		{"sem_auth_ignora_tenant", `{"model":"x"}`, "low", false, true, "high", "default"},
		{"reasoning_effort_precede_objeto", `{"reasoning_effort":"low","reasoning":{"effort":"max"}}`, "", false, false, "low", "client"},
		{"enabled_true_sem_effort_cai_tenant", `{"reasoning":{"enabled":true}}`, "medium", false, false, "medium", "tenant"},
		{"cliente_off_string", `{"reasoning_effort":"off"}`, "", false, false, "off", "client"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var lk TenantEffortLookup
			if !c.nilLookup {
				lk = fakeEffortLookup{effortTestTenant: c.tenant}
			}
			ctx := effortCtx()
			if c.noAuth {
				ctx = context.Background()
			}
			e, src := ResolveLLMEffort(ctx, bodyMap(t, c.body), lk)
			if e != c.wantEffort || src != c.wantSource {
				t.Errorf("got (%q,%q), want (%q,%q)", e, src, c.wantEffort, c.wantSource)
			}
		})
	}
}

func TestDefaultLLMEffortIsHigh(t *testing.T) {
	if DefaultLLMEffort != "high" {
		t.Fatalf("DefaultLLMEffort=%q", DefaultLLMEffort)
	}
}
