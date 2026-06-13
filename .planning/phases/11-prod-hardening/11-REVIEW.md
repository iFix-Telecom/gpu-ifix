---
phase: 11-prod-hardening
reviewed: 2026-05-27T00:00:00Z
depth: standard
files_reviewed: 36
files_reviewed_list:
  - dashboard/drizzle.config.ts
  - dashboard/playwright.config.ts
  - dashboard/src/app/2fa/backup/page.tsx
  - dashboard/src/app/2fa/challenge/page.tsx
  - dashboard/src/app/2fa/enroll/page.tsx
  - dashboard/src/app/first-login/page.tsx
  - dashboard/src/app/login/page.tsx
  - dashboard/src/app/settings/operadores/page.tsx
  - dashboard/src/app/signed-out/page.tsx
  - dashboard/src/app/signup/page.tsx
  - dashboard/src/components/auth/auth-shell.tsx
  - dashboard/src/components/auth/otp-row.tsx
  - dashboard/src/lib/allowlist.test.ts
  - dashboard/src/lib/allowlist.ts
  - dashboard/src/lib/auth-client.ts
  - dashboard/src/lib/auth.test.ts
  - dashboard/src/lib/auth.ts
  - dashboard/src/lib/schema.ts
  - dashboard/src/middleware.test.ts
  - dashboard/src/middleware.ts
  - dashboard/tests/e2e/auth-redirect.spec.ts
  - gateway/cmd/gatewayctl/debug.go
  - gateway/cmd/gatewayctl/debug_test.go
  - gateway/cmd/gatewayctl/key.go
  - gateway/cmd/gatewayctl/key_test.go
  - gateway/cmd/gatewayctl/main.go
  - gateway/cmd/gateway/main.go
  - gateway/db/queries/auth.sql
  - gateway/internal/admin/debug_panic.go
  - gateway/internal/admin/debug_panic_test.go
  - scripts/dashboard/seed-admins.sh
  - scripts/deploy/preflight.sh
  - scripts/integration-smoke/audit-log-export.py
  - scripts/integration-smoke/load-replay.py
  - scripts/integration-smoke/load-replay-report-schema.json
  - scripts/integration-smoke/smoke-sensitive-failover.py
findings:
  critical: 4
  warning: 9
  info: 5
  total: 18
status: issues_found
---

# Phase 11: Code Review Report

**Reviewed:** 2026-05-27
**Depth:** standard
**Files Reviewed:** 36
**Status:** issues_found

## Summary

Phase 11 (prod-hardening + SSO security-critical) introduces a standalone Better Auth dashboard with mandatory TOTP, an admin debug panic surface, an operator key-list CLI, plus three Python integration smokes that read prod audit data. Overall the design is defensible — the cookie-claim contract is documented, the panic handler has an end-to-end integration test, and the key-list query deliberately excludes secret columns. However the adversarial pass surfaced **four BLOCKER-severity issues**:

1. **CR-01** — Middleware fail-mode on stale cookieCache routes a 2FA-enrolled user to `/2fa/enroll`, where `authClient.twoFactor.enable` overwrites the existing TOTP secret + backup codes. This is a credential-rotation primitive triggerable by any operator with a valid session and a stale cookie (TTL=60s) — the kind of cache-aware downgrade attack that bypasses D-12.
2. **CR-02** — `dashboard/drizzle.config.ts` disables TLS certificate verification (`rejectUnauthorized: false`) for the migrations connection. Drizzle-kit migrations run against production DSNs; disabling cert verification opens a MITM downgrade path on the public network where the dashboard DB lives.
3. **CR-03** — `seed-admins.sh` POSTs a body-less `{}` to `/api/auth/sign-up/email` and the three admin candidates during endpoint detection. Better Auth's signup-email route accepts `name + email + password`; on a misconfigured release a 400/422 response is treated as "endpoint exists" but a 2xx would silently create a user with whatever defaults the version accepts. Probing the production write endpoint with the production verb (POST) instead of OPTIONS is a footgun.
4. **CR-04** — `session.create.before` hook in `lib/auth.ts` infers "user passed the 2FA challenge" by matching the request path. If Better Auth's `verifyTotp` endpoint updates the existing session row instead of creating a new one (the comment hedges on this), the hook never fires, `twoFactorVerified` stays `false`, and the user redirect-loops between `/` and `/2fa/challenge` forever. The middleware unit test mocks both cookie helpers, so it cannot detect this regression.

