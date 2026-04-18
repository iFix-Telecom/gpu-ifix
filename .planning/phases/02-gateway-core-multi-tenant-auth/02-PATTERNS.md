# Phase 2: Gateway Core + Multi-tenant Auth — Pattern Map

**Mapped:** 2026-04-18
**Files analyzed:** ~40 new (gateway subtree is greenfield; the only prior Go in-repo is `pod/health-bridge/*` from Phase 1)
**Analogs found:** 28 / 40 (the remaining 12 are novel subsystems — auth/idempotency/audit/reverse-proxy — with no Ifix precedent; use research refs + conceptual neighbors noted below)

> **In-repo style anchor:** `pod/health-bridge/` (Phase 1). Every Go file in `gateway/` mirrors its conventions:
> - `package <name>` comment header with brief purpose + references
> - `slog.Logger.With("module", "UPPER_SNAKE")` seeded at startup, propagated via context
> - Sentinel errors at package level, wrapped with `fmt.Errorf("op: %w", err)`
> - `context.Context` is the first arg of any function that does I/O or can be cancelled
> - Tests colocated (`foo_test.go`), use `httptest.NewServer` for upstreams
>
> **Cross-repo conceptual neighbors** (reuse lessons, not code):
> - `/home/pedro/projetos/pedro/converseai-v4/.github/workflows/deploy-dev.yml` — Portainer webhook trigger pattern (exact reuse in `build-gateway.yml`)
> - `/home/pedro/projetos/pedro/converseai-v4/packages/db/src/migrations/` — numbered SQL migration file naming (`NNNN_description.sql`)
> - `/home/pedro/projetos/pedro/cobrancas-api/src/db/migrations/0000_init.sql` — `CREATE SCHEMA` + typed enums + indexes inline
> - `/home/pedro/projetos/pedro/cobrancas-api/src/modules/invoices/index.ts` — module-as-plugin with `@module` JSDoc (conceptual mirror for chi sub-router composition)
> - `/home/pedro/projetos/pedro/cobrancas-api/src/lib/clickup/errors.ts` — `Error` class hierarchy (mirror as Go sentinels + typed errors)
> - `/home/pedro/projetos/pedro/cobrancas-api/src/lib/logger.ts` — `createLogger('MODULE')` pattern (mirror via `slog.New(...).With("module", "...")`)

---

## File Classification

### Binaries / entrypoints

| Phase 2 file | Role | Data Flow | Closest Analog | Match Quality |
|---|---|---|---|---|
| `gateway/cmd/gateway/main.go` | Go main (HTTP server) | request-response + streaming | `pod/health-bridge/main.go` | exact (in-repo Go server entrypoint) |
| `gateway/cmd/gatewayctl/main.go` | Go main (CLI) | batch / admin | NO ANALOG — new pattern | conceptual: `pod/health-bridge/main.go` flag parsing (`--self-check`) |

### Internal packages (gateway subsystems)

| Phase 2 file | Role | Data Flow | Closest Analog | Match Quality |
|---|---|---|---|---|
| `gateway/internal/config/config.go` | config / env loading | startup | `pod/health-bridge/main.go` lines 18-58 (`loadConfig`, `envOr`, `atoiOr`) | exact |
| `gateway/internal/httpx/requestid.go` | middleware | request-response | NO ANALOG — first middleware in Ifix Go | conceptual: cobrancas-api `src/lib/logger.ts` request-id (`log.child({request_id})`) |
| `gateway/internal/httpx/redact.go` | logging helper | transform | NO ANALOG — first slog redactor in Ifix | conceptual: cobrancas-api `logger.ts` `nowLocal()` simple pure helper |
| `gateway/internal/httpx/envelope.go` | response helper | transform | `pod/health-bridge/handlers.go` lines 10-20 (`writeJSON`) | exact |
| `gateway/internal/auth/apikey.go` | middleware / service | request-response + cache | NO ANALOG — no auth code exists in repo | conceptual: `cobrancas-api/src/lib/clickup/auth.ts` + `pod/health-bridge/probes.go` (HTTP client + context pattern) |
| `gateway/internal/auth/argon2.go` | crypto helper | transform | NO ANALOG | upstream lib docs only (`golang.org/x/crypto/argon2`) |
| `gateway/internal/auth/cache.go` | Redis cache | request-response | NO ANALOG — first Redis consumer in Ifix Go | conceptual: `converseai-v4/apps/api/src/lib/redis.ts` (pattern of prefixed keys) |
| `gateway/internal/audit/writer.go` | async batch writer | event-driven / batch | NO ANALOG — no async queue in Ifix Go | conceptual: `converseai-v4/apps/worker/src/queues/` pattern (but BullMQ, not goroutine) |
| `gateway/internal/audit/tee.go` | streaming body capture | streaming | NO ANALOG — first SSE tee in Ifix | conceptual: `httputil.ReverseProxy.ModifyResponse` docs |
| `gateway/internal/idempotency/store.go` | Redis cache (idempotency) | request-response | NO ANALOG | upstream: Stripe idempotency-key docs |
| `gateway/internal/idempotency/hash.go` | hash helper | transform | NO ANALOG | upstream: `crypto/sha256` stdlib |
| `gateway/internal/proxy/chat.go` | reverse proxy (SSE) | streaming | NO ANALOG — first reverse proxy in Ifix | conceptual: `httputil.ReverseProxy` stdlib docs + `pod/health-bridge/probes.go` HTTP client tuning |
| `gateway/internal/proxy/embeddings.go` | reverse proxy | request-response | (same as chat.go — pattern siblings) | conceptual |
| `gateway/internal/proxy/audio.go` | reverse proxy (multipart) | streaming (upload) | NO ANALOG | conceptual: `pod/health-bridge/probes.go` `probeSTT` multipart pattern |
| `gateway/internal/proxy/errors.go` | error envelope handler | transform | `pkg/openai/types.go` lines 127-136 (`ErrorResponse`) | exact (reuse) |
| `gateway/internal/obs/sentry.go` | Sentry init + BeforeSend | startup + transform | NO ANALOG in gpu-ifix | conceptual: `converseai-v4/apps/api` Sentry init (pattern, not Go) |
| `gateway/internal/obs/metrics.go` | Prometheus scaffold | request-response | NO ANALOG | upstream: `prometheus/client_golang` docs |
| `gateway/internal/db/pool.go` | pgxpool setup | startup | NO ANALOG — first Postgres in Ifix Go | conceptual: `converseai-v4/packages/db/src/index.ts` (Drizzle pool config) |
| `gateway/internal/db/migrate.go` | goose runner | startup / batch | NO ANALOG | upstream: `pressly/goose` docs |
| `gateway/internal/db/gen/*.go` | sqlc generated | — | NO ANALOG — generated code | N/A (sqlc codegen) |

### SQL / migrations

| Phase 2 file | Role | Data Flow | Closest Analog | Match Quality |
|---|---|---|---|---|
| `gateway/db/migrations/0001_create_tenants.sql` | migration | schema | `cobrancas-api/src/db/migrations/0000_init.sql` lines 1-17 (`CREATE SCHEMA` + first table) | exact (style) |
| `gateway/db/migrations/0002_create_api_keys.sql` | migration | schema | `cobrancas-api/src/db/migrations/0000_init.sql` lines 1-40 (table + enum + indexes) | exact (style) |
| `gateway/db/migrations/0003_create_audit_log_partitioned.sql` | migration | schema (partitioned) | NO ANALOG — no partitioned tables in Ifix | upstream: Postgres 16 `PARTITION BY RANGE` docs |
| `gateway/db/migrations/0004_create_audit_log_content_partitioned.sql` | migration | schema (partitioned) | (same as 0003) | upstream |
| `gateway/db/migrations/0005_create_model_aliases.sql` | migration | schema (lookup) | `cobrancas-api/src/db/migrations/0000_init.sql` lines 17-37 (simple table) | exact (style) |
| `gateway/db/migrations/0006_create_usage_counters_skeleton.sql` | migration | schema (empty skeleton) | `cobrancas-api/src/db/migrations/` (numbered siblings) | role-match |
| `gateway/db/queries/auth.sql` | sqlc query | request-response | NO ANALOG — first sqlc in Ifix | upstream: `sqlc.dev` docs |
| `gateway/db/queries/audit.sql` | sqlc query | batch insert | (same) | upstream |
| `gateway/db/queries/admin.sql` | sqlc query | CRUD | (same) | upstream |
| `gateway/sqlc.yaml` | config | startup | NO ANALOG | upstream |

### Infra / CI

| Phase 2 file | Role | Data Flow | Closest Analog | Match Quality |
|---|---|---|---|---|
| `gateway/Dockerfile` | infra / Docker | build pipeline | `pod/health-bridge/Dockerfile` | exact (same Go multi-stage + distroless) |
| `gateway/docker-compose.yml` (dev) | infra | service orchestration | `pod/docker-compose.yml` + `converseai-v4/docker-compose.yml` | role-match |
| `.github/workflows/build-gateway.yml` | CI (GHA) | build + push + deploy | `.github/workflows/build-pod.yml` + `converseai-v4/.github/workflows/deploy-dev.yml` lines 161-173 (Portainer webhook) | exact (merge of the two) |
| `gateway/README.md` | docs | — | `pod/README.md` (exists) | role-match |

