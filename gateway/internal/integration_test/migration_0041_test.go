//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// Phase 23 (23-03) — migrations 0041 (cadeia Haiku + llm_effort + units de
// cache + colunas de billing) e 0042 (aux_pod_lifecycles).
//
// Os testes partem do HEAD (freshSchema), descem para 0040 com Down(2),
// preparam o estado "de prod" e sobem de novo. O Postgres é compartilhado
// pelo pacote: todo teste que deixa o banco fora do HEAD registra um
// t.Cleanup que desfaz a preparação e roda db.Up.

type upRow struct {
	Tier     int
	Priority int
	Enabled  bool
}

func upstreamRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) (upRow, bool) {
	t.Helper()
	var r upRow
	err := pool.QueryRow(ctx,
		`SELECT tier, tier_priority, enabled FROM ai_gateway.upstreams WHERE name = $1`, name).
		Scan(&r.Tier, &r.Priority, &r.Enabled)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return upRow{}, false
		}
		t.Fatalf("upstreamRow(%s): %v", name, err)
	}
	return r, true
}

// ttsTier1 devolve name → tier_priority das linhas tts tier 1.
func ttsTier1(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT name, tier_priority FROM ai_gateway.upstreams WHERE role = 'tts' AND tier = 1`)
	if err != nil {
		t.Fatalf("ttsTier1: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var n string
		var p int
		if err := rows.Scan(&n, &p); err != nil {
			t.Fatalf("ttsTier1 scan: %v", err)
		}
		out[n] = p
	}
	return out
}

func columnExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, col string) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema='ai_gateway' AND table_name=$1 AND column_name=$2)`, table, col).Scan(&ok); err != nil {
		t.Fatalf("columnExists(%s.%s): %v", table, col, err)
	}
	return ok
}

func tableExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('ai_gateway.' || $1) IS NOT NULL`, table).Scan(&ok); err != nil {
		t.Fatalf("tableExists(%s): %v", table, err)
	}
	return ok
}

// at0040 leva o banco ao estado anterior à 0041 (Down(3) desde a 0043) e registra a volta ao HEAD.
// cleanupSQL roda antes do db.Up do cleanup (desfaz a preparação do teste).
func at0040(t *testing.T, ctx context.Context, cleanupSQL ...string) *pgxpool.Pool {
	t.Helper()
	pool, _ := freshSchema(t, ctx)
	// 0044 (model_aliases priority/fallback_models) HEAD bump: Down(3)→Down(4).
	if err := db.Down(ctx, pool, 4); err != nil {
		t.Fatalf("db.Down(4) revert 0044+0043+0042+0041: %v", err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, q := range cleanupSQL {
			if _, err := pool.Exec(cctx, q); err != nil {
				t.Errorf("cleanup %q: %v", q, err)
			}
		}
		if err := db.Up(cctx, pool); err != nil {
			t.Errorf("cleanup db.Up: %v", err)
		}
	})
	return pool
}

func assertPostUp(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ttsBefore map[string]int) {
	t.Helper()
	want := map[string]upRow{
		"anthropic-haiku":     {Tier: 1, Priority: 0, Enabled: false},
		"openrouter-chat":     {Tier: 1, Priority: 10, Enabled: true},
		"openrouter-deepseek": {Tier: 1, Priority: 20, Enabled: false},
		"openrouter-tts":      {Tier: 1, Priority: 0, Enabled: true},
	}
	for name, w := range want {
		got, ok := upstreamRow(t, ctx, pool, name)
		if !ok {
			t.Fatalf("upstream %s ausente após Up", name)
		}
		if got != w {
			t.Fatalf("upstream %s = %+v, want %+v", name, got, w)
		}
	}
	after := ttsTier1(t, ctx, pool)
	for name, p := range ttsBefore {
		if after[name] != p+10 {
			t.Fatalf("tts tier1 %s prio = %d, want %d (antes %d +10)", name, after[name], p+10, p)
		}
	}
	if len(after) != len(ttsBefore)+1 {
		t.Fatalf("tts tier1 rows = %v, want antes %v + openrouter-tts", after, ttsBefore)
	}
	if !columnExists(t, ctx, pool, "tenants", "llm_effort") {
		t.Fatal("tenants.llm_effort ausente após Up")
	}
	for _, c := range []string{"tokens_cache_read", "tokens_cache_write", "cost_source"} {
		if !columnExists(t, ctx, pool, "billing_events", c) {
			t.Fatalf("billing_events.%s ausente após Up", c)
		}
	}
	if !tableExists(t, ctx, pool, "aux_pod_lifecycles") {
		t.Fatal("aux_pod_lifecycles ausente após Up")
	}
}

// TestMigration0041_UpDown — Up cria a cadeia (habilitação adiada), desloca o
// tier 1 de tts, cria llm_effort/units/colunas/tabela; Down(2) restaura o
// estado 0040; Up de novo reproduz o mesmo resultado.
func TestMigration0041_UpDown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool := at0040(t, ctx)

	ttsBefore := ttsTier1(t, ctx, pool)
	chatBefore, ok := upstreamRow(t, ctx, pool, "openrouter-chat")
	if !ok || chatBefore.Priority != 0 {
		t.Fatalf("pré-condição: openrouter-chat em 0040 = %+v ok=%v, want prio 0", chatBefore, ok)
	}

	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("db.Up: %v", err)
	}
	pool.Reset()
	assertPostUp(t, ctx, pool, ttsBefore)

	// Janela segura migrate → troca de imagem: mesmo com todos os envs
	// presentes, o loader (só linhas enabled) não carrega os dois novos LLMs.
	clearUpstreamEnvs(t)
	t.Setenv("UPSTREAM_LLM_OPENROUTER_URL", "http://openrouter.test/api/v1")
	t.Setenv("UPSTREAM_LLM_OPENROUTER_AUTH_BEARER", "sk-test")
	t.Setenv("UPSTREAM_LLM_ANTHROPIC_URL", "http://anthropic.test")
	t.Setenv("UPSTREAM_LLM_ANTHROPIC_API_KEYS", "sk-ant-test")
	loader, err := upstreams.NewLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	if err := loader.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	for _, n := range []string{"anthropic-haiku", "openrouter-deepseek"} {
		if _, ok := loader.Get(n); ok {
			t.Fatalf("loader carregou %s (deveria nascer enabled=false)", n)
		}
	}
	if _, ok := loader.Get("openrouter-chat"); !ok {
		t.Fatal("loader não carregou openrouter-chat")
	}

	// llm_effort: CHECK aceita a lista e rejeita valor fora dela (T-23-07).
	tenantID := seedTenant(t, ctx, pool, "mig0041-tenant", "normal")
	if _, err := pool.Exec(ctx, `UPDATE ai_gateway.tenants SET llm_effort='high' WHERE id=$1`, tenantID); err != nil {
		t.Fatalf("llm_effort='high': %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_gateway.tenants SET llm_effort='bogus' WHERE id=$1`, tenantID); err == nil {
		t.Fatal("llm_effort='bogus' aceito; CHECK deveria rejeitar")
	}

	// Alterar llm_effort dispara o NOTIFY de hot-reload.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := conn.Exec(ctx, `LISTEN tenants_changed`); err != nil {
		conn.Release()
		t.Fatalf("LISTEN: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_gateway.tenants SET llm_effort='low' WHERE id=$1`, tenantID); err != nil {
		conn.Release()
		t.Fatalf("llm_effort='low': %v", err)
	}
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	n, err := conn.Conn().WaitForNotification(wctx)
	wcancel()
	_, _ = conn.Exec(ctx, `UNLISTEN *`)
	conn.Release()
	if err != nil {
		t.Fatalf("NOTIFY tenants_changed não chegou após UPDATE llm_effort: %v", err)
	}
	if n.Payload != tenantID.String() {
		t.Fatalf("NOTIFY payload = %q, want %s", n.Payload, tenantID)
	}

	// prices.unit aceita as units de cache.
	for _, u := range []string{"cache_read_token", "cache_write_token", "cache_write_1h_token"} {
		if _, err := pool.Exec(ctx, `INSERT INTO ai_gateway.prices (model, provider, unit, unit_cost_usd, notes)
			VALUES ('claude-haiku-test', 'anthropic', $1, 0.0000001, 'mig0041 test')`, u); err != nil {
			t.Fatalf("INSERT prices unit=%s: %v", u, err)
		}
	}

	// Down(2) → estado 0040.
	// 0044 (model_aliases priority/fallback_models) HEAD bump: Down(3)→Down(4).
	if err := db.Down(ctx, pool, 4); err != nil {
		t.Fatalf("db.Down(4): %v", err)
	}
	pool.Reset()
	chat, _ := upstreamRow(t, ctx, pool, "openrouter-chat")
	if chat.Priority != 0 {
		t.Fatalf("openrouter-chat prio após Down = %d, want 0", chat.Priority)
	}
	for _, n := range []string{"anthropic-haiku", "openrouter-deepseek", "openrouter-tts"} {
		if _, ok := upstreamRow(t, ctx, pool, n); ok {
			t.Fatalf("%s ainda existe após Down", n)
		}
	}
	if got := ttsTier1(t, ctx, pool); len(got) != len(ttsBefore) {
		t.Fatalf("tts tier1 após Down = %v, want %v", got, ttsBefore)
	} else {
		for name, p := range ttsBefore {
			if got[name] != p {
				t.Fatalf("tts tier1 %s após Down = %d, want %d", name, got[name], p)
			}
		}
	}
	if columnExists(t, ctx, pool, "tenants", "llm_effort") {
		t.Fatal("tenants.llm_effort ainda existe após Down")
	}
	for _, c := range []string{"tokens_cache_read", "tokens_cache_write", "cost_source"} {
		if columnExists(t, ctx, pool, "billing_events", c) {
			t.Fatalf("billing_events.%s ainda existe após Down", c)
		}
	}
	if tableExists(t, ctx, pool, "aux_pod_lifecycles") {
		t.Fatal("aux_pod_lifecycles ainda existe após Down")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_gateway.prices (model, provider, unit, unit_cost_usd)
		VALUES ('claude-haiku-test', 'anthropic', 'cache_read_token', 0.0000001)`); err == nil {
		t.Fatal("CHECK original de prices.unit não foi restaurada no Down")
	}
	// Trigger voltou ao formato 0037 e continua sendo único.
	var trig int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM pg_trigger
		WHERE tgname='tenants_update_notify' AND NOT tgisinternal`).Scan(&trig); err != nil {
		t.Fatalf("trigger query: %v", err)
	}
	if trig != 1 {
		t.Fatalf("tenants_update_notify após Down = %d, want 1", trig)
	}

	// Up de novo: idempotente (mesmo estado, sem deslocar o tts duas vezes).
	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("re-Up: %v", err)
	}
	pool.Reset()
	assertPostUp(t, ctx, pool, ttsBefore)
}