Plus 9 warnings (cookie-cache TTL window, operator UI N+1, `BaseException` swallow during async replay, role assignment by array index, hardcoded password length, etc.) and 5 info items.

## Critical Issues

### CR-01: Stale cookieCache routes 2FA-enrolled user to /2fa/enroll → TOTP overwrite primitive

**File:** `dashboard/src/middleware.ts:56-64`
**Issue:**

When `getCookieCache(req)` returns null (cache stale or absent — explicit failure mode per the docstring), the middleware returns:

```ts
return {
  hasSession: true,
  twoFactorEnabled: false,   // <-- always false in this branch
  twoFactorVerified: false,
};
```

This routes the user to `/2fa/enroll`. The comment claims this is "the more conservative gate" — it is not. The enroll page calls `authClient.twoFactor.enable({ password })` (`2fa/enroll/page.tsx:106`) which Better Auth implements as **issue-and-replace**: it generates a new TOTP secret + new backup codes and writes them into `two_factor.{secret,backup_codes}` for that user, overwriting whatever the user had enrolled before.

Consequence: an attacker who steals an authenticated session cookie (e.g. XSS on a non-auth route, stolen laptop with locked screen but un-locked browser session, MITM during the 60-second cookieCache miss window after a redeploy) can navigate to `/2fa/enroll`, type the user's password (required for the `enable` call — but the password is also stolen in most session-takeover scenarios), and **reset the legitimate user's TOTP secret + backup codes**. The legitimate user is then locked out and the attacker has 2FA control. The challenge gate (`/2fa/challenge`) is the genuinely conservative routing — it requires a fresh TOTP that the attacker does not have.

The cookieCache miss is not theoretical: it happens on every Better Auth `secret` rotation, every deploy that clears Redis, every cookie expiry within `cookieCache.maxAge = 60` seconds, and the very first request of every fresh sign-in (the cookieCache is populated AFTER signin, not during).

**Fix:**

In the stale-cache branch, route conservatively to `/2fa/challenge` (NOT `/2fa/enroll`), OR fall back to a DB lookup in middleware. Since Edge runtime cannot use the Drizzle adapter, route to challenge:

```ts
// dashboard/src/middleware.ts
const cache = await getCookieCache(req);
if (!cache || !cache.session || !cache.user) {
  // Cache stale/absent — we cannot tell whether the user has 2FA enrolled.
  // Route to /2fa/challenge: an unenrolled user will get a clear error
  // ("two-factor not enabled") instead of being able to silently enroll
  // a fresh TOTP that overwrites their real one.
  return {
    hasSession: true,
    twoFactorEnabled: true,   // pessimistic — pretend already enrolled
    twoFactorVerified: false, // force challenge
  };
}
```

Alternatively, add a server-side `enable` guard in `auth.ts` that REJECTS `twoFactor.enable` when `user.twoFactorEnabled === true`, forcing operators to run an explicit `disable-2fa` (audit-logged, separation-of-duty per RUNBOOK-2FA-RECOVERY) before a re-enroll. This is the defense-in-depth fix that closes the primitive regardless of middleware routing.

---

### CR-02: drizzle-kit migrations connect with TLS verification disabled

**File:** `dashboard/drizzle.config.ts:12`
**Issue:**

```ts
dbCredentials: {
  url: process.env.DASHBOARD_DATABASE_URL,
  ssl: { rejectUnauthorized: false },
},
```