### Tests

| Phase 2 file | Role | Data Flow | Closest Analog | Match Quality |
|---|---|---|---|---|
| `gateway/internal/auth/apikey_test.go` | unit test | — | `pod/health-bridge/probes_test.go` | exact (httptest.NewServer pattern) |
| `gateway/internal/audit/writer_test.go` | unit test | — | `pod/health-bridge/probes_test.go` (concurrent test) + `pod/health-bridge/main_test.go` | exact |
| `gateway/internal/idempotency/store_test.go` | unit test | — | (same) | exact |
| `gateway/internal/proxy/*_test.go` | unit test | — | `pod/health-bridge/main_test.go` (handler test via httptest) | exact |
| `gateway/internal/integration_test/*.go` | integration test (testcontainers) | — | NO ANALOG — first testcontainers in Ifix | upstream: `testcontainers-go` docs |

---

## Pattern Assignments

### `gateway/cmd/gateway/main.go` (Go main, HTTP server)

**Primary analog:** `/home/pedro/projetos/pedro/gpu-ifix/pod/health-bridge/main.go`

**Package header + imports pattern** (lines 1-16):

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)
```

**Config-then-logger-then-run pattern** (lines 83-105):

```go
func main() {
	selfCheck := flag.Bool("self-check", false, "exit 0 immediately (docker healthcheck)")
	flag.Parse()
	if *selfCheck {
		fmt.Println("ok")
		os.Exit(0)
	}

	cfg := loadConfig()
	log := newLogger(cfg)
	log.Info("starting health-bridge",
		"port", cfg.Port,
		"llama", cfg.LlamaURL,
		...
	)
```

**Graceful shutdown + signal pattern** (lines 109-170):

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
go func() {
	sig := <-sigCh
	log.Info("signal received, shutting down", "signal", sig.String())
	cancel()
}()

// ... workers spawned, http.Server started in goroutine ...

select {
case <-ctx.Done():
case err := <-serverErr:
	log.Error("http server failed", "err", err)
	cancel()
}

shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 25*time.Second)
defer shutdownCancel()
if err := srv.Shutdown(shutdownCtx); err != nil {
	log.Error("graceful shutdown error", "err", err)
}
wg.Wait()
log.Info("health-bridge exited cleanly")
```

**What to copy verbatim (style):**
- `--self-check` flag for docker healthcheck (fresh-spawned binary exits 0)
- `log := newLogger(cfg)` returns a `*slog.Logger` seeded with `.With("module", "GATEWAY")`
- `sigCh := make(chan os.Signal, 1)` + `signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)`
- `shutdownCtx, _ := context.WithTimeout(context.Background(), 25*time.Second)` — same 25s budget as Phase 1
- `wg.Wait()` after `srv.Shutdown` so background workers (audit flusher, probe goroutines, metrics server) drain too

**Divergences for gateway/cmd/gateway/main.go (novel):**
- HTTP router is `chi.NewRouter()` (NOT `http.NewServeMux()`). Register middleware via `r.Use(...)` in this order: `httpx.RequestID` → `httpx.Logger` → `httpx.Recoverer` → `auth.APIKey` → sub-routers.
- `http.Server` timeouts per CONTEXT.md D-C (plumbing): `ReadHeaderTimeout: 10s`, `ReadTimeout: 60s`, `WriteTimeout: 0` (streaming), `IdleTimeout: 120s`, `MaxHeaderBytes: 1 MiB`.
- Spawn audit flusher goroutine before `srv.ListenAndServe` so writes in the first request don't panic on nil channel.
- Sentry init is first line after config parse (`sentry.Init(...)` — before any `log.Info` so panics during startup still fly to Sentry).
- On `ctx.Done()` path: call `sentry.Flush(2*time.Second)` before final `return` to not drop error events.

---

### `gateway/cmd/gatewayctl/main.go` (Go CLI)

**Analog:** NO ANALOG — first CLI in the repo.
**Closest in-repo style anchor:** `pod/health-bridge/main.go` (flag parsing + env loading).

**Recommended CLI shape (novel, no excerpt to copy):**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
)