// TestMigration0041_TTSDivergentPriorities — prod não segue as prioridades
// das migrations (local-tts reapontado para o tier 1). O Up não assume
// prioridade fixa: desloca tudo +10 e põe openrouter-tts na frente.
func TestMigration0041_TTSDivergentPriorities(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool := at0040(t, ctx,
		// Volta local-tts ao tier 0 (estado das migrations) antes do re-Up.
		`UPDATE ai_gateway.upstreams SET tier = 0, tier_priority = 0 WHERE name = 'local-tts'`,
		`UPDATE ai_gateway.upstreams SET tier_priority = 0 WHERE name = 'kokoro-tts'`,
	)

	if _, err := pool.Exec(ctx,
		`UPDATE ai_gateway.upstreams SET tier_priority = 0 WHERE name = 'kokoro-tts' AND role = 'tts' AND tier = 1`); err != nil {
		t.Fatalf("prep kokoro-tts: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE ai_gateway.upstreams SET tier = 1, tier_priority = 5 WHERE name = 'local-tts'`); err != nil {
		t.Fatalf("prep local-tts: %v", err)
	}
	before := ttsTier1(t, ctx, pool)
	if before["kokoro-tts"] != 0 || before["local-tts"] != 5 {
		t.Fatalf("pré-condição tts tier1 = %v", before)
	}

	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("db.Up com prioridades divergentes: %v", err)
	}
	after := ttsTier1(t, ctx, pool)
	if after["openrouter-tts"] != 0 || after["kokoro-tts"] != 10 || after["local-tts"] != 15 {
		t.Fatalf("tts tier1 após Up = %v, want openrouter-tts 0, kokoro-tts 10, local-tts 15", after)
	}
	for name, p := range after {
		if name != "openrouter-tts" && p <= after["openrouter-tts"] {
			t.Fatalf("%s (prio %d) não ficou atrás de openrouter-tts", name, p)
		}
	}

	// Down restaura as prioridades divergentes originais.
	// 0044 (model_aliases priority/fallback_models) HEAD bump: Down(3)→Down(4).
	if err := db.Down(ctx, pool, 4); err != nil {
		t.Fatalf("db.Down(4): %v", err)
	}
	got := ttsTier1(t, ctx, pool)
	if got["kokoro-tts"] != 0 || got["local-tts"] != 5 || len(got) != 2 {
		t.Fatalf("tts tier1 após Down = %v, want kokoro-tts 0, local-tts 5", got)
	}
}

