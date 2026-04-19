//go:build integration

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
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

// TestMain brings up one shared Postgres + one shared Redis for the
// whole integration_test package. Tests rebuild a FRESH schema between
// cases via db.Down + db.Up (cheap) rather than tearing containers down.
func TestMain(m *testing.M) {
	setupOnce.Do(func() { setupErr = setupContainers(context.Background()) })
	if setupErr != nil {
		fmt.Fprintf(os.Stderr, "integration setup failed: %v\n", setupErr)
		os.Exit(1)
	}
	code := m.Run()
	// Best-effort container teardown — reapers also collect them.
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

// testConfig returns a Config pointing at the shared containers with
// the required upstream URLs pre-filled to a dummy (tests that need a
// real upstream override these fields per-test).
func testConfig() config.Config {
	cfg, err := loadBaseConfig()
	if err != nil {
		panic(err)
	}
	cfg.PGDSN = sharedPGDSN
	cfg.RedisAddr = sharedRedisAddr
	cfg.UpstreamLLMURL = "http://dummy-upstream"
	cfg.UpstreamSTTURL = "http://dummy-upstream"
	cfg.UpstreamEmbedURL = "http://dummy-upstream"
	cfg.UpstreamHealthBridgeURL = "http://dummy-upstream"
	cfg.Env = "development"
	return cfg
}

// loadBaseConfig sets required env vars to dummies just so config.Load
// doesn't fail-fast. Tests then override fields directly.
func loadBaseConfig() (config.Config, error) {
	for k, v := range map[string]string{
		"AI_GATEWAY_PG_DSN":          "postgres://x",
		"AI_GATEWAY_REDIS_ADDR":      "x",
		"UPSTREAM_LLM_URL":           "http://x",
		"UPSTREAM_STT_URL":           "http://x",
		"UPSTREAM_EMBED_URL":         "http://x",
		"UPSTREAM_HEALTH_BRIDGE_URL": "http://x",
	} {
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
	return config.Load()
}

// freshSchema applies db.Up then TRUNCATES all user tables so tests
// start from a known seed state (tenants='converseai', 3 model_aliases).
// Returns a pool + redis client whose lifecycle is managed by t.Cleanup.
func freshSchema(t *testing.T, ctx context.Context) (*pgxpool.Pool, *redis.Client) {
	t.Helper()
	cfg := testConfig()
	pool, err := db.NewPool(ctx, cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	// Force fresh connections so pool AfterConnect runs LoadType against
	// the now-existing ai_gateway.api_key_status + ai_gateway.data_class
	// ENUM types. Without this, the first pool connection pre-dates the
	// migrations and sqlc scans into `interface{}` fields fail with
	// "cannot scan unknown type (OID ...) in text format into *interface {}".
	pool.Reset()

	// Truncate data tables (leave schema + partitions). CASCADE covers
	// the child partitions automatically. Tenants are TRUNCATE'd too so
	// cross-test contamination (prior tests seeding "leak-tenant",
	// "concurrent-tenant", etc.) doesn't bleed into tenant-count assertions.
	for _, tbl := range []string{"api_keys", "audit_log", "audit_log_content", "usage_counters", "tenants"} {
		if _, err := pool.Exec(ctx, "TRUNCATE ai_gateway."+tbl+" CASCADE"); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	// Re-seed the default converseai tenant that the 0001 migration created.
	if _, err := pool.Exec(ctx,
		`INSERT INTO ai_gateway.tenants (slug,name) VALUES ('converseai','ConverseAI') ON CONFLICT (slug) DO NOTHING`); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	// model_aliases seed is idempotent — migration inserted it, truncate
	// did not touch it (not in truncate list).

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

// discardLogger returns a slog.Logger that emits to stderr at Error level
// only — useful to silence warnings during integration tests.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// seedTenant inserts a tenant with a given slug + data_class and returns
// the UUID. Used by tests that need a dedicated tenant for assertions.
func seedTenant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, slug, _ string) uuid.UUID {
	t.Helper()
	q := gen.New(pool)
	// Try to fetch first — tenant may already exist from freshSchema seed.
	if existing, err := q.GetTenantBySlug(ctx, slug); err == nil {
		return existing.ID
	}
	row, err := q.CreateTenant(ctx, gen.CreateTenantParams{Slug: slug, Name: slug})
	if err != nil {
		t.Fatalf("seed tenant %s: %v", slug, err)
	}
	return row.ID
}

// seedTenantAndKey creates a tenant (if missing) + a fresh API key for
// that tenant with the given data_class. Returns (tenantID, apiKeyID,
// rawKey) so the test can use the raw key in HTTP headers.
func seedTenantAndKey(t *testing.T, ctx context.Context, pool *pgxpool.Pool, slug string, dc auth.DataClass) (uuid.UUID, uuid.UUID, string) {
	t.Helper()
	tenantID := seedTenant(t, ctx, pool, slug, string(dc))
	q := gen.New(pool)
	raw, hash, lookupHash, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	inserted, err := q.InsertAPIKey(ctx, gen.InsertAPIKeyParams{
		TenantID:      tenantID,
		KeyHash:       hash,
		KeyLookupHash: lookupHash,
		KeyPrefix:     prefix,
		DataClass:     string(dc),
	})
	if err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	return tenantID, inserted.ID, raw
}

// repoRoot walks up from the test binary to the module root (where go.mod
// lives). Integration_test lives at <root>/gateway/internal/integration_test.
func repoRoot(t *testing.T) string {
	t.Helper()
	// runtime.Caller returns this file's path which is deterministic even
	// when `go test -C` is used.
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// file = <root>/gateway/internal/integration_test/setup_test.go
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