func main() {
	if len(os.Args) < 2 {
		usage(); os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	cfg := config.Load()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("module", "GATEWAYCTL")

	switch cmd {
	case "migrate":
		runMigrate(args, cfg, log)
	case "tenant":
		runTenant(args, cfg, log)
	case "key":
		runKey(args, cfg, log)
	case "audit":
		runAudit(args, cfg, log)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage(); os.Exit(2)
	}
}
```

**What to copy:**
- `slog` + `module=GATEWAYCTL` for all CLI output (admin operations produce audit-trail-worthy logs).
- Reuse `gateway/internal/config` and `gateway/internal/db` — DO NOT duplicate DSN parsing.
- Each subcommand uses `flag.NewFlagSet(name, flag.ExitOnError)` for its own flags.
- Print human-readable output to stdout (tables) — `os.Getenv("GATEWAYCTL_FORMAT") == "json"` triggers JSON for scriptability.

**Subcommands (D-A3, D-D1, D-B3):**

| Subcommand | Flags | Purpose |
|---|---|---|
| `migrate up` / `migrate down N` / `migrate status` | — | goose wrapper; exposes `goose.Up/Down/Status` |
| `tenant create --name X --slug x` | required | Inserts into `tenants`. Prints tenant ID. |
| `key create --tenant X --data-class {normal\|sensitive}` | required | Generates `ifix_sk_<32-base32>`, hashes argon2id, stores key. Prints raw key ONCE (to stdout). |
| `key revoke <id>` | — | Flips `status=revoked`, stamps `revoked_at`. |
| `audit export-month --month YYYY-MM --bucket ifix-ai-gateway-audit-cold` | — | Streams parquet to MinIO; drops partitions >90 days. Job runs as cron in Phase 4+; CLI covers Phase 2 manual use. |

---

### `gateway/internal/config/config.go` (config / env loading)

**Analog:** `/home/pedro/projetos/pedro/gpu-ifix/pod/health-bridge/main.go` lines 18-58.

**Config struct + loader pattern:**

```go
// Config holds runtime configuration sourced from env vars (documented in
// pod/.env.example and pod/docker-compose.yml).
type Config struct {
	Port          int
	LlamaURL      string
	SpeachesURL   string
	InfinityURL   string
	ProbeInterval time.Duration
	LogLevel      string
	Env           string
}

func loadConfig() Config {
	return Config{
		Port:          atoiOr(os.Getenv("HEALTH_BRIDGE_PORT"), 9100),
		LlamaURL:      envOr("LLAMA_URL", "http://llama:8000"),
		SpeachesURL:   envOr("SPEACHES_URL", "http://speaches:8000"),
		InfinityURL:   envOr("INFINITY_URL", "http://infinity:8002"),
		ProbeInterval: time.Duration(atoiOr(os.Getenv("PROBE_INTERVAL_SECONDS"), 10)) * time.Second,
		LogLevel:      envOr("LOG_LEVEL", "info"),
		Env:           envOr("ENV", "production"),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
```

**Envelope to produce for the gateway (novel fields):**

```go
type Config struct {
	// HTTP
	Port              int           // GATEWAY_PORT, default 8080
	ReadHeaderTimeout time.Duration // fixed 10s (not env-driven in Phase 2)
	ReadTimeout       time.Duration // fixed 60s

	// Postgres / Redis
	PGDSN         string // AI_GATEWAY_PG_DSN (required)
	PGMaxConns    int32  // AI_GATEWAY_PG_MAX_CONNS, default 10
	RedisAddr     string // AI_GATEWAY_REDIS_ADDR (required)
	RedisPassword string // AI_GATEWAY_REDIS_PASSWORD (optional)
	RedisKeyPrefix string // fixed "gw:"

	// Upstreams
	UpstreamLLMURL          string // UPSTREAM_LLM_URL (required)
	UpstreamSTTURL          string // UPSTREAM_STT_URL (required)
	UpstreamEmbedURL        string // UPSTREAM_EMBED_URL (required)
	UpstreamHealthBridgeURL string // UPSTREAM_HEALTH_BRIDGE_URL (required)

	// Obs
	SentryDSN string // SENTRY_DSN (optional)
	LogLevel  string // LOG_LEVEL, default info
	Env       string // ENV, default production

	// Admin / bootstrap
	BootstrapTenantSlug string // BOOTSTRAP_TENANT_SLUG, default "converseai"
}
```

**Divergences:**
- **Fail-fast on required vars.** Return `(Config, error)` from `Load()`; in `main.go` call `cfg, err := config.Load(); if err != nil { log.Error(...); os.Exit(2) }`. Required: `PGDSN`, `RedisAddr`, all 4 upstream URLs.
- **Read once at startup** — NEVER re-read at runtime (convention from cobrancas-api `src/config.ts`).
- **No Zod validation** — Go does this with typed struct fields; invalid DSN fails when the pool opens (fail-fast there, with clear error message).

---

### `gateway/internal/httpx/requestid.go` (middleware — X-Request-ID)

**Analog:** NO ANALOG — first middleware in Ifix Go.
**Conceptual neighbor:** `cobrancas-api/src/lib/logger.ts` `createLogger(module).child({request_id})` — same notion of request-scoped correlation ID.

**Pattern to produce (novel):**

```go
// Package httpx provides HTTP middleware and helpers shared by the gateway
// server. Request-ID propagation, slog logger threading, and the response
// envelope helpers live here.
package httpx

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	clientRequestIDKey
	loggerKey
)

// RequestID injects a UUIDv7 X-Request-ID on every request and threads it
// through the context. If the client sent X-Request-ID in the request
// headers, we accept it ONLY for logging (as client_request_id) and still
// generate a gateway-scoped request_id. Rationale: clients cannot forge
// our audit-log keys.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid, err := uuid.NewV7()
		if err != nil {
			// UUIDv7 can't actually fail on current systems, but be defensive.
			rid = uuid.New()
		}
		ridStr := rid.String()
		w.Header().Set("X-Request-ID", ridStr)

		ctx := context.WithValue(r.Context(), requestIDKey, ridStr)
		if client := r.Header.Get("X-Request-ID"); client != "" && isValidUUID(client) {
			ctx = context.WithValue(ctx, clientRequestIDKey, client)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFrom extracts the gateway-generated request ID from ctx.
func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}
```

**What to copy from existing conventions:**
- `context.WithValue` with an **unexported typed key** (Go best practice; `chi/middleware` uses this same shape).
- `uuid.NewV7()` — gives sortable, temporally ordered IDs (critical for D-D1: `audit_log` queries benefit).
- Add ID to response header EARLY so errors during handler execution still return it.
- Propagate slog `logger` via context too (`ctx = context.WithValue(ctx, loggerKey, log.With("request_id", ridStr))`), so any downstream handler can do `httpx.LoggerFrom(ctx).Info(...)`.

---

### `gateway/internal/httpx/redact.go` (slog redactor)

**Analog:** NO ANALOG.
**Design (novel):**

```go
// Redactor is a slog.Handler wrapper that replaces the value of any
// attribute whose key is in sensitiveKeys with "***REDACTED***" before
// the record is emitted. Applied to every record (not just errors) per
// CONTEXT.md D-B7.
var sensitiveKeys = map[string]bool{
	"authorization":       true,
	"x-api-key":           true,
	"cookie":              true,
	"proxy-authorization": true,
	"api_key":             true,       // body fields too
}

type Redactor struct{ inner slog.Handler }

func (r *Redactor) Handle(ctx context.Context, rec slog.Record) error {
	newRec := rec.Clone()
	newRec.Attrs = nil // rebuild
	rec.Attrs(func(a slog.Attr) bool {
		if sensitiveKeys[strings.ToLower(a.Key)] {
			newRec.AddAttrs(slog.String(a.Key, "***REDACTED***"))
		} else {
			newRec.AddAttrs(a)
		}
		return true
	})
	return r.inner.Handle(ctx, newRec)
}
```

**Applied to Sentry as well** (obs/sentry.go `BeforeSend` hook) — the same map drives both, single source of truth per D-B7 "duplicar a proteção".

---

### `gateway/internal/httpx/envelope.go` (response envelope helpers)

**Analog:** `/home/pedro/projetos/pedro/gpu-ifix/pod/health-bridge/handlers.go` lines 10-20.

**Pattern to copy:**

```go
// writeJSON writes a JSON body and status code.
//
// A write error is ignored because it only happens on client disconnect
// mid-write (browser closed tab, gateway timed out) — the payload is a
// bounded state snapshot and there is nothing useful to log beyond what
// net/http already emits when its connection goes away.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
```

**Extend with OpenAI error envelope** (novel — per CONTEXT.md D-A5 and D-C3):

```go
import "github.com/ifixtelecom/gpu-ifix/pkg/openai"

// WriteOpenAIError emits the standard { error: { message, type, code } }
// envelope per OpenAI API spec. Used by proxy.ErrorHandler, auth
// middleware rejection, idempotency conflicts, and any 4xx/5xx path.
func WriteOpenAIError(w http.ResponseWriter, status int, errType, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(openai.ErrorResponse{
		Error: openai.ErrorDetail{Message: msg, Type: errType, Code: code},
	})
}
```

**What to note:**
- Import `pkg/openai` — DO NOT redefine `ErrorResponse` / `ErrorDetail` (per CONTEXT.md D-13 and explicit note in canonical_refs).
- Status-code-to-type mapping table in the same file: `401 → "authentication_error"`, `422 → "invalid_request_error"`, `502 → "api_error"`, `500 → "api_error"`.

---

### `gateway/internal/auth/apikey.go` (middleware + verification)

**Analog:** NO ANALOG — first auth code in the repo.
**Conceptual anchors:**
- `pod/health-bridge/probes.go` — HTTP client tuning + context propagation (mirror the ergonomics).
- `cobrancas-api/src/modules/invoices/index.ts` — plugin style (but TS); closest gateway equivalent is a chi middleware.

**Recommended structure (novel):**

```go
// Package auth implements multi-tenant API-key authentication with
// argon2id verification and a Redis-backed hot-path cache (CONTEXT.md
// decisions D-A1..D-A5).
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// Package-level sentinel errors — style matches pod/health-bridge which
// uses typed sentinels with errors.Is checks downstream.
var (
	ErrMissingAPIKey  = errors.New("auth: no API key in request")
	ErrInvalidAPIKey  = errors.New("auth: API key not found")
	ErrRevokedAPIKey  = errors.New("auth: API key revoked")
	ErrMalformedKey   = errors.New("auth: malformed API key")
)

// DataClass is the tenant-level LGPD classification derived from the
// api_keys row. Propagated via context so the audit writer can decide
// whether to persist body content (D-B2).
type DataClass string

const (
	DataClassNormal    DataClass = "normal"
	DataClassSensitive DataClass = "sensitive"
)

// AuthContext is what a successful Verify() puts on the request context.
type AuthContext struct {
	TenantID  string
	APIKeyID  string
	DataClass DataClass
	KeyPrefix string // "ifix_sk_****abcd" for logs
}

// Middleware returns a chi-compatible middleware that runs Verify and,
// on success, threads AuthContext through ctx. On failure returns a 401
// with the OpenAI error envelope.
func Middleware(v *Verifier, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := extractKey(r)
			if key == "" {
				httpx.WriteOpenAIError(w, http.StatusUnauthorized,
					"authentication_error", "no_api_key",
					"Missing API key. Provide Authorization: Bearer <key> or X-API-Key header.")
				return
			}
			ac, err := v.Verify(r.Context(), key)
			if err != nil {
				code := "invalid_api_key"
				if errors.Is(err, ErrRevokedAPIKey) {
					code = "revoked_api_key"
				}
				log.WarnContext(r.Context(), "auth rejected", "err", err, "key_prefix", keyPrefix(key))
				httpx.WriteOpenAIError(w, http.StatusUnauthorized, "authentication_error", code, err.Error())
				return
			}
			ctx := WithAuth(r.Context(), ac)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractKey returns the raw key string per D-A5: Authorization: Bearer
// takes precedence over X-API-Key.
func extractKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.Header.Get("X-API-Key")
}
```

**Verify() data flow (fast-path cached, slow-path argon2 + DB):**

```go
type Verifier struct {
	db    *pgxpool.Pool
	redis *redis.Client
	argon argon2idParams
	log   *slog.Logger
}

// Verify returns AuthContext on success.
//
// Cache hit path:  Redis GET gw:apikey:{sha256(key)} -> JSON {tenant_id, api_key_id, data_class, status}
// Cache miss path: argon2id.Compare(hashes_in_DB, key) -> cache with TTL=60s
//
// Revoked keys are cached as status="revoked" for 60s too so revocation
// propagates within the cache TTL.
func (v *Verifier) Verify(ctx context.Context, rawKey string) (AuthContext, error) {
	if !strings.HasPrefix(rawKey, "ifix_sk_") || len(rawKey) != 8+32 {
		return AuthContext{}, ErrMalformedKey
	}
	cacheKey := "gw:apikey:" + sha256Hex(rawKey)
	if hit, err := v.cacheGet(ctx, cacheKey); err == nil {
		return hit.toAuthContext(), hit.validate()
	}
	// Slow path: DB lookup by key_prefix, argon2 verify each candidate
	row, err := v.dbLookup(ctx, rawKey)
	if err != nil {
		return AuthContext{}, err
	}
	_ = v.cachePut(ctx, cacheKey, row, 60*time.Second)
	return row.toAuthContext(), row.validate()
}
```

**What to copy from pod/health-bridge/probes.go:**
- `http.NewRequestWithContext(ctx, ...)` pattern for DB queries (pgx already uses ctx-first — follow the same explicit threading).
- Sentinel errors, `fmt.Errorf("stage: %w", err)` wrapping.
- Use `slog.Logger` with `.WithContext(ctx)` when available to carry request_id into log records.

**DO NOT:**
- Do NOT put the raw key in any log line (redactor handles `Authorization`/`X-API-Key` at slog level, but still don't structured-log it).
- Do NOT fall back to DB on every request — the whole point of the 60s Redis cache is to keep argon2 off the hot path.

---

### `gateway/internal/auth/argon2.go` (crypto helper)

**Analog:** NO ANALOG — first crypto code in the repo.
**Upstream reference:** `golang.org/x/crypto/argon2` OR `github.com/alexedwards/argon2id` (ergonomic wrapper — CONTEXT.md canonical_refs suggests the latter).

**Parameters (OWASP 2026 for argon2id):**

```go
var DefaultParams = argon2id.Params{
	Memory:      64 * 1024, // 64 MiB
	Iterations:  3,
	Parallelism: 2,
	SaltLength:  16,
	KeyLength:   32,
}
```

**Format for storage:** The `$argon2id$v=19$m=...,t=...,p=...$<salt-b64>$<hash-b64>` encoded string (standard argon2 "PHC" format). Stored as-is in `api_keys.key_hash TEXT`.

**Verification cost on 4 vCPU VPS:** ~30-50 ms per call; that's why CONTEXT.md D-A2 caches results for 60s.

**Key generation (for `gatewayctl key create`):**

```go
// GenerateAPIKey produces ifix_sk_<32 url-safe base32> per D-A1.
func GenerateAPIKey() (raw string, hash string, prefix string, err error) {
	b := make([]byte, 20) // 20 bytes = 32 base32 chars
	if _, err = rand.Read(b); err != nil {
		return
	}
	// url-safe base32 alphabet without padding, ambiguous chars avoided.
	raw = "ifix_sk_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	hash, err = argon2id.CreateHash(raw, &DefaultParams)
	if err != nil {
		return
	}
	prefix = "ifix_sk_****" + raw[len(raw)-4:]
	return
}
```

---

### `gateway/internal/auth/cache.go` (Redis cache)

**Analog:** NO ANALOG in gpu-ifix.
**Conceptual neighbor:** `converseai-v4/apps/api/src/lib/redis.ts` — same pattern of prefixed keys (`gw:` is the gateway's namespace per CONTEXT.md Integration Points).

**Pattern (novel):**

```go
type cacheEntry struct {
	TenantID  string    `json:"tenant_id"`
	APIKeyID  string    `json:"api_key_id"`
	DataClass DataClass `json:"data_class"`
	Status    string    `json:"status"` // "active" | "revoked"
	KeyPrefix string    `json:"key_prefix"`
}

func (v *Verifier) cacheGet(ctx context.Context, key string) (cacheEntry, error) {
	raw, err := v.redis.Get(ctx, key).Bytes()
	if err != nil {
		return cacheEntry{}, err // redis.Nil is the miss sentinel
	}
	var e cacheEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return cacheEntry{}, err
	}
	return e, nil
}

func (v *Verifier) cachePut(ctx context.Context, key string, e cacheEntry, ttl time.Duration) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return v.redis.Set(ctx, key, b, ttl).Err()
}
```

**Key prefix rule (global across gateway):** every Redis key starts with `gw:` — documented in `gateway/README.md`. Never write a bare key.

---

### `gateway/internal/audit/writer.go` (async buffered audit flusher)

**Analog:** NO ANALOG — first async-write subsystem in the repo.
**Conceptual neighbor:** `converseai-v4/apps/worker/src/queues/` — similar notion of "producer doesn't block" but via BullMQ, not in-process goroutine.

**Design (novel — directly from CONTEXT.md D-B4):**

```go
// Package audit writes metadata + (when data_class=normal) content rows
// to Postgres without blocking the request hot path. Buffer size 1000;
// flush when 500 rows OR 1s elapsed; on buffer full, drop + metric.
package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const (
	bufferSize       = 1000
	flushBatchSize   = 500
	flushInterval    = 1 * time.Second
)

