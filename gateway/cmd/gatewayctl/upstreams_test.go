//go:build integration

// Integration tests for `gatewayctl upstreams` subcommands. Brings up a
// dedicated Postgres + Redis testcontainer pair for the gatewayctl test
// binary (TestMain) — the integration_test package's TestMain is
// per-package so we cannot share it cross-binary.
//
// Each test calls runUpstreams directly (in-process) so we can capture
// stdout/stderr and assert on the wire format without exec subprocess
// overhead. The CLI under test reads its DSN from os.Setenv, so we
// override AI_GATEWAY_PG_DSN + AI_GATEWAY_REDIS_ADDR per-test.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

var (
	sharedPG        *postgres.PostgresContainer
	sharedPGDSN     string
	sharedRedis     testcontainers.Container
	sharedRedisAddr string
	setupOnce       sync.Once
	setupErr        error
)

func TestMain(m *testing.M) {
	setupOnce.Do(func() { setupErr = setupContainers(context.Background()) })
	if setupErr != nil {
		fmt.Fprintf(os.Stderr, "gatewayctl integration setup failed: %v\n", setupErr)
		os.Exit(1)
	}
	code := m.Run()
	if sharedPG != nil {
		_ = sharedPG.Terminate(context.Background())
	}
	if sharedRedis != nil {
		_ = sharedRedis.Terminate(context.Background())
	}
	os.Exit(code)
}

func setupContainers(ctx context.Context) error {
	pg, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("gateway_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		return fmt.Errorf("postgres container: %w", err)
	}
	sharedPG = pg
	sharedPGDSN, err = pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return fmt.Errorf("dsn: %w", err)
	}

	redisReq := testcontainers.ContainerRequest{
		Image:        "redis:7-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForLog("Ready to accept connections").WithStartupTimeout(30 * time.Second),
	}
	rc, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: redisReq, Started: true,
	})
	if err != nil {
		return fmt.Errorf("redis container: %w", err)
	}
	sharedRedis = rc
	host, err := rc.Host(ctx)
	if err != nil {
		return err
	}
	port, err := rc.MappedPort(ctx, "6379/tcp")
	if err != nil {
		return err
	}
	sharedRedisAddr = fmt.Sprintf("%s:%s", host, port.Port())
	return nil
}

