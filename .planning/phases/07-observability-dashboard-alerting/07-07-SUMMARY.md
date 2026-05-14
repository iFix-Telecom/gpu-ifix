---
phase: 07-observability-dashboard-alerting
plan: 07
subsystem: ui
tags: [next.js, react, better-auth, shadcn, recharts, drizzle, docker, github-actions, observability]

# Dependency graph
requires:
  - phase: 04-admin-api-quota-enforcement
    provides: "/admin sub-router + admin-key bcrypt middleware — the gateway endpoints the dashboard proxy forwards to"
  - phase: 07-observability-dashboard-alerting (07-03)
    provides: "/admin/metrics + /admin/audit JSON handlers — the response shapes gateway.ts wrappers type against (07-03 runs in the same wave; shapes taken from the 07-03 plan contract)"
provides:
  - "Greenfield dashboard/ Next.js 15 app skeleton — App Router, src dir, standalone output"
  - "shadcn radix-nova design system reproduced from converseai-v4 + the --status-warning ops token"
  - "Standalone Better Auth instance (emailAndPassword only) over an ai_gateway-isolated DB"
  - "Unauthed->/login middleware auth boundary + pt-BR login page"
  - "Server-side /api/gateway/[...path] proxy that holds X-Admin-Key server-side"
  - "Typed fetchMetrics/fetchAudit/fetchUsage wrappers + vitest coverage"
  - "Dockerfile + build-dashboard.yml CI + the dashboard docker-compose service block"
affects: [07-08-dashboard-ui, 07-09-human-uat, observability, deploy]

# Tech tracking
tech-stack:
  added:
    - "next ^15.2.0, react/react-dom ^19 — matched to converseai-v4 pins"
    - "better-auth ~1.4.18 — standalone emailAndPassword-only instance"
    - "recharts 2.15.4 — pinned (CLI tried to bump to 3.x)"
    - "drizzle-orm ^0.45 + pg — Better Auth drizzleAdapter over the dashboard's own DB"
    - "shadcn ^3.8 radix-nova preset, lucide-react, @tanstack/react-query, sonner"
    - "vitest 3 + jsdom + @vitejs/plugin-react — dashboard test toolchain"
  patterns:
    - "Server-side admin-key proxy: the browser only ever sees /api/gateway/* relative paths; the admin key is read in exactly one server route handler"
    - "Lazy DB client init via a Proxy — next build evaluates route modules with no runtime env, so the Pool is constructed on first property access not at import"
    - "Pinned-version discipline: every dashboard dep matches converseai-v4 exactly; the shadcn CLI's latest-major bumps are reverted"

key-files:
  created:
    - "dashboard/package.json — pinned deps + react-is ^19 overrides block (Pitfall 3)"
    - "dashboard/components.json — radix-nova preset reproduced from converseai-v4"
    - "dashboard/src/app/globals.css — radix-nova .dark OKLCH set + --status-warning"
    - "dashboard/src/lib/auth.ts — standalone betterAuth, emailAndPassword only"
    - "dashboard/src/lib/db.ts — drizzle client over DASHBOARD_DATABASE_URL, ai_gateway-isolated"
    - "dashboard/src/lib/schema.ts — minimal Better Auth drizzle tables"
    - "dashboard/src/middleware.ts — getSessionCookie -> /login redirect"
    - "dashboard/src/app/login/page.tsx — pt-BR sign-in form"
    - "dashboard/src/app/api/gateway/[...path]/route.ts — server-side X-Admin-Key proxy"
    - "dashboard/src/lib/gateway.ts — typed fetchMetrics/fetchAudit/fetchUsage wrappers"
    - "dashboard/Dockerfile — 3-stage Next.js standalone build"
    - ".github/workflows/build-dashboard.yml — CI build + push, mirrors build-gateway.yml"
  modified:
    - "gateway/docker-compose.yml — added the dashboard service block"