// Event is the full audit row (metadata) plus optional Content.
type Event struct {
	TS                   time.Time
	RequestID            string
	TenantID             string
	APIKeyID             string
	DataClass            string
	Route                string
	Method               string
	Upstream             string
	StatusCode           int
	LatencyMs            int64
	TokensIn             int
	TokensOut            int
	CostBRL              *float64
	ErrorCode            string
	IdempotencyReplayed  bool
	Stream               bool
	Truncated            bool
	// Content only written when DataClass == "normal"
	Prompt               []byte // JSON bytes (RawMessage)
	Response             []byte
	// Whisper audio metadata (D-B6)
	AudioFilename        string
	AudioMime            string
	AudioSizeBytes       int64
	AudioDurationS       float64
	AudioLanguage        string
}

type Writer struct {
	ch  chan Event
	db  *pgxpool.Pool
	log *slog.Logger
}

func NewWriter(db *pgxpool.Pool, log *slog.Logger) *Writer {
	return &Writer{
		ch:  make(chan Event, bufferSize),
		db:  db,
		log: log.With("module", "AUDIT"),
	}
}

// Enqueue is the hot-path API. NEVER blocks: if buffer is full, drops
// the event and increments gateway_audit_dropped_total (D-B4 fail-safe).
func (w *Writer) Enqueue(e Event) {
	select {
	case w.ch <- e:
	default:
		obs.AuditDroppedTotal.Inc()
		w.log.Warn("audit buffer full — event dropped",
			"request_id", e.RequestID, "tenant_id", e.TenantID)
	}
}