`rejectUnauthorized: false` disables TLS certificate validation entirely — the migration connection will trust any cert presented by anything on the wire, including a MITM intercepting the path between the operator's host and the managed Postgres. Per the project context the dashboard DB is on DigitalOcean managed Postgres reachable over public IP; the operator runs `bunx drizzle-kit push` from `ops-claude` over Tailscale → DO. A successful MITM during migration push could:

- Capture the operator's DSN (which includes user + password)
- Substitute migration SQL with arbitrary DDL (rogue columns, dropped constraints)
- Capture every schema row the migration touches (the user table; the twoFactor secrets table during a regen)

This is a security-critical mis-default copy-pasted from the dev environment. There is no comment justifying it.

**Fix:**

Require strict TLS for any non-localhost DSN. If the DO managed Postgres uses a custom CA, ship the CA cert and pin it:

```ts
import { defineConfig } from "drizzle-kit";
import { readFileSync } from "node:fs";

if (!process.env.DASHBOARD_DATABASE_URL) {
  throw new Error("DASHBOARD_DATABASE_URL must be set for drizzle-kit");
}

const url = process.env.DASHBOARD_DATABASE_URL;
const isLocalhost = /\/\/localhost|@127\./.test(url);

export default defineConfig({
  schema: "./src/lib/schema.ts",
  dialect: "postgresql",
  dbCredentials: {
    url,
    ssl: isLocalhost
      ? false
      : {
          rejectUnauthorized: true,
          // Pin the DO CA cert when targeting managed Postgres.
          ...(process.env.DASHBOARD_DB_CA_CERT
            ? { ca: readFileSync(process.env.DASHBOARD_DB_CA_CERT, "utf8") }
            : {}),
        },
  },
  verbose: true,
  strict: true,
});
```

---

### CR-03: seed-admins.sh probes write endpoints with POST `{}` — false-positive detection risks unintended writes

**File:** `scripts/dashboard/seed-admins.sh:183-217`
**Issue:**

`probe_endpoint()` issues a real `POST` with `Content-Type: application/json` and body `{}` against every candidate, including the production write endpoints:

```
'/api/auth/admin/create-user'   # admin plugin write
'/api/auth/admin/invite'        # admin plugin write
'/api/auth/forget-password'     # request-password-reset write
'/api/auth/sign-up/email'       # signup write
```

The detector accepts ANY response other than `000` and `404` as "endpoint exists". Two failure modes:

1. **False-positive detection** — Better Auth's `/api/auth/forget-password` returns 200 to all inputs (intentional, to prevent email enumeration). If `/api/auth/admin/create-user` is NOT present on this Better Auth version, the probe falls through to `/api/auth/forget-password` and **dispatches a password-reset email** to nobody (empty body → likely 400, but a future version may treat the empty body more liberally and dispatch a reset link to a default address).
2. **Probe-as-write** — On `/api/auth/sign-up/email` with default field types in some Better Auth versions, a `{}` body returns 422. But the probe loop never inspects the body — only the status code. A future Better Auth release that adds liberal coercion (`""` → `name`, `""` → `email`) could create a phantom `""@""` user. Even more concerning: the probe runs BEFORE the per-email loop, meaning a misbehaving production endpoint creates a record the operator never wrote.

The script's own docstring states "HTTP-only single-path provisioning per reviews MEDIUM #2 — this script NEVER touches Postgres directly." But probing a write endpoint with a write verb (POST) violates the spirit of the rule even when the endpoint refuses the malformed body.

**Fix:**

Use `OPTIONS` (or `HEAD`, falling back to OPTIONS) for endpoint detection, never POST. Better Auth handlers respond to OPTIONS for CORS preflight, returning 200 for routes that exist and 404 for routes that don't:

```bash
probe_endpoint() {
  local path="$1"
  local code
  code=$(curl -sS -o /dev/null -w '%{http_code}' \
    -X OPTIONS \
    -H 'Access-Control-Request-Method: POST' \
    -H 'Origin: https://dashboard-probe.invalid' \
    --max-time 10 \
    "${DASHBOARD_BASE_URL}${path}" 2>/dev/null || echo "000")
  case "$code" in
    000|404) return 1 ;;
    *) return 0 ;;
  esac
}
```

Alternatively, drop endpoint detection entirely and pin a single path via env var (`DASHBOARD_PROVISIONING_PATH`) with a clear default. The "auto-detect" feature is a footgun for a 4-admin one-off script.

---

### CR-04: session.create.before path-based inference is undocumented and unverified — silent redirect loop risk

**File:** `dashboard/src/lib/auth.ts:127-140`
**Issue:**

The hook infers "session created from 2FA challenge verify" by matching `context.path`:

```ts
session: {
  create: {
    before: async (session, context) => {
      const path = (context as { path?: string } | null)?.path ?? "";
      const fromChallenge =
        path === "/two-factor/verify-totp" ||
        path === "/two-factor/verify-backup-code";
      if (fromChallenge) {
        return { data: { ...session, twoFactorVerified: true } };
      }
      return { data: session };
    },
  },
},
```

Three concrete failure modes:

1. **`session.create.before` may never fire on verify-totp** — Better Auth's twoFactor plugin verify-totp endpoint may update the EXISTING session row (setting a flag) rather than CREATE a new session row. If it updates, `session.create.before` does not fire, `twoFactorVerified` stays `false`, and the middleware loops `/` → `/2fa/challenge` → verify OK → `/` → `/2fa/challenge` indefinitely.
2. **Path matching is fragile across Better Auth versions** — the hook's path constants are not exported by Better Auth; a minor version bump that renames `/verify-totp` to `/two-factor/totp/verify` silently breaks the hook. The auth.test.ts behavior tests (which the docstring calls "STABLE PUBLIC API only") exercise sign-in but **never** verify-totp — there is no green-path test that proves `twoFactorVerified` flips to true after a successful TOTP verify.
3. **Context type cast** — `(context as { path?: string } | null)?.path ?? ""` silently coerces any unexpected context shape to the empty string. If Better Auth ships a future change where the context wrapper changes shape (e.g. nesting under `.request.path`), the hook fails open by returning `{ data: session }` — `twoFactorVerified` stays `false` forever.

The middleware unit test mocks `getCookieCache`, so it asserts the decision tree but does not exercise the integration where the hook must actually fire. The Playwright e2e tests skip cases 2-4 unless `PLAYWRIGHT_RUN_AUTHENTICATED_CASES` is set; in CI without that env var, only case 1 (`session_expired=1`) runs.

**Fix:**

Add an integration-style test in `auth.test.ts` that exercises the full flow: signUp → signIn → enable 2FA → verifyTotp → fetch session → assert `session.twoFactorVerified === true`. The memoryAdapter already underpins this test; the missing piece is calling `auth.api.verifyTwoFactorTOTP(...)` (or whichever twoFactor plugin endpoint Better Auth 1.4.x exposes) and reading the resulting session payload back.

Additionally, replace the path-string heuristic with the documented `context.endpoint.path` or `context.endpointPath` if Better Auth exposes it:

```ts
session: {
  create: {
    before: async (session, context) => {
      // Read the canonical endpoint path from the documented field if it exists,
      // else fall back to the legacy `path` field. Match BOTH so cross-version
      // drift cannot silently break the hook.
      const ctx = context as { path?: string; endpoint?: { path?: string } } | null;
      const path = ctx?.endpoint?.path ?? ctx?.path ?? "";
      const fromChallenge =
        path === "/two-factor/verify-totp" ||
        path === "/two-factor/verify-backup-code" ||
        path === "/two-factor/totp/verify" ||
        path === "/two-factor/backup-code/verify";
      // ...
    },
  },
},
```