key-decisions:
  - "Used worker_intra network for the dashboard compose service (the plan said traefik-public, but the actual gateway/docker-compose.yml uses worker_intra — matched reality)"
  - "Added a .npmrc with legacy-peer-deps=true — better-auth's optional @sveltejs/kit peer collides with @vitejs/plugin-react's vite peer; this lets plain `npm install` exit 0"
  - "react-is overrides pinned to ^19.0.0 (resolves react-is@19.2.6) — converseai-v4 has no overrides block because bun dedupes; npm needs the explicit pin for Pitfall 3"
  - "chart.tsx swapped to the converseai-v4 recharts-2.x version — the shadcn CLI emitted a recharts-3 variant incompatible with the pinned recharts 2.15.4"
  - "db.ts lazy-inits the Pool via a Proxy so `next build` (no runtime env) succeeds; the DSN is a deploy-time requirement, throws on first use not at import"

patterns-established:
  - "Server-side secret proxy: GATEWAY_ADMIN_KEY is greppable to exactly one file (the [...path] route handler); typed wrappers call the proxy, never the gateway"
  - "Pinned-version discipline against converseai-v4 — shadcn/CLI version drift is reverted, not accepted"

requirements-completed: [OBS-03]

# Metrics
duration: 38min
completed: 2026-05-14
---

# Phase 7 Plan 07: Dashboard Skeleton — Scaffold, Auth Boundary & Gateway Proxy Summary

**Greenfield `dashboard/` Next.js 15 app: shadcn radix-nova design system, a standalone emailAndPassword-only Better Auth instance over an ai_gateway-isolated DB, an unauthed->/login middleware, and a server-side `/api/gateway/[...path]` proxy that keeps `X-Admin-Key` greppable to exactly one file — plus Dockerfile, CI, and the docker-compose service.**

## Performance

- **Duration:** ~38 min
- **Tasks:** 3 completed
- **Files modified:** 48 (1 modified: gateway/docker-compose.yml; 47 created)

## Accomplishments

- Scaffolded the greenfield `dashboard/` Next.js 15 app — pinned every dependency to the converseai-v4 versions, reproduced the radix-nova shadcn preset, installed the 15-block UI-SPEC component inventory, and wired vitest. `npm install && npm run build && npx vitest run` all exit 0.
- Stood up a **standalone** Better Auth instance with ONLY `emailAndPassword` — every converseai-v4 plugin (organization/2FA/admin) stripped — backed by a drizzle client pointed at `DASHBOARD_DATABASE_URL`, a DB isolated from the gateway's `ai_gateway` schema (Pitfall 7). Unauthed requests redirect to `/login`; a pt-BR login page exists.
- Built the server-side gateway proxy: `/api/gateway/[...path]/route.ts` injects `X-Admin-Key` and forwards to `${GATEWAY_BASE_URL}/admin/*`. `grep -rl GATEWAY_ADMIN_KEY dashboard/src/` returns exactly that one file — the browser only ever sees relative `/api/gateway/*` paths (threat T-07-24 mitigated).
- Delivered the deploy plumbing: a 3-stage Next.js-standalone Dockerfile, `build-dashboard.yml` mirroring `build-gateway.yml` (ubuntu-latest, `dashboard/**` paths filter, GHCR push, Portainer webhooks), and the `dashboard` service block in `gateway/docker-compose.yml`.

## Task Commits

Each task was committed atomically:

1. **Task 1: Scaffold dashboard/ + shadcn radix-nova + pinned deps + vitest** - `e2fd868` (feat)
2. **Task 2: Standalone Better Auth + isolated DB + unauthed->/login middleware + login page** - `5a9dd98` (feat)
3. **Task 3: Server-side gateway proxy + typed fetch wrappers + Dockerfile + CI + compose service** - `5b518d8` (feat)

## Files Created/Modified

**Scaffold & design system (Task 1):**
- `dashboard/package.json` - pinned deps (next ^15.2.0, recharts 2.15.4, better-auth ~1.4.18) + `react-is ^19` overrides block (Pitfall 3)
- `dashboard/.npmrc` - `legacy-peer-deps=true` resolves the better-auth optional-peer collision
- `dashboard/components.json` - radix-nova preset reproduced verbatim from converseai-v4
- `dashboard/src/app/globals.css` - radix-nova `.dark` OKLCH token set + the single `--status-warning` token
- `dashboard/src/components/ui/*` - 19 shadcn radix-nova blocks (card/table/badge/alert/button/tabs/select/popover/calendar/sidebar/skeleton/sonner/separator/scroll-area/chart + transitive deps)
- `dashboard/src/components/ui/chart.tsx` - swapped to the converseai-v4 recharts-2.x version
- `dashboard/.env.example` - documents all 5 server-side dashboard env vars
- `dashboard/vitest.config.ts` + `dashboard/src/lib/smoke.test.ts` - jsdom test toolchain + smoke placeholder