func assertUpFails(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wantSubstr string) {
	t.Helper()
	err := db.Up(ctx, pool)
	if err == nil {
		t.Fatalf("db.Up sucesso; esperado erro contendo %q", wantSubstr)
	}
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("db.Up erro = %v; want contendo %q", err, wantSubstr)
	}
	if strings.Contains(err.Error(), "upstreams_role_tier_priority_key") {
		t.Fatalf("db.Up falhou com erro cru de UNIQUE em vez da guarda: %v", err)
	}
	// A migration roda em transação: nada da 0041 pode ter ficado aplicado.
	if _, ok := upstreamRow(t, ctx, pool, "anthropic-haiku"); ok {
		t.Fatal("anthropic-haiku persistiu após Up falho (transação não reverteu)")
	}
}

// TestMigration0041_OccupiedLLMSlotFails — (llm,1,20) ocupada por outro
// upstream → Up falha com a mensagem da guarda.
func TestMigration0041_OccupiedLLMSlotFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := at0040(t, ctx, `DELETE FROM ai_gateway.upstreams WHERE name = 'fake-llm-20'`)
	if _, err := pool.Exec(ctx, `INSERT INTO ai_gateway.upstreams (name, role, tier, tier_priority, url_env)
		VALUES ('fake-llm-20', 'llm', 1, 20, 'UPSTREAM_FAKE_URL')`); err != nil {
		t.Fatalf("prep fake-llm-20: %v", err)
	}
	assertUpFails(t, ctx, pool, "phase23 0041")
}

// TestMigration0041_OccupiedSlot10Fails — (llm,1,10) ocupada → a guarda roda
// ANTES do UPDATE do openrouter-chat, então o erro é o da guarda e não o de
// UNIQUE(role,tier,tier_priority).
func TestMigration0041_OccupiedSlot10Fails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := at0040(t, ctx, `DELETE FROM ai_gateway.upstreams WHERE name = 'fake-llm-10'`)
	if _, err := pool.Exec(ctx, `INSERT INTO ai_gateway.upstreams (name, role, tier, tier_priority, url_env)
		VALUES ('fake-llm-10', 'llm', 1, 10, 'UPSTREAM_FAKE_URL')`); err != nil {
		t.Fatalf("prep fake-llm-10: %v", err)
	}
	assertUpFails(t, ctx, pool, "phase23 0041")
}

// TestMigration0041_SameNameWrongDefinitionFails — openrouter-deepseek criado
// à mão com url_env errado: o ON CONFLICT (name) DO NOTHING preservaria a
// linha; a conferência final de definição faz o Up falhar.
func TestMigration0041_SameNameWrongDefinitionFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := at0040(t, ctx, `DELETE FROM ai_gateway.upstreams WHERE name = 'openrouter-deepseek'`)
	if _, err := pool.Exec(ctx, `INSERT INTO ai_gateway.upstreams (name, role, tier, tier_priority, url_env, auth_bearer_env)
		VALUES ('openrouter-deepseek', 'llm', 1, 20, 'UPSTREAM_ERRADO_URL', 'UPSTREAM_LLM_OPENROUTER_AUTH_BEARER')`); err != nil {
		t.Fatalf("prep openrouter-deepseek divergente: %v", err)
	}
	assertUpFails(t, ctx, pool, "definicao divergente")
}