And — most importantly — add the verify-totp integration test. Without it, every Better Auth minor upgrade has a silent regression risk.

## Warnings

### WR-01: cookieCache 60s TTL window allows post-revocation access

**File:** `dashboard/src/lib/auth.ts:73`
**Issue:**

`cookieCache: { enabled: true, maxAge: 60 }` means the middleware reads `twoFactorVerified` from the signed cookie for up to 60 seconds without consulting the DB. If an operator's TOTP is reset via the runbook (RUNBOOK-2FA-RECOVERY.md updates `two_factor.secret`), or their session is revoked via `DELETE FROM session WHERE id = ...`, that change does not propagate to active middleware decisions for up to 60 seconds. For a 4-admin internal panel this is acceptable, but it should be documented as a known runbook gap (e.g. "after session revocation, wait 60s before assuming the operator is locked out") and the value should appear in the RUNBOOK-INCIDENTS.md class-4 entry.

**Fix:**

Add a comment in `auth.ts` cross-referencing the runbook and document the 60s lag in `RUNBOOK-2FA-RECOVERY.md`. Optionally reduce to `maxAge: 15` (the trade-off is more DB reads on the Edge cache miss path).

---

### WR-02: operadores/page.tsx — N+1 query and synchronous `for` loop with sequential awaits

**File:** `dashboard/src/app/settings/operadores/page.tsx:87-110`
**Issue:**

The roster loop runs 2 sequential DB queries per user (count active sessions + select latest session). With 4 admins this is 8 queries serially; with 40 it would be 80. The pattern is a classic N+1. For Phase 11 the user count is small but the code as written becomes a measurable cost the moment the operator roster grows beyond a handful. More importantly, the lookup order leaks ordering through the page: the operator marked `i === 0` becomes "owner" on every render — a UI-coupled-to-DB-sort-order anti-pattern (see IN-01).

**Fix:**

Replace the N+1 with a single grouped query:

```ts
const sessionStats = await db
  .select({
    userId: schema.session.userId,
    openSessions: count(),
    lastSignIn: sql`MAX(${schema.session.updatedAt})`,
  })
  .from(schema.session)
  .groupBy(schema.session.userId);

const byUser = new Map(sessionStats.map((s) => [s.userId, s]));
for (const u of users) {
  const stats = byUser.get(u.id);
  operators.push({
    id: u.id, name: u.name, email: u.email,
    twoFactorEnabled: u.twoFactorEnabled,
    lastSignIn: stats?.lastSignIn ?? null,
    openSessions: stats?.openSessions ?? 0,
  });
}
```

(Note: performance is out of v1 scope per review charter, but this is also a correctness/UX issue when the table flickers during sequential awaits.)

---

### WR-03: load-replay.py swallows `BaseException` including KeyboardInterrupt

**File:** `scripts/integration-smoke/load-replay.py:410-411`
**Issue:**

```python
except BaseException as e:  # noqa: BLE001 — capture every transport failure
    error_class = _classify_error(e, status_code) or ERROR_TIMEOUT
```

`BaseException` catches `KeyboardInterrupt`, `SystemExit`, and `GeneratorExit` in addition to ordinary exceptions. An operator pressing Ctrl-C during a 30-minute load replay will see the cancellation absorbed by this handler, the task will record `error_class: "timeout"`, and the loop continues. The script will only stop when the asyncio event loop itself receives the signal at the orchestration layer.

Additionally, line 409 calls `_classify_error(BaseException(), status_code)` on the success path — `BaseException()` is not a real exception, it's a sentinel value used to drive the function's status-code branch. The function falls through to `if status_code >= 500` because `BaseException()` matches none of the `isinstance` checks. This works but is opaque: a future reader sees "classify an exception" being called on a non-error path.

**Fix:**

```python
except Exception as e:  # not BaseException — preserve KeyboardInterrupt
    error_class = _classify_error(e, status_code) or ERROR_TIMEOUT
```