// Run is the flusher goroutine. Spawn once from main, pass the root ctx.
func (w *Writer) Run(ctx context.Context) {
	batch := make([]Event, 0, flushBatchSize)
	tick := time.NewTicker(flushInterval)
	defer tick.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := w.flushBatch(ctx, batch); err != nil {
			w.log.Error("flush failed", "err", err, "batch_size", len(batch))
			// Best-effort: do NOT retry indefinitely; drop on error to
			// keep the goroutine drained. Sentry captures the error.
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush() // drain on shutdown
			return
		case e := <-w.ch:
			batch = append(batch, e)
			if len(batch) >= flushBatchSize {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}
```

**flushBatch SQL strategy (from CONTEXT.md D-D2 — sqlc queries):**
- Two COPY-style inserts per batch: one into `audit_log`, one into `audit_log_content` (filtered where `DataClass == "normal"` AND `Prompt != nil`).
- Wrapped in a single `pgx.BeginTx` — partial failure rolls back both.
- Use `pgx.CopyFrom` for > 100 rows to leverage Postgres COPY protocol (faster than VALUES).

**Shutdown sequence (main.go must enforce):**
1. `cancel()` root ctx
2. Server stops accepting new connections (`srv.Shutdown(...)`)
3. In-flight handlers drain → they finish enqueueing
4. `wg.Wait()` on audit goroutine (goroutine sees `ctx.Done()`, does final `flush()`, returns)
5. Pool.Close()

---

### `gateway/internal/audit/tee.go` (SSE tee writer)

**Analog:** NO ANALOG.
**Design (novel — directly from CONTEXT.md D-B5):**

```go
// teeBody wraps the upstream response body so the proxy can stream
// chunks to the client AND capture up to 128 KB for the audit row.
// Implements io.ReadCloser.
const sseContentCap = 128 * 1024 // 128 KB per D-B5

type teeBody struct {
	upstream io.ReadCloser
	buf      *bytes.Buffer
	truncated bool
	capLeft  int
}

func newTeeBody(upstream io.ReadCloser) *teeBody {
	return &teeBody{upstream: upstream, buf: &bytes.Buffer{}, capLeft: sseContentCap}
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.upstream.Read(p)
	if n > 0 && t.capLeft > 0 {
		take := n
		if take > t.capLeft {
			take = t.capLeft
			t.truncated = true
		}
		t.buf.Write(p[:take])
		t.capLeft -= take
	}
	return n, err
}

func (t *teeBody) Close() error {
	return t.upstream.Close()
}
```

**Integration point:** `httputil.ReverseProxy.ModifyResponse` swaps `resp.Body = newTeeBody(resp.Body)`. After the client disconnects or the stream completes, proxy calls a callback `onStreamClose(requestID, tee)` that packages `tee.buf` into an `audit.Event` and calls `audit.Writer.Enqueue(...)`.

**Goroutine leak caution (per research PITFALLS §9 noted in CONTEXT.md canonical_refs):** the callback MUST fire on BOTH happy path and client-disconnect; chain it into `tee.Close()` and also a `defer` at the end of the proxy handler.

---

### `gateway/internal/idempotency/store.go` (Redis idempotency cache)

**Analog:** NO ANALOG.
**Reference:** Stripe idempotency-key docs (CONTEXT.md canonical_refs).

**Design (novel — directly from CONTEXT.md D-C1..D-C5):**

```go
type Entry struct {
	Status       int               `json:"status"`
	Headers      map[string]string `json:"headers"` // whitelist only
	Body         []byte            `json:"body"`
	RequestHash  string            `json:"request_hash"` // sha256 hex
	StoredAt     time.Time         `json:"stored_at"`
}

var headerWhitelist = []string{
	"Content-Type",
	"X-Request-ID",
	"OpenAI-Organization",
	"OpenAI-Processing-Ms",
}

const ttl = 24 * time.Hour

type Store struct {
	redis *redis.Client
	log   *slog.Logger
}

// Get returns (entry, exists, err). Caller does the body-hash comparison
// and decides replay vs 422 conflict vs fresh execution.
func (s *Store) Get(ctx context.Context, tenantID, idemKey string) (Entry, bool, error) {
	k := "gw:idem:" + tenantID + ":" + idemKey
	raw, err := s.redis.Get(ctx, k).Bytes()
	if errors.Is(err, redis.Nil) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return Entry{}, false, err
	}
	return e, true, nil
}

// Put stores the entry with 24h TTL (D-C2). Setnx ensures we don't
// overwrite an existing entry (only the first response is persisted).
func (s *Store) Put(ctx context.Context, tenantID, idemKey string, e Entry) error {
	k := "gw:idem:" + tenantID + ":" + idemKey
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return s.redis.SetNX(ctx, k, b, ttl).Err()
}
```

**Scope gate (D-C4):** middleware must reject `Idempotency-Key` on non-`/v1/chat/completions` routes AND on chat requests where `stream=true`. Returns 400 with `code=idempotency_key_unsupported_stream` or `code=idempotency_key_unsupported_route`.

---

### `gateway/internal/idempotency/hash.go` (SHA-256 request hash)

**Analog:** NO ANALOG.

**Design (novel):**

```go
// HashBody returns the SHA-256 hex of the canonicalized JSON body.
// Canonicalization = parse as map, sort keys, re-marshal. This ensures
// that two semantically identical payloads with different key orders
// hash identically — per CONTEXT.md D-C2.
func HashBody(raw []byte) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	canonical, err := canonicalMarshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
```

---

### `gateway/internal/proxy/chat.go` (reverse proxy — SSE capable)

**Analog:** NO ANALOG — first reverse proxy in the repo.
**Closest style neighbor for HTTP tuning:** `pod/health-bridge/probes.go` lines 26-37 (`newHTTPClient`).

**HTTP client tuning pattern to mirror:**

```go
// newHTTPClient returns a client tuned for long-lived probe loops.
// MaxIdleConns/IdleConnTimeout per PITFALLS §Pitfall 12.
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:          10,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		},
	}
}
```

**Reverse proxy skeleton (novel — directly from CONTEXT.md Plumbing):**

```go
// NewChatProxy builds the httputil.ReverseProxy for /v1/chat/completions.
// FlushInterval: -1 is mandatory for SSE streaming (CONTEXT.md Plumbing).
func NewChatProxy(upstream *url.URL, audit *audit.Writer, log *slog.Logger) *httputil.ReverseProxy {
	rp := httputil.NewSingleHostReverseProxy(upstream)
	rp.FlushInterval = -1 // SSE: flush every Write()
	rp.Transport = &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		// No ReadTimeout on the transport — streams are open-ended.
	}

	rp.Director = func(r *http.Request) {
		r.URL.Scheme = upstream.Scheme
		r.URL.Host = upstream.Host
		r.Host = upstream.Host
		// Propagate X-Request-ID to pod (CONTEXT.md Plumbing).
		if rid := httpx.RequestIDFrom(r.Context()); rid != "" {
			r.Header.Set("X-Request-ID", rid)
		}
		// Strip client auth headers — pod trusts the gateway, not the caller.
		r.Header.Del("Authorization")
		r.Header.Del("X-API-Key")
		r.Header.Del("Cookie")
	}

	rp.ModifyResponse = func(resp *http.Response) error {
		if isSSE(resp) {
			resp.Body = audit.NewTeeBody(resp.Body, /* onClose callback */)
		}
		return nil
	}

	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.ErrorContext(r.Context(), "upstream error", "err", err, "upstream", upstream.String())
		httpx.WriteOpenAIError(w, http.StatusBadGateway,
			"api_error", "upstream_unreachable",
			"The upstream inference service is temporarily unreachable.")
	}

	return rp
}
```

**What to copy (non-negotiable):**
- `FlushInterval: -1` — set BEFORE any `ServeHTTP` call (set once on construction).
- `ErrorHandler` returns OpenAI envelope 502 (CONTEXT.md Plumbing).
- `Director` strips client auth — pod is not a public interface.
- `ModifyResponse` wraps body in tee for SSE audit (D-B5).
- Transport tuning mirrors `pod/health-bridge/probes.go` ergonomics (same shape, different numbers).

**Siblings `embeddings.go` and `audio.go` reuse this skeleton** — only the upstream URL and response content-type detection differ. Audio proxy has `ReadTimeout: 60s` on request body path (Whisper multipart can be large); embeddings proxy is fastest (no SSE, `FlushInterval` can be left default but setting `-1` is harmless).

---

### `gateway/internal/proxy/errors.go` (error envelope translator)

**Analog (reuse, don't redefine):** `pkg/openai/types.go` lines 127-136.

```go
// ErrorResponse is the OpenAI error envelope returned by all endpoints on
// non-2xx outcomes.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the body of ErrorResponse.Error.
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}
```

**Rule:** every 4xx/5xx path in the gateway imports `pkg/openai` and uses these structs. Do NOT redefine locally (reiterates CONTEXT.md explicit constraint).

---

### `gateway/internal/obs/sentry.go` (Sentry init + redactor)

**Analog:** NO ANALOG in gpu-ifix.
**Reference:** `docs.sentry.io/platforms/go/` (canonical_refs) + `converseai-v4` Sentry init (TS pattern — shape only).

**Design (novel):**

```go
func Init(cfg config.Config) error {
	if cfg.SentryDSN == "" {
		return nil // opt-out
	}
	return sentry.Init(sentry.ClientOptions{
		Dsn:              cfg.SentryDSN,
		Environment:      cfg.Env,
		Release:          BuildVersion, // set via -ldflags at build
		TracesSampleRate: 0.0,          // obs budget in Phase 2 is minimal (CONTEXT.md Plumbing)
		BeforeSend: func(e *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			redactSentryEvent(e) // strip Authorization, X-API-Key, Cookie from request/headers
			return e
		},
	})
}
```

---

### `gateway/internal/obs/metrics.go` (Prometheus scaffold)

**Analog:** NO ANALOG.
**Design (novel — CONTEXT.md Plumbing: exactly 2 counters in Phase 2):**

```go
var (
	RequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_requests_total",
			Help: "Total requests to the gateway by route and status.",
		},
		[]string{"route", "status"}, // bounded cardinality: 3 routes × ~5 status classes
	)

	AuditDroppedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "gateway_audit_dropped_total",
			Help: "Audit events dropped because the writer buffer was full.",
		},
	)
)

// Handler returns the /metrics endpoint. Phase 7 adds histograms; Phase 2
// is deliberately minimal per CONTEXT.md Plumbing.
func Handler() http.Handler { return promhttp.Handler() }
```

---

### `gateway/internal/db/pool.go` (pgx pool)

**Analog:** NO ANALOG.
**Reference:** `jackc/pgx v5` docs (canonical_refs).

**Design (from CONTEXT.md Plumbing):**

```go
func NewPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.PGDSN)
	if err != nil {
		return nil, fmt.Errorf("pgxpool parse: %w", err)
	}
	pcfg.MaxConns = cfg.PGMaxConns // default 10 per CONTEXT.md
	pcfg.MaxConnIdleTime = 5 * time.Minute
	// Ensure search_path is set per connection (D-D4).
	pcfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path = ai_gateway, public")
		return err
	}
	return pgxpool.NewWithConfig(ctx, pcfg)
}
```

---

### `gateway/internal/db/migrate.go` (goose runner)

**Analog:** NO ANALOG.
**Reference:** `pressly/goose` (canonical_refs).

**Embed pattern (from CONTEXT.md D-D1 `//go:embed`):**

```go
import (
	"embed"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func Up(ctx context.Context, pool *pgxpool.Pool) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	db := stdlib.OpenDBFromPool(pool) // pgx stdlib adapter
	return goose.UpContext(ctx, db, "migrations")
}
```

**Naming convention** (from `converseai-v4/packages/db/src/migrations/`):
- `NNNN_description.sql` — 4-digit zero-padded, snake_case description.
- Each file starts with `-- +goose Up` / `-- +goose Down` (goose SQL migration format).

---

### `gateway/db/migrations/0001_create_tenants.sql` (SQL migration)

**Analog:** `/home/pedro/projetos/pedro/cobrancas-api/src/db/migrations/0000_init.sql` lines 1-17.

**Pattern to copy (schema + enums + table + indexes in one file):**

```sql
-- Create cobrancas schema
CREATE SCHEMA IF NOT EXISTS cobrancas;

-- Enums
CREATE TYPE client_profile AS ENUM ('standard', 'vip', 'new', 'bad_payer', 'enterprise');
...

-- Clients
CREATE TABLE cobrancas.clients (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ...
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_clients_conta_azul_id ON cobrancas.clients(conta_azul_id);
```

**Apply to gateway — mandatory file header (D-D4 `search_path`):**

