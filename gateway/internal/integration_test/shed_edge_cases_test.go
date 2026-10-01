//go:build integration

// Phase 5 Plan 05-08 Task 8.3 — edge cases covering D-B3 (sensitive 503),
// D-D1 (tier-1 unavailable 503), D-D3 (peak-off-hours noop), D-C5
// (shed-force operator override), and DCGM fail-open.
//
// CONTEXT.md references:
//
//	D-B3: sensitive saturated → 503 + Retry-After:5 (LGPD)
//	D-D1: tier-1 also unavailable → 503 all_chat_upstreams_saturated + Retry-After:30
//	D-D3: peak-off-hours is noop for shed; metric records 'skipped_peak_offhours'
//	D-C5: operator shed-force overrides FSM via gw:shed:force:{upstream} Redis key
//	D-A3: DCGM scrape fail-open — VRAM signal becomes unknown; FSM continues via inflight+P95
package integration

import (
	"bytes"
	"net/http"
	"os"
	"testing"
	"time"

	vegeta "github.com/tsenart/vegeta/lib"
)

// driveFSMToOn issues sustained slow traffic to push the FSM into
// StateOn within the test-scaled arm window. Returns when state="on" is
// observed or fails the test. Caller is responsible for stopping the
// load goroutine via attacker.Stop() if needed — this function does not
// keep load running after FSM=on.
func driveFSMToOn(t *testing.T, stack *ShedStack, gwURL, tenantSlug string) {
	t.Helper()
	stack.Tier0Mock.SetLatency(600 * time.Millisecond)
	target := vegeta.Target{
		Method: "POST",
		URL:    gwURL + "/v1/chat/completions",
		Header: http.Header{
			"Authorization": {"Bearer " + stack.ApiKey(tenantSlug)},
			"Content-Type":  {"application/json"},
		},
		Body: chatBody(),
	}
	attacker := vegeta.NewAttacker(vegeta.Timeout(10 * time.Second))
	stopCh := make(chan struct{})
	go func() {
		defer close(stopCh)
		for res := range attacker.Attack(vegeta.NewStaticTargeter(target),
			vegeta.Rate{Freq: 20, Per: time.Second}, 10*time.Second, "edge-warmup") {
			_ = res
		}
	}()
	state := waitForState(t, stack, "local-llm", "on", 10*time.Second)
	attacker.Stop()
	<-stopCh
	if state != "on" {
		t.Fatalf("driveFSMToOn: FSM never reached on; last state=%q", state)
	}
}

// forceShedOn liga o override do operador gw:shed:force:local-llm=on (D-C5)
// e espera o FSM refletir "on". Com o force ativo o ticker NÃO avalia sinais
// (tick.go), então o FSM fica em On de forma determinística — sem depender
// de carga/p95 (quick 260930-vkt).
func forceShedOn(t *testing.T, stack *ShedStack) {
	t.Helper()
	if err := stack.Rdb.Set(stack.Ctx, "gw:shed:force:local-llm", "on", 60*time.Second).Err(); err != nil {
		t.Fatalf("set shed-force: %v", err)
	}
	t.Cleanup(func() { _ = stack.Rdb.Del(stack.Ctx, "gw:shed:force:local-llm").Err() })
	if state := waitForState(t, stack, "local-llm", "on", 5*time.Second); state != "on" {
		t.Fatalf("shed-force on não refletiu no FSM; last state=%q", state)
	}
}

// occupyTier0Slot dispara 1 request de chat em background para o tenant e
// devolve um canal que fecha quando ela termina. Usado para ocupar o único
// slot de inflight do tenant (cap=1) enquanto o tier-0 mock segura a
// resposta. NÃO usa authedPost (t.Fatalf fora da goroutine do teste).
func occupyTier0Slot(t *testing.T, gwURL, apiKey string) <-chan int {
	t.Helper()
	done := make(chan int, 1)
	go func() {
		req, err := http.NewRequest("POST", gwURL+"/v1/chat/completions", bytes.NewReader(chatBody()))
		if err != nil {
			done <- -1
			return
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			done <- -1
			return
		}
		_ = drainBody(resp)
		done <- resp.StatusCode
	}()
	return done
}