And on the success path, classify by status_code directly:

```python
status_code = r.status_code
upstream = r.headers.get("X-Upstream") or None
# Classify by status code only — no exception happened on this path.
error_class = ERROR_UPSTREAM_5XX if status_code >= 500 else None
```

---

### WR-04: load-replay.py — task spawn race + unbounded list growth

**File:** `scripts/integration-smoke/load-replay.py:530-535`
**Issue:**

The fixture-read loop spawns `asyncio.create_task(_go())` for every line, holds references in `tasks`, then `await asyncio.gather(*tasks)`. For a 30-minute replay against a high-volume audit window, `tasks` grows unboundedly with task handles AND `results` grows with response records — both held in memory the entire run. A 30-min replay at 50 req/s = 90,000 tasks each holding a closure capturing `rec`. Memory pressure may not break correctness but will hide real bugs in the replay engine itself.

Additionally, the asynchronous `await asyncio.sleep(delay_s / cfg.speedup)` happens INSIDE the fixture-read loop, before the task spawn. Tasks are spawned serially with delays preserved between them, but the semaphore-bound parallelism inside `_go` means a backlog can pile up if the gateway is slow. This is OK for SLO measurement (the gates compare wall-clock latency) but worth flagging.

**Fix:**

Use a bounded queue + consumer task pool rather than spawn-per-record:

```python
queue: asyncio.Queue = asyncio.Queue(maxsize=cfg.max_concurrency * 2)

async def consumer():
    async with sem:
        while True:
            rec = await queue.get()
            if rec is None:
                return
            await replay_one(client, cfg, rec, keys, results)

consumers = [asyncio.create_task(consumer()) for _ in range(cfg.max_concurrency)]
# ... feed queue from the fixture loop ...
for _ in consumers:
    await queue.put(None)
await asyncio.gather(*consumers)
```

---

### WR-05: gatewayctl key create — uses `flag.ExitOnError` instead of `flag.ContinueOnError`

**File:** `gateway/cmd/gatewayctl/key.go:40, 106`
**Issue:**

```go
fs := flag.NewFlagSet("key create", flag.ExitOnError)
```

`flag.ExitOnError` calls `os.Exit(2)` directly on parse error, bypassing the `runCmd` Pattern D contract (`return int`). The newer `runDebug` / `runKeyList` use `flag.ContinueOnError` + explicit `return 2`. Mixing the two patterns within the same binary means:

- A unit test of `runKeyCreate` calling `runCmd(ctx, args, log)` with bad flags terminates the test process (not the function).
- Future operators piping `gatewayctl key create --bad-flag` into a wrapper script see different behavior than `gatewayctl key list --bad-flag`.

**Fix:**

```go
fs := flag.NewFlagSet("key create", flag.ContinueOnError)
fs.SetOutput(os.Stderr)
if err := fs.Parse(args); err != nil {
  return 2
}
```

Apply the same change to `key revoke`.

---

### WR-06: middleware "stale cache" comment contradicts behavior

**File:** `dashboard/src/middleware.ts:57-64`
**Issue:**

The docstring says:

> If `getCookieCache` returns null (cache stale or absent — e.g. just after sign-in before the first cookieCache write), we conservatively treat as session-present-but-unverified and route to /2fa/challenge.

But the code actually returns `twoFactorEnabled: false`, which routes to `/2fa/enroll` (Stage 2a), NOT `/2fa/challenge`. This is the same root cause as CR-01; logging it as a separate WARN because the comment-vs-code mismatch is the smoking gun a reviewer would spot. Either the comment is wrong (then CR-01 is the intended behavior and the security regression must be fixed) or the code is wrong (and the routing should be to challenge).

**Fix:**

After applying CR-01, update the comment to match the corrected behavior:

```ts
// Cookie cache stale/absent — pessimistically treat as enrolled-but-unverified
// to avoid routing to /2fa/enroll where authClient.twoFactor.enable could
// overwrite a real TOTP secret (see CR-01).
```