```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE SCHEMA IF NOT EXISTS ai_gateway;

CREATE TABLE IF NOT EXISTS ai_gateway.tenants (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tenants_slug ON ai_gateway.tenants(slug);

-- Seed initial tenant per CONTEXT.md D-A3.
INSERT INTO ai_gateway.tenants (slug, name) VALUES ('converseai', 'ConverseAI')
ON CONFLICT (slug) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.tenants;
-- +goose StatementEnd
```

**What to copy:**
- `UUID PRIMARY KEY DEFAULT gen_random_uuid()` convention.
- `TIMESTAMPTZ NOT NULL DEFAULT NOW()` for `created_at` / `updated_at`.
- `CREATE INDEX idx_<table>_<col>` naming.
- Enums created inline with the first table that uses them.

**Divergences for Phase 2:**
- Every migration sets `SET search_path = ai_gateway, public;` at top per D-D4.
- `CREATE SCHEMA IF NOT EXISTS ai_gateway` is in 0001 only (idempotent).
- goose format: `-- +goose Up` / `-- +goose Down` + `StatementBegin/End` for multi-statement migrations (required by goose SQL parser).

---

### `gateway/db/migrations/0002_create_api_keys.sql`

**Analog:** Same as 0001.
**Columns per CONTEXT.md D-A4:**

```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE TYPE ai_gateway.api_key_status AS ENUM ('active', 'revoked');
CREATE TYPE ai_gateway.data_class AS ENUM ('normal', 'sensitive');

CREATE TABLE IF NOT EXISTS ai_gateway.api_keys (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL REFERENCES ai_gateway.tenants(id) ON DELETE CASCADE,
    key_hash       TEXT NOT NULL,
    key_prefix     TEXT NOT NULL,
    status         ai_gateway.api_key_status NOT NULL DEFAULT 'active',
    data_class     ai_gateway.data_class NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at     TIMESTAMPTZ,
    last_used_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_api_keys_tenant ON ai_gateway.api_keys(tenant_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_status_tenant ON ai_gateway.api_keys(status, tenant_id);
-- +goose StatementEnd
```

---

### `gateway/db/migrations/0003_create_audit_log_partitioned.sql`

**Analog:** NO ANALOG in Ifix.
**Reference:** Postgres 16 `PARTITION BY RANGE (ts)` docs.

**Structural guidance (novel, no excerpt to copy — CONTEXT.md D-B1, D-B3, D-B6):**

```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE TABLE IF NOT EXISTS ai_gateway.audit_log (
    ts                    TIMESTAMPTZ NOT NULL,
    request_id            UUID NOT NULL,
    tenant_id             UUID NOT NULL,
    api_key_id            UUID,
    data_class            ai_gateway.data_class NOT NULL,
    route                 TEXT NOT NULL,
    method                TEXT NOT NULL,
    upstream              TEXT,
    status_code           SMALLINT NOT NULL,
    latency_ms            INTEGER NOT NULL,
    tokens_in             INTEGER,
    tokens_out            INTEGER,
    cost_brl              NUMERIC(10, 4),
    error_code            TEXT,
    idempotency_replayed  BOOLEAN NOT NULL DEFAULT FALSE,
    stream                BOOLEAN NOT NULL DEFAULT FALSE,
    truncated             BOOLEAN NOT NULL DEFAULT FALSE,
    -- Whisper metadata (D-B6)
    audio_filename        TEXT,
    audio_mime            TEXT,
    audio_size_bytes      BIGINT,
    audio_duration_s      REAL,
    audio_language        TEXT,
    PRIMARY KEY (request_id, ts)  -- ts must be in PK for partitioning
) PARTITION BY RANGE (ts);

CREATE INDEX IF NOT EXISTS idx_audit_log_tenant_ts ON ai_gateway.audit_log(tenant_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_request_id ON ai_gateway.audit_log(request_id);

-- First partition: current month. Rolling partition creation is part of
-- `gatewayctl audit rotate-partitions` (cron in Phase 4+; manual Phase 2).
CREATE TABLE IF NOT EXISTS ai_gateway.audit_log_202604 PARTITION OF ai_gateway.audit_log
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
-- +goose StatementEnd
```

**What to note:**
- `PRIMARY KEY (request_id, ts)` — Postgres partitioning requires the partitioning column in the PK.
- First partition seeded at migration time; rolling partitions added by `gatewayctl` (Phase 2 ships manual cmd; Phase 4+ cron automates).
- Retention (D-B3): 90 days hot in Postgres + 1 year cold in MinIO Parquet — drop partitions > 90 days in `gatewayctl audit export-month`.

---

### `gateway/db/migrations/0004_create_audit_log_content_partitioned.sql`

**Same partitioning pattern as 0003** but only `prompt JSONB, response JSONB, ts` columns (CONTEXT.md D-B2). **Row is only inserted when `data_class=normal`** — this is enforced by the writer code, not by the schema (schema allows any insert; the writer filters).

---

### `gateway/db/migrations/0005_create_model_aliases.sql`

**Analog:** Any simple lookup table in `cobrancas-api/src/db/migrations/0000_init.sql`.

```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE TABLE IF NOT EXISTS ai_gateway.model_aliases (
    alias       TEXT PRIMARY KEY,      -- e.g. "qwen"
    upstream    TEXT NOT NULL,         -- "llm" | "stt" | "embed"
    target      TEXT NOT NULL,         -- concrete model id passed to the pod
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO ai_gateway.model_aliases (alias, upstream, target) VALUES
    ('qwen',   'llm',   'qwen'),
    ('whisper','stt',   'Systran/faster-whisper-large-v3'),
    ('bge-m3', 'embed', 'BAAI/bge-m3')
ON CONFLICT (alias) DO NOTHING;
-- +goose StatementEnd
```

---

### `gateway/db/migrations/0006_create_usage_counters_skeleton.sql`

**Just the empty table shape — Phase 4 populates.** Columns: `tenant_id, date, tokens_in, tokens_out, requests_count`. PK: `(tenant_id, date)`.

---

### `gateway/sqlc.yaml` (sqlc config)

**Analog:** NO ANALOG.
**Reference:** `sqlc.dev` docs.

**Pattern:**

```yaml
version: "2"
sql:
  - engine: "postgresql"
    queries: "db/queries"
    schema: "db/migrations"
    gen:
      go:
        package: "gen"
        out: "internal/db/gen"
        sql_package: "pgx/v5"
        emit_json_tags: true
        emit_prepared_queries: false
        emit_interface: true
        emit_exact_table_names: false
```

---

### `gateway/db/queries/auth.sql` (sqlc query)

**Analog:** NO ANALOG in Ifix.
**sqlc annotation pattern:**

```sql
-- name: GetActiveKeysByTenantPrefix :many
-- Used by auth.Verifier.dbLookup when argon2 must find the candidate row.
-- Scans all active keys whose key_prefix matches (keys_prefix is suffixed
-- with the last-4 chars of the raw key for indexing).
SELECT id, tenant_id, key_hash, key_prefix, status, data_class
FROM ai_gateway.api_keys
WHERE tenant_id = $1
  AND status = 'active'
ORDER BY last_used_at DESC NULLS LAST;

-- name: TouchKeyLastUsed :exec
UPDATE ai_gateway.api_keys SET last_used_at = NOW() WHERE id = $1;

-- name: InsertAPIKey :one
INSERT INTO ai_gateway.api_keys (tenant_id, key_hash, key_prefix, data_class)
VALUES ($1, $2, $3, $4)
RETURNING id, created_at;

-- name: RevokeAPIKey :exec
UPDATE ai_gateway.api_keys SET status = 'revoked', revoked_at = NOW() WHERE id = $1;
```

---

### `gateway/db/queries/audit.sql` (sqlc query)

**Goal:** high-throughput batch insert. Use a single multi-row VALUES or `pgx.CopyFrom` — but sqlc generates one-row helpers; the writer.go `flushBatch` uses `pool.CopyFrom` directly (bypassing sqlc for this hot path; sqlc handles the content-table insert).

```sql
-- name: InsertAuditLogContent :exec
INSERT INTO ai_gateway.audit_log_content (request_id, ts, prompt, response)
VALUES ($1, $2, $3, $4);
```

---

### `gateway/db/queries/admin.sql` (sqlc query — tenants/keys CRUD for gatewayctl)

```sql
-- name: CreateTenant :one
INSERT INTO ai_gateway.tenants (slug, name) VALUES ($1, $2) RETURNING id;

-- name: ListTenants :many
SELECT id, slug, name, created_at FROM ai_gateway.tenants ORDER BY created_at DESC;
```

---

### `gateway/Dockerfile` (Go multi-stage → distroless)

**Primary analog:** `/home/pedro/projetos/pedro/gpu-ifix/pod/health-bridge/Dockerfile` (exact pattern — same builder → distroless static).

**Pattern to copy (full file — mirror with substitutions):**