**Auth boundary (Task 2):**
- `dashboard/src/lib/auth.ts` - standalone betterAuth instance, `emailAndPassword` only
- `dashboard/src/lib/db.ts` - lazy drizzle client over `DASHBOARD_DATABASE_URL`, ai_gateway-isolated
- `dashboard/src/lib/schema.ts` - minimal Better Auth drizzle tables (user/session/account/verification)
- `dashboard/src/lib/auth-client.ts` - plugin-free browser client
- `dashboard/src/app/api/auth/[...all]/route.ts` - `toNextJsHandler(auth)`
- `dashboard/src/middleware.ts` - `getSessionCookie` -> `/login` redirect; matcher excludes login/api-auth/_next/favicon
- `dashboard/src/app/login/page.tsx` - pt-BR email/password sign-in form on shadcn card + button

**Gateway proxy & deploy (Task 3):**
- `dashboard/src/app/api/gateway/[...path]/route.ts` - server-only proxy, injects `X-Admin-Key`
- `dashboard/src/lib/gateway.ts` - typed `fetchMetrics`/`fetchAudit`/`fetchUsage` wrappers (call the proxy, never the gateway)
- `dashboard/src/lib/gateway.test.ts` - vitest: wrappers hit the proxy path + parse the expected shapes
- `dashboard/Dockerfile` - 3-stage Next.js standalone build (node:20-alpine, non-root)
- `.github/workflows/build-dashboard.yml` - CI mirror of build-gateway.yml
- `gateway/docker-compose.yml` - **modified** — added the `dashboard` service block

## Decisions Made

- **worker_intra over traefik-public** — the plan's frontmatter said the dashboard compose service should join `traefik-public`, but the actual `gateway/docker-compose.yml` uses `worker_intra` (the shared overlay where Traefik discovers services). Matched the file as-built rather than the stale plan note.
- **`.npmrc` legacy-peer-deps** — better-auth declares optional peer deps for every framework adapter; `@sveltejs/kit` transitively pulls a `vite@8` peer that conflicts with `@vitejs/plugin-react`'s `vite@7`. `legacy-peer-deps=true` lets plain `npm install` (no flags) exit 0, matching how converseai-v4's bun install dedupes the same graph.
- **react-is `^19.0.0` overrides** — converseai-v4's root package.json has NO overrides block (bun dedupes react-is automatically). npm needs the explicit pin for Pitfall 3; `^19.0.0` resolves `react-is@19.2.6` (the React 19 line) for recharts 2.15.4.
- **db.ts lazy-init via Proxy** — `next build` evaluates every route module with no runtime env. A module-load `throw` on missing `DASHBOARD_DATABASE_URL` broke the build's page-data step; the Pool is now constructed on first property access, so the DSN is a deploy-time (not build-time) requirement.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] better-auth optional-peer dependency conflict blocked `npm install`**
- **Found during:** Task 1
- **Issue:** `npm install` failed with an ERESOLVE conflict — better-auth's optional `@sveltejs/kit` peer pulls `vite@8`, colliding with `@vitejs/plugin-react`'s `vite@7` peer. The Task 1 acceptance criterion requires plain `npm install` to exit 0.
- **Fix:** Added `dashboard/.npmrc` with `legacy-peer-deps=true` — the standard resolution for Next.js + better-auth on npm; converseai-v4 sidesteps it by using bun.
- **Files modified:** `dashboard/.npmrc` (created)
- **Commit:** `e2fd868`

**2. [Rule 1 - Bug] shadcn CLI bumped recharts to ^3.8.0 and react-day-picker to ^10.0.0**
- **Found during:** Task 1
- **Issue:** `npx shadcn add` rewrote `package.json`, bumping `recharts` and `react-day-picker` past the plan's explicit pins. The plan + UI-SPEC + Pitfall 3 lock `recharts` to `2.15.4` (the shadcn radix-nova chart block targets recharts 2.x). The CLI also emitted a `chart.tsx` targeting the recharts-3 API (`DefaultTooltipContentProps` type imports that don't exist in 2.15.4).
- **Fix:** Restored `recharts` to `2.15.4` and `react-day-picker` to `^9.13.2` (the converseai-v4 pins); replaced the generated `chart.tsx` with the recharts-2.x version from converseai-v4's `apps/web/src/components/ui/chart.tsx`.
- **Files modified:** `dashboard/package.json`, `dashboard/src/components/ui/chart.tsx`
- **Commit:** `e2fd868`

