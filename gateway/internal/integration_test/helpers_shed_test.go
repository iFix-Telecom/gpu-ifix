//go:build integration

// Package integration (helpers_shed_test.go) — Phase 5 Plan 05-08 Task 8.1
// shared harness for the SC-1..SC-4 + edge-case + mirror-convergence
// integration suites.
//
// Why subprocess + env wiring instead of in-process gateway.Run:
//
// Plan 05-08 originally envisioned a "Run(ctx, cfg, hooks)" hook in
// gateway/cmd/gateway/main.go (Plan 06 Task 6.4) so SC tests could boot
// the gateway in-process. That refactor did NOT land in the Phase 5 wave
// — main.go still ships a single func main() entrypoint with all wiring
// inline. Rather than block this wave on a cmd/gateway refactor that
// belongs to Plan 06 (and would conflict with Plan 05-07 running in
// parallel on gatewayctl/), we adopt the SAME pattern that
// gateway_e2e_test.go used since Phase 3: build ./gateway/cmd/gateway
// into a temp binary, exec it with env vars pointing at the
// testcontainer Postgres + Redis, and probe /health to wait for boot.
//
// This is "Option B" from the plan but using subprocess instead of a
// fake router — it exercises the REAL middleware chain, real shed
// goroutines (ticker + scraper + subscribe + reconcile), and real
// dispatcher; only the inspection surface is HTTP + Redis HGETALL rather
// than in-process pointer access to *shed.Set. SC-1..SC-4 + edge cases
// can all be expressed through this surface; the mirror-convergence test
// uses two in-process shed.Set instances against the same Redis (no
// gateway involved) so it does not need bootGateway at all.
//
// Concretely the bootGateway helper:
//  1. builds the gateway binary into t.TempDir() (cached across tests
//     via package-level once.Do — pays the ~3s build cost once)
//  2. picks a free port via net.Listen("tcp", "127.0.0.1:0").Close()
//  3. starts the binary with env vars overriding tier-0/tier-1 URLs to
//     point at the test ControlledMockServer instances + DCGM URL + the
//     Phase 5 SHED_TICK_INTERVAL_MS=100 (10x faster than prod for fast
//     convergence in tests) + AI_GATEWAY_MIGRATE_ON_BOOT=true so the
//     gateway applies migrations 0001..0017 against the testcontainer DB
//  4. polls /health for up to 30s until the gateway is ready
//  5. registers t.Cleanup to SIGTERM the subprocess + drain stdout/stderr
//
// Test-scaled shed thresholds: the SC tests need fast FSM convergence so
// 30s arm + 60s recover (prod defaults) would balloon SC-1 runtime to
// 120s+. seedShedThresholds writes arm=1s/recover=2s into local-llm's
// circuit_config JSONB BEFORE bootGateway runs, so the FSM converges in
// ~3-5s after load stops.
package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// ShedStack bundles every primitive the SC + edge + mirror suites need.
// Fields named ShedKey* are the seeded API keys used in HTTP Authorization
// headers (TEST_*_KEY are raw values; the bcrypt/argon2 hashes live in
// ai_gateway.api_keys via seedTenantAndKey).
type ShedStack struct {
	T   *testing.T
	Ctx context.Context

	Pool *pgxpool.Pool
	Rdb  *redis.Client

	// Mock upstreams — Tier0Mock is the local-llm target (slow / saturated
	// in SC tests), Tier1Mock is openrouter-chat (fast fallback). Both
	// implement OpenAI-compatible JSON skeletons so the dispatcher's
	// reverse proxy + audit/billing middleware do not break on parse.
	Tier0Mock *ControlledMockServer
	Tier1Mock *ControlledMockServer
	// DCGMMock is an optional mock returning Prometheus text format with
	// DCGM_FI_DEV_FB_USED. Tests that want to exercise the VRAM signal
	// set it via newShedStackWithDCGM; tests that disable VRAM (DCGM
	// fail-open path) leave it nil and pass DCGMExporterURL="".
	DCGMMock *httptest.Server

	// GatewayURL is "http://127.0.0.1:<port>" populated by bootGateway.
	GatewayURL string
	gatewayCmd *exec.Cmd
	gatewayBin string

	// Seeded tenant IDs / API keys. Tests reference these via
	// stack.ApiKey("converseai") to keep request building terse.
	tenants map[string]uuid.UUID
	apiKeys map[string]string // slug -> raw API key
}