```dockerfile
# syntax=docker/dockerfile:1.7
# =========================================================================
# Stage 1: builder — Go toolchain + source
# =========================================================================
FROM golang:1.23-alpine AS builder
WORKDIR /build
RUN apk add --no-cache ca-certificates git

COPY go.mod go.sum* ./
RUN go mod download || true

COPY pkg/openai ./pkg/openai
COPY gateway ./gateway

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /gateway \
    ./gateway/cmd/gateway

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /gatewayctl \
    ./gateway/cmd/gatewayctl

# =========================================================================
# Stage 2: runtime — distroless static
# =========================================================================
FROM gcr.io/distroless/static-debian12 AS runtime
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
ENV TZ=America/Sao_Paulo
COPY --from=builder /gateway /gateway
COPY --from=builder /gatewayctl /gatewayctl
EXPOSE 8080
STOPSIGNAL SIGTERM
ENTRYPOINT ["/gateway"]
```

**Divergences from pod/health-bridge/Dockerfile:**
- Two binaries in one image — `/gateway` (default `ENTRYPOINT`) and `/gatewayctl` (invoked via `docker exec <container> /gatewayctl migrate up` or `docker run --rm --entrypoint /gatewayctl <image> ...`).
- `EXPOSE 8080` (not 9100 — CONTEXT.md Plumbing: `:8080` on VPS).
- No `.sha256` template check here (that was pod-specific).
- Migrations run at container boot via `goose.Up` called from `main.go` (per CONTEXT.md D-D1: "aplicados no boot OU via gatewayctl migrate up"). The `docker-compose.yml` healthcheck must have a long enough `start_period` to allow migrations on cold start.

---

### `gateway/docker-compose.yml` (dev-local stack)

**Primary analog:** `/home/pedro/projetos/pedro/gpu-ifix/pod/docker-compose.yml` (for style) + `/home/pedro/projetos/pedro/converseai-v4/docker-compose.yml` (for shared infra pattern).

**Key decisions from CONTEXT.md Integration Points:**
- Production uses shared DO Postgres + shared Redis Ifix — compose file references them as external services.
- Dev can spin up local `postgres:16` + `redis:7` via compose profiles OR testcontainers during integration tests.

**Skeleton:**

```yaml
services:
  gateway:
    image: ghcr.io/ifixtelecom/ifix-ai-gateway:${TAG:-latest-dev}
    container_name: ifix-ai-gateway
    environment:
      TZ: America/Sao_Paulo
      GATEWAY_PORT: 8080
      ENV: ${ENV:-production}
      LOG_LEVEL: ${LOG_LEVEL:-info}
      AI_GATEWAY_PG_DSN: ${AI_GATEWAY_PG_DSN}
      AI_GATEWAY_REDIS_ADDR: ${AI_GATEWAY_REDIS_ADDR}
      UPSTREAM_LLM_URL: ${UPSTREAM_LLM_URL}
      UPSTREAM_STT_URL: ${UPSTREAM_STT_URL}
      UPSTREAM_EMBED_URL: ${UPSTREAM_EMBED_URL}
      UPSTREAM_HEALTH_BRIDGE_URL: ${UPSTREAM_HEALTH_BRIDGE_URL}
      SENTRY_DSN: ${SENTRY_DSN:-}
    ports:
      - "8080:8080"
    networks:
      - traefik-public
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "/gateway", "--self-check"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 40s  # allow goose migrations on cold start

networks:
  traefik-public:
    external: true
```

**What to copy:**
- `${VAR}` interpolation only — no inline secrets (Portainer UI / .env).
- `TZ: America/Sao_Paulo` set explicitly.
- `healthcheck: /gateway --self-check` — same `--self-check` flag as `pod/health-bridge/main.go` line 84.
- `restart: unless-stopped` standard.
- `network: traefik-public` for consistency with other Ifix stacks.

---

### `.github/workflows/build-gateway.yml` (CI — build + push + Portainer redeploy)

**Primary analog A:** `/home/pedro/projetos/pedro/gpu-ifix/.github/workflows/build-pod.yml` — exact structure for tagging, test job, build job.
**Primary analog B:** `/home/pedro/projetos/pedro/converseai-v4/.github/workflows/deploy-dev.yml` lines 161-173 — Portainer webhook deploy job (Phase 1 did NOT use; Phase 2 DOES because gateway runs on Portainer, not Vast.ai).

**Structure to copy from `build-pod.yml` (already in-repo):**

```yaml
name: build-gateway

on:
  push:
    branches: [main, develop]
    tags: ['v*']
  pull_request:
    branches: [main, develop]
    paths:
      - 'gateway/**'
      - 'pkg/openai/**'
      - 'go.mod'
      - 'go.sum'
      - '.github/workflows/build-gateway.yml'
  workflow_dispatch:

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

permissions:
  contents: read
  packages: write

env:
  REGISTRY: ghcr.io
  IMAGE_GATEWAY: ghcr.io/ifixtelecom/ifix-ai-gateway
  GO_VERSION: '1.23'

jobs:
  test:
    # ... copy verbatim from build-pod.yml `test` job (go vet, go test, gofmt)
  compute-tags:
    # ... copy verbatim from build-pod.yml, substituting IMAGE_GATEWAY
  build-gateway:
    # ... copy verbatim from build-pod.yml `build-pod` job, different Dockerfile
```

**Add Portainer redeploy job (from converseai-v4/deploy-dev.yml lines 161-173):**

```yaml
  deploy-dev:
    name: Trigger Portainer redeploy (dev)
    runs-on: ubuntu-latest
    needs: [build-gateway, compute-tags]
    if: github.ref == 'refs/heads/develop' && needs.build-gateway.result == 'success'
    steps:
      - name: Trigger Portainer webhook
        if: env.PORTAINER_WEBHOOK_URL_DEV != ''
        env:
          PORTAINER_WEBHOOK_URL_DEV: ${{ secrets.PORTAINER_WEBHOOK_URL_DEV_GATEWAY }}
        run: |
          curl -X POST "$PORTAINER_WEBHOOK_URL_DEV" --fail --silent --show-error
          echo "Portainer webhook triggered — gateway container will be recreated"

  deploy-prod:
    name: Trigger Portainer redeploy (prod)
    runs-on: ubuntu-latest
    needs: [build-gateway, compute-tags]
    if: github.ref == 'refs/heads/main' && needs.build-gateway.result == 'success'
    steps:
      - name: Trigger Portainer webhook
        if: env.PORTAINER_WEBHOOK_URL_PROD != ''
        env:
          PORTAINER_WEBHOOK_URL_PROD: ${{ secrets.PORTAINER_WEBHOOK_URL_PROD_GATEWAY }}
        run: |
          curl -X POST "$PORTAINER_WEBHOOK_URL_PROD" --fail --silent --show-error
```