---

### WR-07: bootstrap admin key — preview suffix collision risk

**File:** `gateway/cmd/gateway/main.go:1485-1488`
**Issue:**

```go
suffix := bootstrap
if len(bootstrap) >= 4 {
  suffix = bootstrap[len(bootstrap)-4:]
}
```

The fallback `suffix := bootstrap` runs only when `len(bootstrap) < 4` — i.e. the operator passed a bootstrap key SHORTER than 4 characters via `AI_GATEWAY_ADMIN_KEY_BOOTSTRAP`. In that case the FULL plaintext key is concatenated into the `key_prefix` stored in `ai_gateway.admin_keys` and logged via `log.Info`. While a 1-3 char bootstrap key would already be a deployment error, the path leaks the entire plaintext to the structured log sink (which goes to Sentry/Portainer per the redactor comment elsewhere). The Phase 11 explicit fix (ME-06) routed the random-generated bootstrap to stderr precisely to avoid this; the operator-supplied short-key path was missed.

**Fix:**

```go
suffix := bootstrap
if len(bootstrap) >= 4 {
  suffix = bootstrap[len(bootstrap)-4:]
} else {
  // Defense in depth — never log even a partial key when the operator
  // passes a key too short to safely truncate. Reject + return error so
  // the operator notices.
  return fmt.Errorf("bootstrap admin key too short (got %d chars; need >= 16)", len(bootstrap))
}
```

Or enforce a minimum length on the input env var.

---

### WR-08: drizzle-kit migrations — `verbose: true` may log DDL with embedded secrets

**File:** `dashboard/drizzle.config.ts:14`
**Issue:**

`verbose: true` causes drizzle-kit to print the full SQL of every migration step to stdout. For a fresh `bunx drizzle-kit push`, migrations may include backfill UPDATEs whose values include user emails (during a schema rename, for example) or — for the Better Auth tables — TOTP secrets if a future migration recomputes them. Verbose logging in operator-facing migrations is appropriate for dev but for prod runs the DSN log entry alone is enough; consider gating verbosity by env.

**Fix:**

```ts
verbose: process.env.NODE_ENV !== "production",
```

---

### WR-09: smoke-sensitive-failover.py — SSH command injection via GATEWAYCTL_SSH_HOST is safe in subprocess.run(list), but document it

**File:** `scripts/integration-smoke/smoke-sensitive-failover.py:398-401`
**Issue:**

```python
if GATEWAYCTL_SSH_HOST:
    cmd: list[str] = ["ssh", GATEWAYCTL_SSH_HOST] + docker_exec_cmd
```

`subprocess.run` with a list argument does not invoke a shell, so a `GATEWAYCTL_SSH_HOST` value like `"host; rm -rf /"` is passed as a single argv to `ssh`, which rejects it. The current code is safe but the script does not document the contract — a future refactor that switches to `shell=True` for "convenience" would open a command-injection hole. Add a comment.

**Fix:**

```python
if GATEWAYCTL_SSH_HOST:
    # subprocess.run with a list argument MUST NOT be changed to shell=True —
    # GATEWAYCTL_SSH_HOST comes from env and could contain shell metacharacters.
    cmd: list[str] = ["ssh", GATEWAYCTL_SSH_HOST] + docker_exec_cmd
```

## Info

### IN-01: operadores/page.tsx assigns "owner" role by array index

**File:** `dashboard/src/app/settings/operadores/page.tsx:317-327`
**Issue:**

```tsx
{operators.map((o, i) => (
  ...
  {i === 0 ? "owner" : "operator"}
```

The operator marked "owner" is whichever user happens to come first in the `ORDER BY created_at ASC` result — there is no role column. This is documentation-only UI (not used for any authorization decision) but it misleads operators into thinking there's an RBAC hierarchy. The Phase 11 design explicitly avoids the Better Auth admin plugin "no role escalation surface needed" — so the column should say either "admin" for all or be removed entirely.