// ControlledMockServer simulates an upstream with thread-safe controls
// over latency (via atomic.Int64 nanoseconds), HTTP status code (via
// atomic.Int32), and a hit counter. All knobs are safe to mutate from
// any test goroutine while load generators are firing.
//
// Body shape is OpenAI chat.completions for 2xx and OpenAI error envelope
// for 4xx/5xx so the dispatcher's reverse-proxy + billing flush
// (which parses usage tokens) do not panic on unexpected JSON.
type ControlledMockServer struct {
	srv        *httptest.Server
	latency    atomic.Int64 // nanoseconds; 0 means no sleep
	statusCode atomic.Int32 // default 200
	hits       atomic.Int64
}

func newControlledMock(t *testing.T) *ControlledMockServer {
	t.Helper()
	m := &ControlledMockServer{}
	m.statusCode.Store(200)
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		m.hits.Add(1)
		if lat := m.latency.Load(); lat > 0 {
			time.Sleep(time.Duration(lat))
		}
		code := int(m.statusCode.Load())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if code < 400 {
			_, _ = fmt.Fprintf(w, `{"id":"mock-%d","object":"chat.completion","created":1,"model":"qwen","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, m.hits.Load())
		} else {
			_, _ = fmt.Fprintf(w, `{"error":{"message":"mock-%d","type":"mock","code":"mock_error"}}`, code)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// URL returns the mock's base URL (e.g. http://127.0.0.1:12345).
func (m *ControlledMockServer) URL() string { return m.srv.URL }

// SetLatency injects a per-request sleep on the next call. 0 disables
// the sleep. Used by SC-1 to drive inflight up + by SC-2 to oscillate
// the P95 signal.
func (m *ControlledMockServer) SetLatency(d time.Duration) { m.latency.Store(d.Nanoseconds()) }

// SetStatus changes the HTTP status code returned on subsequent calls.
// Used by the tier-1-unavailable edge case (set 503 to trip the breaker).
func (m *ControlledMockServer) SetStatus(code int) { m.statusCode.Store(int32(code)) }

// Hits returns the total request count since the mock was created or
// the last ResetHits call.
func (m *ControlledMockServer) Hits() int64 { return m.hits.Load() }

// ResetHits zeroes the counter. Tests call this between phases (warmup
// vs. measurement) to attribute hits to specific stages.
func (m *ControlledMockServer) ResetHits() { m.hits.Store(0) }

// ----------------------------------------------------------------------
// Gateway binary build — cached across tests via once.Do
// ----------------------------------------------------------------------

var (
	gatewayBinPath string
	gatewayBinErr  error
	gatewayBinOnce sync.Once
)

// buildGatewayBinary compiles ./gateway/cmd/gateway once per test binary
// invocation. The resulting binary is placed in os.TempDir() so all
// integration tests (running serially in a single `go test` binary)
// share it — the ~3s build cost is amortised. The binary is left
// on disk; the OS reaps it at next reboot.
func buildGatewayBinary(t *testing.T) string {
	t.Helper()
	gatewayBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "gateway-shed-")
		if err != nil {
			gatewayBinErr = fmt.Errorf("mkdtemp: %w", err)
			return
		}
		bin := filepath.Join(dir, "gateway")
		cmd := exec.Command(goBinaryPath(), "build", "-tags", "integration", "-o", bin, "./gateway/cmd/gateway")
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			gatewayBinErr = fmt.Errorf("build gateway: %w\n%s", err, out)
			return
		}
		gatewayBinPath = bin
	})
	if gatewayBinErr != nil {
		t.Fatalf("build gateway binary: %v", gatewayBinErr)
	}
	return gatewayBinPath
}

// ----------------------------------------------------------------------
// newShedStack — testcontainer-backed harness + 2 mock upstreams + seeded
// schema + per-test fresh tenant/API-key set.
// ----------------------------------------------------------------------

// newShedStack builds the full Phase 5 integration harness. Caller does
// not need to clean up — t.Cleanup is registered for every resource.
//
// Mode A (default): DCGM is left unset → VRAM signal disabled, 2-of-3
// reduces to 2-of-2 inflight+P95. SC-1, SC-3, SC-4 + the DCGM fail-open
// edge case use this mode.
//
// Mode B (with DCGM): pass useDCGM=true so a httptest server is started
// returning Prometheus text format with DCGM_FI_DEV_FB_USED stable at
// 1024 MiB (well below 21504 threshold). Tests that need to drive VRAM
// up flip the mock handler at runtime via stack.DCGMMock.Config.
func newShedStack(t *testing.T) *ShedStack {
	return newShedStackInternal(t, false)
}

func newShedStackInternal(t *testing.T, useDCGM bool) *ShedStack {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	pool, rdb := freshSchema(t, ctx)

	t0 := newControlledMock(t)
	t1 := newControlledMock(t)

	var dcgm *httptest.Server
	if useDCGM {
		// Static 1024 MiB used — well below 21504 threshold, VRAM signal
		// stays low. Tests that need it high flip the mock handler.
		dcgm = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "# HELP DCGM_FI_DEV_FB_USED VRAM used MiB\n# TYPE DCGM_FI_DEV_FB_USED gauge\nDCGM_FI_DEV_FB_USED{gpu=\"0\"} 1024\n")
		}))
		t.Cleanup(dcgm.Close)
	}

	stack := &ShedStack{
		T:         t,
		Ctx:       ctx,
		Pool:      pool,
		Rdb:       rdb,
		Tier0Mock: t0,
		Tier1Mock: t1,
		DCGMMock:  dcgm,
		tenants:   make(map[string]uuid.UUID),
		apiKeys:   make(map[string]string),
	}

	// Seed default test tenants + keys. The 6 known phase-5 slugs from
	// the seed migration are not present in freshSchema (which only seeds
	// 'converseai'); we use a small set sufficient for the SC tests.
	stack.seedTenantWithKey("converseai", auth.DataClassNormal)
	stack.seedTenantWithKey("campanhas", auth.DataClassNormal)
	stack.seedTenantWithKey("telefonia", auth.DataClassSensitive)
	stack.seedTenantWithKey("chat-ifix", auth.DataClassNormal)

	// Lower the test-scoped shed thresholds so SC tests converge fast.
	// Production seed (migration 0017) sets arm=30s/recover=60s; we drop
	// to 1s/2s so SC-1 + SC-3 finish in seconds, not minutes. Inflight
	// max stays at 4 (low) so a small burst saturates quickly.
	stack.seedShedThresholds()

	return stack
}

// seedTenantWithKey inserts a tenant by slug with the given DataClass
// and creates an API key whose raw value the test can use in HTTP
// headers. Stored in stack.tenants + stack.apiKeys for later lookup.
//
// IMPORTANT: setup_test.go's seedTenantAndKey persists data_class on the
// API key only — not on the tenants row. Phase 5 sensitive-tenant logic
// (D-B3) reads data_class from the TENANT, so we explicitly UPDATE
// ai_gateway.tenants here. This keeps tenants.data_class and
// api_keys.data_class consistent without requiring a setup_test.go
// patch (which the worktree envelope forbids).
func (s *ShedStack) seedTenantWithKey(slug string, dc auth.DataClass) {
	s.T.Helper()
	tenantID, _, raw := seedTenantAndKey(s.T, s.Ctx, s.Pool, slug, dc)
	s.tenants[slug] = tenantID
	s.apiKeys[slug] = raw
	// Sync tenants.data_class so shed.Middleware D-B3 logic reads the
	// expected class. Tenants.data_class is an ai_gateway.data_class ENUM
	// added by migration 0013.
	if _, err := s.Pool.Exec(s.Ctx,
		`UPDATE ai_gateway.tenants SET data_class = $1::ai_gateway.data_class WHERE id = $2`,
		string(dc), tenantID); err != nil {
		s.T.Fatalf("sync tenant data_class for %s: %v", slug, err)
	}
}

// ApiKey returns the raw API key seeded for the given tenant slug.
// Used in vegeta.Target Header maps + http.NewRequest Authorization.
func (s *ShedStack) ApiKey(slug string) string {
	if k, ok := s.apiKeys[slug]; ok {
		return k
	}
	s.T.Fatalf("no seeded API key for tenant %q", slug)
	return ""
}

// TenantID returns the seeded uuid for slug.
func (s *ShedStack) TenantID(slug string) uuid.UUID {
	if id, ok := s.tenants[slug]; ok {
		return id
	}
	s.T.Fatalf("no seeded tenant for slug %q", slug)
	return uuid.Nil
}

// seedShedThresholds writes test-scaled shed thresholds into
// ai_gateway.upstreams.circuit_config JSONB for local-llm. arm=1s,
// recover=2s lets SC-1 / SC-3 converge in single-digit seconds.
// inflight_max=4 ensures even small bursts saturate.
func (s *ShedStack) seedShedThresholds() {
	s.T.Helper()
	if _, err := s.Pool.Exec(s.Ctx, `
		UPDATE ai_gateway.upstreams
		SET circuit_config = circuit_config || jsonb_build_object(
			'shed_inflight_max', 4,
			'shed_p95_ms', 500,
			'shed_vram_used_mib', 21504,
			'shed_arm_seconds', 1,
			'shed_recover_seconds', 2,
			'failures', 3,
			'cooldown_s', 30
		)
		WHERE name = 'local-llm'
	`); err != nil {
		s.T.Fatalf("seed shed thresholds: %v", err)
	}
}

// ----------------------------------------------------------------------
// bootGateway — start the gateway subprocess pointing at the testcontainer
// DB + mock upstreams. Returns the gateway URL when /health responds 200.
// ----------------------------------------------------------------------

// bootGateway compiles + starts ./gateway/cmd/gateway as a subprocess.
// envOverrides allow per-test tweaks (e.g. DCGM_EXPORTER_URL="" to
// disable VRAM signal, or SHED_TICK_INTERVAL_MS=50 to converge faster).
// Returns the base URL ("http://127.0.0.1:PORT") after /health is 200.
func bootGateway(s *ShedStack, envOverrides map[string]string) string {
	s.T.Helper()
	s.gatewayBin = buildGatewayBinary(s.T)

	// Pick a free port (close immediately; subprocess re-binds).
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.T.Fatalf("free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	// Build base env: testcontainer Postgres + Redis + tier-0/tier-1 URLs
	// + Phase 5 fast-tick + (default) disabled DCGM. envOverrides win.
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"AI_GATEWAY_PG_DSN=" + sharedPGDSN,
		"AI_GATEWAY_REDIS_ADDR=" + sharedRedisAddr,
		"UPSTREAM_LLM_URL=" + s.Tier0Mock.URL(),
		"UPSTREAM_STT_URL=" + s.Tier0Mock.URL(),
		"UPSTREAM_EMBED_URL=" + s.Tier0Mock.URL(),
		"UPSTREAM_HEALTH_BRIDGE_URL=" + s.Tier0Mock.URL(),
		"UPSTREAM_LLM_OPENROUTER_URL=" + s.Tier1Mock.URL(),
		"UPSTREAM_LLM_OPENROUTER_AUTH_BEARER=test-bearer",
		"UPSTREAM_STT_OPENAI_URL=" + s.Tier1Mock.URL(),
		"UPSTREAM_STT_OPENAI_AUTH_BEARER=test-bearer",
		"UPSTREAM_EMBED_OPENAI_URL=" + s.Tier1Mock.URL(),
		"UPSTREAM_EMBED_OPENAI_AUTH_BEARER=test-bearer",
		"GATEWAY_PORT=" + strconv.Itoa(port),
		"AI_GATEWAY_MIGRATE_ON_BOOT=false", // schema already applied by freshSchema
		"ENV=development",
		"LOG_LEVEL=warn",
		"SHED_TICK_INTERVAL_MS=100",         // 10x faster than prod for fast tests
		"SHED_DCGM_SCRAPE_INTERVAL_MS=1000", // fast scrape too
		"SHED_DCGM_TIMEOUT_MS=500",
		"DCGM_EXPORTER_URL=", // default disabled; tests opt in via override
	}
	if s.DCGMMock != nil {
		env = append(env, "DCGM_EXPORTER_URL="+s.DCGMMock.URL)
	}
	for k, v := range envOverrides {
		env = append(env, k+"="+v)
	}

	cmd := exec.Command(s.gatewayBin)
	cmd.Env = env
	stdout := &lockingBuffer{}
	stderr := &lockingBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		s.T.Fatalf("start gateway: %v", err)
	}
	s.gatewayCmd = cmd

	// Wait for /health up to 30s. If the subprocess exits prematurely
	// (boot error, DB schema mismatch, etc.) surface the stderr in t.Fatal.
	s.GatewayURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		// If the process exited, fail fast with logs.
		if cmd.ProcessState != nil {
			s.T.Fatalf("gateway exited before /health ready:\nSTDOUT:\n%s\nSTDERR:\n%s",
				stdout.String(), stderr.String())
		}
		resp, err := http.Get(s.GatewayURL + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
	}

	// Final health probe — if still not ready, dump logs and fail.
	resp, err := http.Get(s.GatewayURL + "/health")
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			_ = resp.Body.Close()
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_, _ = cmd.Process.Wait()
		s.T.Fatalf("gateway /health never ready (err=%v):\nSTDOUT:\n%s\nSTDERR:\n%s",
			err, stdout.String(), stderr.String())
	}
	_ = resp.Body.Close()

	// Cleanup: SIGTERM + wait so the goroutines drain gracefully.
	s.T.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_, _ = cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
		if s.T.Failed() {
			s.T.Logf("gateway STDOUT:\n%s", stdout.String())
			s.T.Logf("gateway STDERR:\n%s", stderr.String())
		}
	})

	return s.GatewayURL
}

// lockingBuffer is a thread-safe bytes.Buffer wrapper since
// exec.Cmd.Stdout/Stderr can race with test reads.
type lockingBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockingBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockingBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ----------------------------------------------------------------------
// Inspection helpers — Redis Hash reads + state polling.
// ----------------------------------------------------------------------

// readShedState returns the gw:shed:{upstream} Hash. The empty map
// means "no transition has fired yet" (FSM is still StateOff in-process
// but has not published its first event).
func readShedState(stack *ShedStack, upstream string) (map[string]string, error) {
	return stack.Rdb.HGetAll(stack.Ctx, "gw:shed:"+upstream).Result()
}

// waitForState polls Redis mirror until state == target or timeout
// expires. Returns the last observed state (which may be != target on
// timeout — caller compares).
func waitForState(t *testing.T, stack *ShedStack, upstream, target string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		m, _ := readShedState(stack, upstream)
		last = m["state"]
		if last == target {
			return target
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last
}

// sqlUpdate is a t.Fatal-on-error wrapper around Pool.Exec used by
// SC-3 hot-reload + edge-case D-D3 (peak-off-hours) seeders.
func sqlUpdate(t *testing.T, stack *ShedStack, q string, args ...any) {
	t.Helper()
	if _, err := stack.Pool.Exec(stack.Ctx, q, args...); err != nil {
		t.Fatalf("sql: %v", err)
	}
}

// auditCountFor returns the number of audit_log rows whose upstream
// column matches `marker` (e.g. "shed_blocked_sensitive"). Polled by
// edge-case tests to confirm the dispatcher's auditctx.WithUpstreamOverride
// wire was honored.
func auditCountFor(t *testing.T, stack *ShedStack, marker string) int {
	t.Helper()
	var n int
	err := stack.Pool.QueryRow(stack.Ctx,
		`SELECT COUNT(*) FROM ai_gateway.audit_log WHERE upstream = $1`, marker).Scan(&n)
	if err != nil {
		t.Fatalf("audit count for %q: %v", marker, err)
	}
	return n
}

// drainBody reads + closes the response body (test-side hygiene).
func drainBody(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// chatBody returns a minimal OpenAI chat.completions JSON body that the
// gateway accepts (one user message, deterministic model).
func chatBody() []byte {
	return []byte(`{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`)
}

// authedPost issues POST gwURL+path with Bearer apiKey + body. Returns
// the response (caller drains).
func authedPost(t *testing.T, gwURL, path, apiKey string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", gwURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http do: %v", err)
	}
	return resp
}

// ensure gen import is used (avoids "imported and not used" if all the
// seed helpers above get refactored to seedTenantAndKey via setup_test.go).
var _ = gen.New