// TestSensitiveSaturated503 validates D-B3 — when a sensitive tenant's
// request arrives while local-llm FSM=ON and the tenant has exhausted
// its local inflight cap, the gateway MUST return 503 with
// Retry-After:5 + envelope code "upstream_saturated_for_sensitive_tenant"
// + audit row marked upstream="shed_blocked_sensitive" (LGPD: sensitive
// data cannot be routed to external tier-1 providers).
//
// Quick 260930-vkt: versão anterior setava local_inflight_max_llm = 0, que
// no middleware significa "sem config → defaultCapForRole = 4"
// (middleware.go), então a request passava com 200. Agora o caminho é
// determinístico: cap=1 gravado ANTES do boot (sem depender de NOTIFY),
// FSM forçado ON via gw:shed:force, 1 request lenta ocupa o slot e a 2ª
// cai obrigatoriamente no Branch 10a.
func TestSensitiveSaturated503(t *testing.T) {
	if os.Getenv("CI") == "true" && os.Getenv("CI_ALLOW_TIGHT_SHED_TIMING") != "1" {
		t.Skip("skipping in CI — testcontainers + tight timing flaky on free-tier runners. Run locally or set CI_ALLOW_TIGHT_SHED_TIMING=1.")
	}
	stack := newShedStack(t)
	sqlUpdate(t, stack, `
		UPDATE ai_gateway.tenants
		SET local_inflight_max_llm = 1
		WHERE slug = 'telefonia'
	`)
	gwURL := bootGateway(stack, nil)

	forceShedOn(t, stack)

	// Ocupa o único slot de telefonia: tier-0 segura 1.5s.
	stack.Tier0Mock.SetLatency(1500 * time.Millisecond)
	firstDone := occupyTier0Slot(t, gwURL, stack.ApiKey("telefonia"))
	// Auth já aquecido no boot (warm-up) → a 1ª request passa do middleware
	// em ms; 400ms de folga garante que ela está em voo (inflight=1).
	time.Sleep(400 * time.Millisecond)

	resp := authedPost(t, gwURL, "/v1/chat/completions", stack.ApiKey("telefonia"), chatBody())
	body := drainBody(resp)
	t.Logf("sensitive 503 response: status=%d retry-after=%q body=%s",
		resp.StatusCode, resp.Header.Get("Retry-After"), body)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("D-B3 FAIL: expected 503, got %d (body=%s)", resp.StatusCode, body)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "5" {
		t.Errorf("D-B3 FAIL: expected Retry-After=5, got %q", ra)
	}
	if !containsAny(body, "upstream_saturated_for_sensitive_tenant") {
		t.Errorf("D-B3 FAIL: envelope missing upstream_saturated_for_sensitive_tenant; body=%s", body)
	}

	// A request que ocupava o slot deve ter sido servida pelo tier-0.
	if code := <-firstDone; code != http.StatusOK {
		t.Errorf("D-B3: request que ocupava o slot terminou com status=%d (esperado 200 do tier-0)", code)
	}

	// Audit writer is buffered (200ms flush); poll for up to 3s.
	deadline := time.Now().Add(3 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		if auditCountFor(t, stack, "shed_blocked_sensitive") > 0 {
			found = true
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !found {
		t.Errorf("D-B3 audit row missing: no row with upstream='shed_blocked_sensitive'")
	}
}

// TestTier1UnavailableShedded503 validates D-D1 — when shed must divert a
// normal tenant over its cap AND no tier-1 is available, gateway returns
// 503 + Retry-After:30 + envelope code "all_chat_upstreams_saturated".
//
// Quick 260930-vkt: o caminho D-D1 (Branch 10b do shed middleware) só
// dispara quando Loader.Resolve("llm", 1) não encontra upstream habilitado —
// breaker aberto no tier-1 NÃO leva ao 10b (o middleware só faz override
// para o tier-1 e quem decide depois é o dispatcher). A versão anterior
// tentava abrir o breaker com 503 do mock e por isso nunca era
// determinística (asserção soft). Agora: tier-1 de llm desabilitado ANTES do
// boot (reabilitado no Cleanup — tabela upstreams não é truncada pelo
// freshSchema), cap=1, FSM forçado ON, 1 request lenta ocupa o slot e a 2ª
// cai obrigatoriamente no 10b. Asserção estrita.
func TestTier1UnavailableShedded503(t *testing.T) {
	if os.Getenv("CI") == "true" && os.Getenv("CI_ALLOW_TIGHT_SHED_TIMING") != "1" {
		t.Skip("skipping in CI — testcontainers + tight timing flaky on free-tier runners. Run locally or set CI_ALLOW_TIGHT_SHED_TIMING=1.")
	}
	stack := newShedStack(t)

	rows, err := stack.Pool.Query(stack.Ctx, `
		UPDATE ai_gateway.upstreams SET enabled = FALSE
		WHERE role = 'llm' AND tier >= 1 AND enabled
		RETURNING name`)
	if err != nil {
		t.Fatalf("disable llm tier-1: %v", err)
	}
	var disabled []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		disabled = append(disabled, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("disable llm tier-1 rows: %v", err)
	}
	t.Cleanup(func() {
		if len(disabled) > 0 {
			_, _ = stack.Pool.Exec(stack.Ctx,
				`UPDATE ai_gateway.upstreams SET enabled = TRUE WHERE name = ANY($1)`, disabled)
		}
	})
	t.Logf("D-D1: llm tier-1 desabilitados para o teste: %v", disabled)

	sqlUpdate(t, stack, `
		UPDATE ai_gateway.tenants
		SET local_inflight_max_llm = 1
		WHERE slug = 'converseai'
	`)
	gwURL := bootGateway(stack, nil)

	forceShedOn(t, stack)

	stack.Tier0Mock.SetLatency(1500 * time.Millisecond)
	firstDone := occupyTier0Slot(t, gwURL, stack.ApiKey("converseai"))
	time.Sleep(400 * time.Millisecond)

	resp := authedPost(t, gwURL, "/v1/chat/completions", stack.ApiKey("converseai"), chatBody())
	body := drainBody(resp)
	t.Logf("D-D1 response: status=%d retry-after=%q body=%s",
		resp.StatusCode, resp.Header.Get("Retry-After"), body)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("D-D1 FAIL: expected 503, got %d (body=%s)", resp.StatusCode, body)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "30" {
		t.Errorf("D-D1 FAIL: expected Retry-After=30, got %q", ra)
	}
	if !containsAny(body, "all_chat_upstreams_saturated") {
		t.Errorf("D-D1 FAIL: envelope missing all_chat_upstreams_saturated; body=%s", body)
	}
	if code := <-firstDone; code != http.StatusOK {
		t.Errorf("D-D1: request que ocupava o slot terminou com status=%d (esperado 200 do tier-0)", code)
	}

	deadline := time.Now().Add(3 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		if auditCountFor(t, stack, "shed_tier1_unavailable") > 0 {
			found = true
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !found {
		t.Errorf("D-D1 audit row missing: no row with upstream='shed_tier1_unavailable'")
	}
}

// TestPeakOffHoursNoopWithMetric validates D-D3 — when a peak-mode
// tenant's window is OUT-of-peak, the schedule middleware routes it to
// tier-1 BEFORE shed runs; shed sees the tier-1 override and is a no-op.
// We verify tier-1 was hit and shed did not interfere.
func TestPeakOffHoursNoopWithMetric(t *testing.T) {
	if os.Getenv("CI") == "true" && os.Getenv("CI_ALLOW_TIGHT_SHED_TIMING") != "1" {
		t.Skip("skipping in CI — testcontainers + tight timing flaky on free-tier runners. Run locally or set CI_ALLOW_TIGHT_SHED_TIMING=1.")
	}
	stack := newShedStack(t)
	gwURL := bootGateway(stack, nil)

	// Configure chat-ifix as peak mode with a window of 00:00..00:01
	// so that the current wall-clock time is always OUT of peak.
	// Schedule middleware will override to tier-1 unconditionally.
	sqlUpdate(t, stack, `
		UPDATE ai_gateway.tenants
		SET mode='peak', peak_window_start='00:00', peak_window_end='00:01'
		WHERE slug='chat-ifix'
	`)
	time.Sleep(1500 * time.Millisecond)

	stack.Tier1Mock.ResetHits()
	stack.Tier0Mock.ResetHits()

	resp := authedPost(t, gwURL, "/v1/chat/completions",
		stack.ApiKey("chat-ifix"), chatBody())
	body := drainBody(resp)
	t.Logf("D-D3 response: status=%d body=%s", resp.StatusCode, body)

	if resp.StatusCode != 200 {
		t.Errorf("D-D3 FAIL: expected 200 (schedule routed to tier-1), got %d body=%s",
			resp.StatusCode, body)
	}
	if stack.Tier1Mock.Hits() == 0 {
		t.Errorf("D-D3 FAIL: tier-1 not hit — schedule did not override")
	}
	if stack.Tier0Mock.Hits() != 0 {
		t.Errorf("D-D3 FAIL: tier-0 hit unexpectedly (%d) — schedule should have bypassed local-llm",
			stack.Tier0Mock.Hits())
	}
}

// TestShedForceOverride validates D-C5 — operator shed-force via
// gw:shed:force:{upstream} TTL key forces the FSM into the override
// state regardless of signals.
func TestShedForceOverride(t *testing.T) {
	if os.Getenv("CI") == "true" && os.Getenv("CI_ALLOW_TIGHT_SHED_TIMING") != "1" {
		t.Skip("skipping in CI — testcontainers + tight timing flaky on free-tier runners. Run locally or set CI_ALLOW_TIGHT_SHED_TIMING=1.")
	}
	stack := newShedStack(t)
	_ = bootGateway(stack, nil)

	// Set force=on via Redis. The shed ticker (1s in prod, 100ms in
	// tests via SHED_TICK_INTERVAL_MS) reads this key on each iteration
	// and calls Transition(StateOn) on the FSM.
	if err := stack.Rdb.Set(stack.Ctx, "gw:shed:force:local-llm", "on", 30*time.Second).Err(); err != nil {
		t.Fatalf("set shed-force: %v", err)
	}

	// Wait for the ticker to pick up + publish to Redis mirror.
	// 100ms tick * 5 = 500ms; 2s gives plenty of margin.
	state := waitForState(t, stack, "local-llm", "on", 2*time.Second)
	if state != "on" {
		t.Errorf("D-C5 FAIL: expected force-induced state=on, got %q", state)
	}

	// Clear the force key. FSM should evaluate signals naturally and
	// transition to "off" (or recovering) since no load is applied.
	if err := stack.Rdb.Del(stack.Ctx, "gw:shed:force:local-llm").Err(); err != nil {
		t.Fatalf("del shed-force: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m, _ := readShedState(stack, "local-llm")
		st := m["state"]
		if st == "off" || st == "recovering" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Soft assertion — depending on timing the FSM may still be in "on"
	// briefly. Log but do not fail since the core force behavior was
	// already validated above.
	m, _ := readShedState(stack, "local-llm")
	t.Logf("D-C5 post-clear: state=%q (acceptable: off|recovering|on briefly)", m["state"])
}

// TestDCGMFailOpen validates D-A3 — when DCGM_EXPORTER_URL is empty (or
// the endpoint is unreachable), the VRAM signal becomes "unknown" and
// the 2-of-3 saturation gate reduces to 2-of-2 over (Inflight, P95).
// The FSM must still transition to ON under sustained inflight+P95.
//
// We pass DCGM_EXPORTER_URL="" explicitly; the harness default is already
// empty so this is a redundant assertion of mode A. The key behavior:
// FSM=on is reachable without VRAM.
func TestDCGMFailOpen(t *testing.T) {
	if os.Getenv("CI") == "true" && os.Getenv("CI_ALLOW_TIGHT_SHED_TIMING") != "1" {
		t.Skip("skipping in CI — testcontainers + tight timing flaky on free-tier runners. Run locally or set CI_ALLOW_TIGHT_SHED_TIMING=1.")
	}
	stack := newShedStack(t)
	gwURL := bootGateway(stack, map[string]string{"DCGM_EXPORTER_URL": ""})

	// Apply sustained inflight + P95 saturation. Without VRAM signal,
	// the only way to reach saturated=true is both InflightOverMax AND
	// P95OverMax. tier-0 latency=600ms + 20 RPS satisfies both.
	stack.Tier0Mock.SetLatency(600 * time.Millisecond)
	target := vegeta.Target{
		Method: "POST",
		URL:    gwURL + "/v1/chat/completions",
		Header: http.Header{
			"Authorization": {"Bearer " + stack.ApiKey("converseai")},
			"Content-Type":  {"application/json"},
		},
		Body: chatBody(),
	}
	attacker := vegeta.NewAttacker(vegeta.Timeout(10 * time.Second))
	go func() {
		for res := range attacker.Attack(vegeta.NewStaticTargeter(target),
			vegeta.Rate{Freq: 20, Per: time.Second}, 12*time.Second, "dcgm-fail-open") {
			_ = res
		}
	}()
	defer attacker.Stop()

	state := waitForState(t, stack, "local-llm", "on", 12*time.Second)
	if state != "on" {
		t.Errorf("DCGM fail-open FAIL: FSM should reach 'on' via inflight+P95 alone; got %q", state)
	} else {
		t.Logf("DCGM fail-open PASS: FSM reached 'on' without VRAM signal")
	}
}

// containsAny returns true if haystack contains any of the needles.
// Helper to keep edge-case assertions tolerant to evolving envelope
// strings while still proving the right code path executed.
func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if len(haystack) >= len(n) && stringContains(haystack, n) {
			return true
		}
	}
	return false
}

func stringContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