**What to copy from both analogs:**
- GitHub Secrets naming: `PORTAINER_WEBHOOK_URL_DEV_GATEWAY` / `PORTAINER_WEBHOOK_URL_PROD_GATEWAY` (separate from converseai's secrets).
- `curl --fail --silent --show-error` — never let a webhook failure silently pass.
- Stack names in Portainer: `ai-gateway-dev` / `ai-gateway-prod` (per CONTEXT.md Integration Points).

**Divergences from build-pod.yml:**
- **Adds `deploy-dev` / `deploy-prod` jobs** (build-pod had NO Portainer webhook because Vast.ai hosts pod).
- **Single build job** (`build-gateway`) — build-pod had two (`build-pod`, `build-health-bridge`). Gateway ships one image.
- Image name: `ghcr.io/ifixtelecom/ifix-ai-gateway` (not `ifix-ai-pod`).

---

### `gateway/README.md`

**Analog:** `/home/pedro/projetos/pedro/gpu-ifix/pod/README.md` (exact structure role-match).
**Sections to include (Pt-BR, per Ifix convention):**
- Visão geral (o que o gateway faz em Fase 2)
- Como rodar localmente (`docker compose up` com testcontainers ou Postgres/Redis externos)
- Env vars (tabela: name, required, default, description)
- Rotas expostas (`/v1/chat/completions`, `/v1/embeddings`, `/v1/audio/transcriptions`, `/v1/health/upstreams`, `/health`, `/metrics`)
- Admin workflow via `gatewayctl` (examples)
- Redis namespace: `gw:*` (document explicitly so other Ifix products don't collide)
- Deploy (Portainer stack `ai-gateway-dev` / `ai-gateway-prod`)

---

## Shared Patterns

### Timezone / locale
**Source:** `pod/health-bridge/Dockerfile` lines 46-48 + every Ifix docker-compose.
**Apply to:** `gateway/Dockerfile`, `gateway/docker-compose.yml`, all Go containers.

```dockerfile
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
ENV TZ=America/Sao_Paulo
```

Plus in compose env: `TZ: America/Sao_Paulo`. Timestamps serialized as RFC3339 via `time.Now().Format(time.RFC3339)`.

---

### Structured logging (slog with module attribute)
**Source:** `pod/health-bridge/main.go` lines 60-81 (`newLogger`).
**Apply to:** `gateway/cmd/gateway/main.go`, `gateway/cmd/gatewayctl/main.go`, every internal package.

```go
func newLogger(cfg Config) *slog.Logger {
	lvl := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if cfg.Env == "development" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h).With("module", "HEALTH_BRIDGE")
}
```

**Gateway module names:** `GATEWAY` (main), `AUTH`, `AUDIT`, `IDEM`, `PROXY`, `CONFIG`, `DB`, `OBS`, `HTTPX`.

**Wrap with Redactor:** the gateway also wraps the handler in `httpx.Redactor` (see `internal/httpx/redact.go` above) before `slog.New(handler)` — redaction is global, not opt-in per call.

---

### Sentinel errors + wrapping
**Source:** `pod/health-bridge/probes.go` lines 221-228 + `docs/CONVENTIONS.md` lines 68-82.
**Apply to:** every `internal/*` package.

```go
func failed(start time.Time, err error) ProbeResult {
	return ProbeResult{
		Status:    StatusFailed,
		LatencyMs: time.Since(start).Milliseconds(),
		LastProbe: time.Now(),
		Error:     err.Error(),
	}
}
// Wrap with context when returning up the stack:
//   return fmt.Errorf("probing %s: %w", upstream, err)
// Use errors.Is / errors.As at the caller for typed checks — never
// strings.Contains(err.Error(), "...").
```

**Gateway sentinels (declare in respective packages):**
- `auth.ErrMissingAPIKey`, `auth.ErrInvalidAPIKey`, `auth.ErrRevokedAPIKey`, `auth.ErrMalformedKey`
- `idempotency.ErrConflict`, `idempotency.ErrUnsupportedRoute`, `idempotency.ErrStreamingNotSupported`
- `proxy.ErrUpstreamUnreachable`

---

### Graceful shutdown on SIGTERM
**Source:** `pod/health-bridge/main.go` lines 109-170.
**Apply to:** `gateway/cmd/gateway/main.go`.

Critical sequence (MUST be preserved by gateway):
1. `signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)` → cancel root ctx
2. `srv.Shutdown(shutdownCtx)` with 25s budget — stops accepting new conns, waits for in-flight to finish
3. `wg.Wait()` on audit flusher, background workers → they see `ctx.Done()`, drain, exit
4. `sentry.Flush(2 * time.Second)` so error events aren't dropped
5. `pool.Close()` last

---

### HTTP client tuning
**Source:** `pod/health-bridge/probes.go` lines 26-37.
**Apply to:** `gateway/internal/proxy/*.go` reverse proxy transports + any `http.Client` inside the gateway.

```go
return &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	},
}
```

For proxy transports: `MaxIdleConns: 100`, `MaxIdleConnsPerHost: 10` (higher — the proxy fans out more than a probe does). `ResponseHeaderTimeout: 30 * time.Second` for chat (first-token latency on cold model can hit 20s).

---

### Testing style (httptest + parallel-safe state)
**Source:** `pod/health-bridge/probes_test.go` + `pod/health-bridge/main_test.go`.
**Apply to:** every `*_test.go` in `gateway/internal/*`.

```go
// pod/health-bridge/probes_test.go:20-43 — mock upstream + probe call
srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		t.Errorf("unexpected path %s", r.URL.Path)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "x",
		"object":  "chat.completion",
		...
	})
}))
defer srv.Close()

r := probeLLM(context.Background(), newHTTPClient(), srv.URL, discardLogger())
if r.Status != StatusHealthy {
	t.Errorf("got status %q want healthy; err=%q", r.Status, r.Error)
}
```

**Concurrency test pattern** (from probes_test.go:122-138):
```go
var wg sync.WaitGroup
for i := 0; i < 100; i++ {
	wg.Add(1)
	go func(i int) {
		defer wg.Done()
		// concurrent operation
	}(i)
}
wg.Wait()
```

Gateway's `audit.Writer` gets a similar concurrency test (100 goroutines call `Enqueue`, verify buffer never panics + dropped count ≤ N-1000).

**Integration tests with testcontainers-go** (novel — Phase 2 introduces):
```go
import "github.com/testcontainers/testcontainers-go/modules/postgres"

pgContainer, err := postgres.Run(ctx, "postgres:16-alpine",
    postgres.WithDatabase("gateway_test"),
    postgres.WithUsername("test"), postgres.WithPassword("test"),
    testcontainers.WithWaitStrategy(wait.ForLog("ready to accept")),
)
```

Place under `gateway/internal/integration_test/` so `go test ./...` unit runs remain fast (tests under that path are gated with `//go:build integration`).

---

### GitHub Actions concurrency + permissions
**Source:** `build-pod.yml` lines 27-33.
**Apply to:** `.github/workflows/build-gateway.yml`.

```yaml
concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

permissions:
  contents: read
  packages: write
```

---

### Ifix convention — module name in UPPER_SNAKE_CASE
**Source:** `docs/CONVENTIONS.md` line 49-54.
**Apply to:** every `slog.Logger` seed in the gateway.

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).
    With("module", "HEALTH_BRIDGE")
```

Gateway module names list (see "Shared Patterns → Structured logging" above). Never use lowercase.

---

## Ifix-Wide Conventions to Honor

Carried over from Phase 1 pattern map and `docs/CONVENTIONS.md`:

| Convention | Apply to Phase 2 |
|---|---|
| **File naming: kebab-case** | `gateway/cmd/gateway/main.go`, `gateway/internal/auth/apikey.go`, `.github/workflows/build-gateway.yml`, `0001_create_tenants.sql` |
| **Logger: slog `With("module", "UPPER_SNAKE")`** | All gateway logger seeds |
| **Constants: PascalCase for Go exports** (e.g. `DefaultProbeInterval`) vs `UPPER_SNAKE_CASE` for env vars | Follow Go idiom in code; env vars like `AI_GATEWAY_PG_DSN` |
| **NDJSON in prod, Text in dev** | Driven by `ENV=development` flag in `config` |
| **TZ=America/Sao_Paulo** | Dockerfile + compose env |
| **RFC3339 timestamps** | `time.Now().Format(time.RFC3339)` everywhere |
| **`{branch}` + `{branch}-{sha}` tagging** | `build-gateway.yml` reuses `compute-tags` job from `build-pod.yml` verbatim |
| **Image namespace `ghcr.io/ifixtelecom/`** | `ghcr.io/ifixtelecom/ifix-ai-gateway` |
| **Conventional commits with scope** | `feat(gateway): ...`, `fix(auth): ...`, `ci(build-gateway): ...`, `chore(db): ...` |
| **Package comments above `package X`** | Every new .go file |
| **Exported symbols get godoc comments starting with symbol name** | PascalCase exports |

---

## No Analog Found

Files with no close match in gpu-ifix OR cross-repo Ifix code. Planner should lean on research bundle + upstream docs:

| File | Why no analog | Use instead |
|---|---|---|
| `gateway/internal/auth/apikey.go` + `argon2.go` + `cache.go` | No auth code anywhere in Ifix Go | CONTEXT.md D-A + `alexedwards/argon2id` + `redis/go-redis` docs |
| `gateway/internal/audit/writer.go` + `tee.go` | No async-batch-writer pattern in Go | CONTEXT.md D-B + research PITFALLS §9 (goroutine hygiene) |
| `gateway/internal/idempotency/*.go` | No idempotency code in Ifix | CONTEXT.md D-C + Stripe docs |
| `gateway/internal/proxy/*.go` | No reverse proxy in Ifix | stdlib `httputil.ReverseProxy` docs + `FlushInterval: -1` for SSE |
| `gateway/internal/obs/sentry.go` | No Go Sentry in Ifix | `docs.sentry.io/platforms/go/` |
| `gateway/internal/obs/metrics.go` | No Prometheus in Ifix Go | `prometheus/client_golang` docs |
| `gateway/internal/db/pool.go` + `migrate.go` | No pgx or goose in Ifix | `jackc/pgx v5` + `pressly/goose` docs |
| `gateway/db/migrations/0003_audit_log_partitioned.sql` + `0004` | No partitioned tables in Ifix | Postgres 16 `PARTITION BY RANGE` docs |
| `gateway/db/queries/*.sql` + `sqlc.yaml` | No sqlc in Ifix | `sqlc.dev` docs |
| `gateway/internal/integration_test/*.go` | No testcontainers in Ifix | `testcontainers-go` module docs |
| `gateway/cmd/gatewayctl/main.go` | No admin CLI in Ifix | Design per CONTEXT.md D-A3 + health-bridge flag pattern |

---

## Metadata

**Analog search scope:**
- `/home/pedro/projetos/pedro/gpu-ifix/` — `pod/health-bridge/*.go`, `pkg/openai/*.go`, `pod/Dockerfile`, `.github/workflows/build-pod.yml`, `docs/CONVENTIONS.md`, `.planning/phases/01-*/*.md`
- `/home/pedro/projetos/pedro/cobrancas-api/` — `src/lib/logger.ts`, `src/lib/clickup/errors.ts`, `src/modules/invoices/index.ts`, `src/db/migrations/0000_init.sql`
- `/home/pedro/projetos/pedro/converseai-v4/` — `.github/workflows/deploy-dev.yml` (Portainer webhook), `packages/db/src/migrations/` (naming)

**Files read (non-overlapping):** 12 read calls across 10 files; zero re-reads.

**Pattern extraction date:** 2026-04-18
