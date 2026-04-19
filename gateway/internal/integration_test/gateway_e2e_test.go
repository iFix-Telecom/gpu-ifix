//go:build integration

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
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// TestIntegration_06_GatewayE2E builds the real ./gateway/cmd/gateway
// binary, points it at our test Postgres+Redis, and sends 3 HTTP calls.
func TestIntegration_06_GatewayE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	// Build the binary into a temp dir.
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "gateway")
	build := exec.Command(goBinaryPath(), "build", "-o", bin, "./gateway/cmd/gateway")
	build.Dir = repoRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build gateway: %v\n%s", err, out)
	}

	// Set up a fake upstream that returns 200 + JSON.
	upstream := startFakeUpstream()
	defer upstream.Close()

	// Find a free port.
	port := freePort(t)

	env := append(os.Environ(),
		"AI_GATEWAY_PG_DSN="+sharedPGDSN,
		"AI_GATEWAY_REDIS_ADDR="+sharedRedisAddr,
		"UPSTREAM_LLM_URL="+upstream.URL,
		"UPSTREAM_STT_URL="+upstream.URL,
		"UPSTREAM_EMBED_URL="+upstream.URL,
		"UPSTREAM_HEALTH_BRIDGE_URL="+upstream.URL,
		"GATEWAY_PORT="+strconv.Itoa(port),
		"AI_GATEWAY_MIGRATE_ON_BOOT=true",
		"ENV=development",
	)
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_, _ = cmd.Process.Wait()
	}()

	// Wait for /health.
	waitForHealth(t, port, 15*time.Second)

	// 1. Unauthenticated /v1/chat/completions → 401 OpenAI envelope.
	resp1, err := http.Post(fmt.Sprintf("http://localhost:%d/v1/chat/completions", port),
		"application/json", bytes.NewBufferString(`{"model":"qwen","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp1.Body.Close()
	if resp1.StatusCode != 401 {
		t.Errorf("unauth got %d want 401", resp1.StatusCode)
	}

	// 2. Authenticated request — issue a key via sqlc directly.
	key := issueAPIKey(t, ctx, pool)
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://localhost:%d/v1/chat/completions", port),
		bytes.NewBufferString(`{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("authed got %d want 200: %s", resp2.StatusCode, b)
	}

	// 3. Health.
	resp3, err := http.Get(fmt.Sprintf("http://localhost:%d/health", port))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Errorf("/health got %d", resp3.StatusCode)
	}

	// 4. Audit row landed (after flush).
	deadline := time.Now().Add(4 * time.Second)
	var auditCount int
	for time.Now().Before(deadline) {
		_ = pool.QueryRow(ctx, "SELECT COUNT(*) FROM ai_gateway.audit_log").Scan(&auditCount)
		if auditCount > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if auditCount == 0 {
		t.Errorf("no audit rows written after E2E request")
	}
}

// ---- helpers ----

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func waitForHealth(t *testing.T, port int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/health", port))
		if err == nil && resp.StatusCode == 200 {
			_ = resp.Body.Close()
			return
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("gateway /health did not become ready")
}

func startFakeUpstream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","created":1,"model":"qwen","choices":[]}`))
	}))
}

func issueAPIKey(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	q := gen.New(pool)
	tenant, err := q.GetTenantBySlug(ctx, "converseai")
	if err != nil {
		t.Fatal(err)
	}
	raw, hash, lookupHash, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertAPIKey(ctx, gen.InsertAPIKeyParams{
		TenantID:      tenant.ID,
		KeyHash:       hash,
		KeyLookupHash: lookupHash,
		KeyPrefix:     prefix,
		DataClass:     string(auth.DataClassNormal),
	}); err != nil {
		t.Fatal(err)
	}
	return raw
}

// goBinaryPath returns the absolute path to the Go toolchain binary used
// to build the subprocess gateway. Respects GOROOT when present (the env
// that invoked `go test` propagates through exec.Command by default, so
// we fall back to plain `go` which finds it on $PATH).
func goBinaryPath() string {
	if gr := os.Getenv("GOROOT"); gr != "" {
		candidate := filepath.Join(gr, "bin", "go")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// On Linux CI / dev machines, `go` is on $PATH. If it isn't, exec will
	// surface a clear error.
	return "go"
}

// silence unused-runtime-import warning if goBinaryPath gets simplified.
var _ = runtime.GOOS