**3. [Rule 1 - Bug] `db.ts` threw at module load, breaking `next build`**
- **Found during:** Task 2
- **Issue:** `db.ts` threw on missing `DASHBOARD_DATABASE_URL` at import time. `next build`'s "Collecting page data" step evaluates route modules with no runtime env, so the build failed on the `/api/auth/[...all]` route.
- **Fix:** Lazy-init the Pool + drizzle client via a Proxy — constructed on first property access, throwing on first use rather than at import. The DSN stays a hard requirement, just deferred to request time.
- **Files modified:** `dashboard/src/lib/db.ts`
- **Commit:** `5a9dd98`

**4. [Rule 1 - Bug] `GATEWAY_ADMIN_KEY` literal in a `gateway.ts` comment failed the security grep**
- **Found during:** Task 3
- **Issue:** The Task 3 acceptance criterion requires `grep -rl GATEWAY_ADMIN_KEY dashboard/src/` to return ONLY the proxy route. `gateway.ts` had the literal string `GATEWAY_ADMIN_KEY` in a comment, so it matched too.
- **Fix:** Reworded the comment to "the gateway admin key" — no functional change, but the security grep now resolves to exactly the proxy route file.
- **Files modified:** `dashboard/src/lib/gateway.ts`
- **Commit:** `5b518d8`

## Known Stubs

- `dashboard/src/app/page.tsx` — placeholder home page ("Painel de observabilidade — em construção (07-08)"). **Intentional** — this plan delivers the skeleton only; the real Overview/Tenants/Incident-History views are built in 07-08 against the contracts this plan fixes (auth boundary, the gateway-fetch wrappers, the design tokens). The plan's `<objective>` explicitly states "No UI components yet (that is 07-08)".
- `dashboard/src/lib/smoke.test.ts` — placeholder smoke test. **Intentional** — confirms the vitest+jsdom+`@/` alias toolchain before 07-08 adds real component tests.

## Verification Results

- `cd dashboard && npm install` — exit 0
- `cd dashboard && npm run build` — exit 0 (routes: `/`, `/login`, `/api/auth/[...all]`, `/api/gateway/[...path]`, middleware)
- `cd dashboard && npx tsc --noEmit` — exit 0
- `cd dashboard && npx vitest run` — exit 0 (5 tests: 1 smoke + 4 gateway-wrapper)
- `grep -rl 'GATEWAY_ADMIN_KEY' dashboard/src/` — returns exactly `src/app/api/gateway/[...path]/route.ts`
- `dashboard/components.json` — `"style": "radix-nova"`, `"baseColor": "neutral"`, UI-SPEC aliases
- `dashboard/src/app/globals.css` — `--status-warning` declared exactly once
- `.github/workflows/build-dashboard.yml` exists (ubuntu-latest, `dashboard/**` PR paths filter)
- `gateway/docker-compose.yml` — `dashboard` service on the `worker_intra` network

## Threat Surface Notes

All three Task threat-model mitigations were implemented as designed — no new surface beyond the plan's `<threat_model>`:
- **T-07-24** (admin-key disclosure): `GATEWAY_ADMIN_KEY` greppable to exactly one server route handler.
- **T-07-25** (unauthenticated access): `middleware.ts` gates every route except `/login` + `/api/auth`.
- **T-07-26** (Better Auth DB collision): `db.ts` connects via `DASHBOARD_DATABASE_URL`, zero `ai_gateway` connection strings.
- **T-07-27** (over-broad plugins): `auth.ts` enables ONLY `emailAndPassword`.
- **T-07-28** (secrets in the client bundle): all 5 dashboard env vars are server-only (no `NEXT_PUBLIC_` prefix); `.env.example` documents them as Portainer stack vars.

## Self-Check: PASSED

All 12 spot-checked created files exist on disk; all 3 task commits (`e2fd868`, `5a9dd98`, `5b518d8`) are present in git history.