// freshSchema applies migrations + truncates user tables + re-enables the
// upstream seed rows. Mirrors the integration_test/setup_test.go helper of
// the same name (the gatewayctl test binary needs its own copy because
// TestMain is per-package).
func freshSchema(t *testing.T, ctx context.Context) (*pgxpool.Pool, *redis.Client) {
	t.Helper()
	// Set required env vars so config.Load + db.NewPool succeed.
	t.Setenv("AI_GATEWAY_PG_DSN", sharedPGDSN)
	t.Setenv("AI_GATEWAY_REDIS_ADDR", sharedRedisAddr)
	t.Setenv("UPSTREAM_LLM_URL", "http://x")
	t.Setenv("UPSTREAM_STT_URL", "http://x")
	t.Setenv("UPSTREAM_EMBED_URL", "http://x")
	t.Setenv("UPSTREAM_HEALTH_BRIDGE_URL", "http://x")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	pool, err := db.NewPool(ctx, cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool.Reset()

	for _, tbl := range []string{"api_keys", "audit_log", "audit_log_content", "usage_counters", "tenants"} {
		if _, err := pool.Exec(ctx, "TRUNCATE ai_gateway."+tbl+" CASCADE"); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO ai_gateway.tenants (slug,name) VALUES ('converseai','ConverseAI') ON CONFLICT (slug) DO NOTHING`); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	// Reset upstream rows the way integration_test/upstreams_loader_test.go's
	// resetUpstreamsTable does — freshSchema's TRUNCATE list deliberately
	// omits ai_gateway.upstreams (the 0008 seed migration is INSERT… ON
	// CONFLICT DO NOTHING and would not re-enable rows previously disabled
	// by another test in the same process).
	if _, err := pool.Exec(ctx, `UPDATE ai_gateway.upstreams
        SET enabled = TRUE,
            tier = CASE name
                WHEN 'local-llm' THEN 0
                WHEN 'openrouter-chat' THEN 1
                WHEN 'local-stt' THEN 0
                WHEN 'openai-whisper' THEN 1
                WHEN 'local-embed' THEN 0
                WHEN 'openai-embed' THEN 1
                ELSE tier
            END,
            circuit_config = '{}'::jsonb,
            last_probe_at = NULL,
            last_probe_ms = NULL,
            last_probe_status = NULL,
            last_probe_error = NULL`); err != nil {
		t.Fatalf("reset upstreams: %v", err)
	}

	rdb, err := redisx.NewClient(ctx, cfg)
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("redis flush: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_ = rdb.Close()
	})
	return pool, rdb
}

// runCLI calls runUpstreams in-process with stdout/stderr captured via
// os.Pipe. Returns (stdout, stderr, exitCode).
func runCLI(t *testing.T, args []string) (stdout, stderr string, code int) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	doneOut := make(chan string, 1)
	doneErr := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, rOut)
		doneOut <- b.String()
	}()
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, rErr)
		doneErr <- b.String()
	}()

	code = runUpstreams(ctx, args, log)
	_ = wOut.Close()
	_ = wErr.Close()
	stdout = <-doneOut
	stderr = <-doneErr

	os.Stdout = oldOut
	os.Stderr = oldErr
	return stdout, stderr, code
}

// TestRunUpstreams_List_PrintsSeedRows asserts the `list` subcommand
// prints all 6 seeded upstream rows + the header.
func TestRunUpstreams_List_PrintsSeedRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = freshSchema(t, ctx)

	stdout, stderr, code := runCLI(t, []string{"list"})
	if code != 0 {
		t.Fatalf("code = %d; stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "NAME") || !strings.Contains(stdout, "ROLE") || !strings.Contains(stdout, "TIER") {
		t.Errorf("list output missing header columns:\n%s", stdout)
	}
	for _, n := range []string{"local-llm", "openrouter-chat", "local-stt", "openai-whisper", "local-embed", "openai-embed"} {
		if !strings.Contains(stdout, n) {
			t.Errorf("list output missing upstream %q\nstdout:\n%s", n, stdout)
		}
	}
	// AUTH_BEARER_ENV column should be populated for the 3 external rows.
	if !strings.Contains(stdout, "UPSTREAM_LLM_OPENROUTER_AUTH_BEARER") {
		t.Errorf("AUTH_BEARER_ENV column missing OpenRouter env name:\n%s", stdout)
	}
}

// TestRunUpstreams_Update_EnabledFalse verifies the `update --enabled=false`
// path mutates the row in the database (the live verification of the
// NOTIFY-triggered hot-reload pipeline lives in the integration tests
// of Task 2).
func TestRunUpstreams_Update_EnabledFalse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	_, stderr, code := runCLI(t, []string{"update", "--name=openrouter-chat", "--enabled=false"})
	if code != 0 {
		t.Fatalf("code = %d; stderr=%q", code, stderr)
	}

	var enabled bool
	if err := pool.QueryRow(ctx,
		"SELECT enabled FROM ai_gateway.upstreams WHERE name='openrouter-chat'").Scan(&enabled); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if enabled {
		t.Fatal("openrouter-chat should be disabled after update")
	}
}

// TestRunUpstreams_Update_NameRequired exercises the missing --name flag
// path: stderr must mention --name and exit code must be 2 (usage error).
func TestRunUpstreams_Update_NameRequired(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = freshSchema(t, ctx)

	_, stderr, code := runCLI(t, []string{"update", "--enabled=false"})
	if code != 2 {
		t.Errorf("want exit 2 (usage error), got %d", code)
	}
	if !strings.Contains(stderr, "--name required") {
		t.Errorf("stderr = %q, want error about --name", stderr)
	}
}

// TestRunUpstreams_Disable_Enable_Roundtrip exercises both shortcut
// subcommands and verifies the row state via direct SQL.
func TestRunUpstreams_Disable_Enable_Roundtrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	if _, _, code := runCLI(t, []string{"disable", "--name=local-llm"}); code != 0 {
		t.Fatalf("disable exit = %d", code)
	}
	var enabled bool
	if err := pool.QueryRow(ctx,
		"SELECT enabled FROM ai_gateway.upstreams WHERE name='local-llm'").Scan(&enabled); err != nil {
		t.Fatalf("scan disabled: %v", err)
	}
	if enabled {
		t.Fatal("local-llm should be disabled after `disable` subcommand")
	}

	if _, _, code := runCLI(t, []string{"enable", "--name=local-llm"}); code != 0 {
		t.Fatalf("enable exit = %d", code)
	}
	if err := pool.QueryRow(ctx,
		"SELECT enabled FROM ai_gateway.upstreams WHERE name='local-llm'").Scan(&enabled); err != nil {
		t.Fatalf("scan re-enabled: %v", err)
	}
	if !enabled {
		t.Fatal("local-llm should be re-enabled after `enable` subcommand")
	}
}

// TestRunUpstreams_Update_UnknownName surfaces a clear error for a typo'd
// upstream name (otherwise the UPDATE would be a silent no-op).
func TestRunUpstreams_Update_UnknownName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = freshSchema(t, ctx)

	_, stderr, code := runCLI(t, []string{"update", "--name=does-not-exist", "--enabled=true"})
	if code != 1 {
		t.Errorf("want exit 1 (lookup failure), got %d", code)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("stderr = %q, want 'not found'", stderr)
	}
}