**Fix:**

Either drop the column, or render a stable label:

```tsx
<td>operator</td>
```

---

### IN-02: OtpRow.tsx — convoluted no-op padding chain on paste

**File:** `dashboard/src/components/auth/otp-row.tsx:91-92`
**Issue:**

```ts
const trimmed = clampSix(raw);
onChange(trimmed.padEnd(SLOT_COUNT, " ").slice(0, SLOT_COUNT).replaceAll(" ", ""));
```

`clampSix` already returns a 0-6 char digits-only string. `padEnd(6, " ").slice(0, 6).replaceAll(" ", "")` reconstructs `trimmed` byte-for-byte — it's a no-op. The line just propagates the trimmed string.

**Fix:**

```ts
onChange(clampSix(raw));
```

---

### IN-03: 2fa/enroll page renders QR code via `<img src="data:...">` without CSP guidance

**File:** `dashboard/src/app/2fa/enroll/page.tsx:226-231`
**Issue:**

The QR PNG is rendered via `<img src={qrDataUrl}>` where `qrDataUrl` is a `data:image/png;base64,...` URL produced client-side by `qrcode.toDataURL`. This works but if the dashboard ever adds a Content-Security-Policy `img-src 'self'` directive (which is good hygiene), the QR disappears silently. Document the requirement in a comment so the CSP author knows to include `data:` in img-src.

**Fix:**

```tsx
{/* CSP NOTE: this <img> requires `img-src 'self' data:` if a CSP is added. */}
<img src={qrDataUrl} ... />
```

---

### IN-04: audit-log-export.py — sanitize_body does NOT recurse into messages[].tool_calls[].function.arguments for nested message types

**File:** `scripts/integration-smoke/audit-log-export.py:262-273`
**Issue:**

The sanitizer iterates `messages` once and replaces `tool_calls` per-message, but it does not recurse into nested message structures (e.g. tool-call responses that themselves contain JSON-serialized arguments). For the Phase 11 audit fixture this is likely fine because OpenAI's chat-completions schema is flat at the message level, but if a future tool-call format embeds nested calls, sensitive free-text leaks through. Add a defensive note.

**Fix:**

Document the assumption explicitly in `_placeholder_tool_calls`:

```python
# ASSUMPTION: tool_calls is a flat list of {function: {arguments: ...}}
# objects. If the OpenAI schema ever nests tool_calls inside a tool-call's
# response payload, this function must be extended to recurse.
```

---

### IN-05: bootstrap admin key — log.Warn after secret already on stderr is duplicate noise

**File:** `gateway/cmd/gateway/main.go:1477-1478`
**Issue:**

```go
fmt.Fprintf(os.Stderr, "\n*** ROTATE THIS KEY IMMEDIATELY ***\nbootstrap admin key: %s ...\n", bootstrap)
log.Warn("bootstrap admin key generated; see stderr for the one-time display. ROTATE via ...")
```

If the container's log driver captures both stdout AND stderr (the default for Docker `json-file`), the stderr line is ALSO in the structured log stream — defeating the purpose of routing it to stderr to keep it out of structured logs. The threat model assumes a slog redactor sees `log.*` calls but not fmt.Fprintf to stderr; in Portainer / Docker, both go to the same log pipeline.

**Fix:**

Either accept this is a single-event one-time-display (operator captures from console before container ships logs) and document it, or write the bootstrap key to a chmod-600 file on the host and log only the path. The simplest documentation fix:

```go
// NOTE: this stderr write IS captured by Docker's json-file log driver in
// most deployments. The "one-time display" guarantee is the human operator
// reading the boot log within the first 60 seconds — NOT structural log
// secrecy. Rotate via `gatewayctl admin-key create` immediately.
fmt.Fprintf(os.Stderr, ...)
```

---

_Reviewed: 2026-05-27_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
