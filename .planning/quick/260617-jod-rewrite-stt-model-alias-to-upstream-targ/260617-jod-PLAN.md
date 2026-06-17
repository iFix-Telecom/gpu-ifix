---
phase: quick-260617-jod
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - gateway/internal/proxy/openai_whisper_director.go
  - gateway/internal/proxy/audio.go
  - gateway/internal/proxy/dynamic_override.go
  - gateway/cmd/gateway/main.go
  - gateway/internal/proxy/stt_model_rewrite_test.go
autonomous: true
requirements: [SEED-018]
must_haves:
  truths:
    - "On the local-stt audio path, a multipart request with model=whisper is forwarded to the pod with model=Systran/faster-whisper-large-v3"
    - "On the primary-override STT path (emergency_pod_stt), a multipart request with model=whisper is forwarded to the pod with model=Systran/faster-whisper-large-v3"
    - "Audio file bytes are preserved byte-identical through both rewrite paths"
    - "openai-whisper and groq-whisper director tests still pass (no regression)"
    - "gemini-stt tier-1 path and WhisperAbortGuard duplicate-model behavior are unchanged"
  artifacts:
    - path: "gateway/internal/proxy/openai_whisper_director.go"
      provides: "canonicalAliasForUpstream gains local-stt entry; reused rewrite helper"
      contains: "\"local-stt\": \"whisper\""
    - path: "gateway/internal/proxy/dynamic_override.go"
      provides: "STT-aware override director that rewrites multipart model via resolver against local-stt"
      contains: "rewriteMultipartModelViaResolver"
    - path: "gateway/internal/proxy/stt_model_rewrite_test.go"
      provides: "Unit tests asserting model rewrite on BOTH local-stt and override paths"
      contains: "Systran/faster-whisper-large-v3"
  key_links:
    - from: "gateway/internal/proxy/audio.go"
      to: "rewriteMultipartModelViaResolver"
      via: "BuildOpenAIWhisperDirector with empty authBearer + upstreamName=local-stt"
      pattern: "BuildOpenAIWhisperDirector\\("
    - from: "gateway/cmd/gateway/main.go"
      to: "NewAudioProxy"
      via: "passes resolver into the local-stt proxy constructor"
      pattern: "NewAudioProxy\\(cfg.UpstreamSTTURL"
    - from: "gateway/cmd/gateway/main.go"
      to: "NewDynamicOverrideProxy"
      via: "STT override proxy receives resolver + resolves against local-stt"
      pattern: "emergency_pod_stt"
---

<objective>
Apply the existing multipart STT model-rewrite (currently used only by tier-1
openai-whisper / groq-whisper) to the two STT paths that still send the literal
public alias `whisper` to a Speaches upstream:

  1. the local-stt audio path (`NewAudioProxy`, used when `UPSTREAM_STT_URL` set), and
  2. the primary-override STT path (`emergency_pod_stt` via `NewDynamicOverrideProxy`).

Both must rewrite the multipart `model` form field to the resolver target for
upstream `local-stt` (`Systran/faster-whisper-large-v3`) so the pod's Speaches
returns 200 instead of 404 "Model 'whisper' is not installed".

Root cause (SEED-018): `audio.go` and `dynamic_override.go` use a plain
`BuildDirector` that preserves the multipart body untouched — they never call
the resolver. The rewrite helper `rewriteMultipartModelViaResolver` ALREADY
EXISTS in `openai_whisper_director.go` and is reused verbatim here. The
`(whisper, local-stt) → Systran/faster-whisper-large-v3` alias row exists in the
schema (migration 0029 step 3), so `resolver.Resolve("whisper", "local-stt")`
hits the schema layer and returns the correct target.

Purpose: STT works whether or not the primary pod is up — bringing the pod up no
longer regresses STT to 404.
Output: Rewrite wired on both Speaches-bound STT paths + unit tests proving it on
both, with no regression to openai-whisper / groq-whisper / gemini-stt /
WhisperAbortGuard.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@.planning/seeds/SEED-018-gateway-stt-model-not-rewritten-override-sends-literal-alias.md

<interfaces>
<!-- Contracts extracted from the codebase. Use directly — no exploration needed. -->

Resolver (internal/models/resolver.go):
  func (r *Resolver) Resolve(alias, upstream string) string
  // Precedence: (1) env-override via upstreamEnvVarMap, (2) schema row
  //   keyed (alias, upstream_name), (3) passthrough (returns alias unchanged).
  // (whisper, local-stt) → "Systran/faster-whisper-large-v3" exists in schema
  //   (migration 0029 step 3). "local-stt" is NOT in upstreamEnvVarMap, so the
  //   env layer is skipped → schema layer hits → returns the target.

Existing reusable rewrite (internal/proxy/openai_whisper_director.go):
  var canonicalAliasForUpstream = map[string]string{
      "openai-whisper": "whisper",
      "groq-whisper":   "whisper",
  }
  func BuildOpenAIWhisperDirector(upstream *url.URL, authBearer string,
      resolver *models.Resolver, upstreamName string, log *slog.Logger) func(*http.Request)
  // - authBearer=="" → SKIPS the Authorization header injection (already conditional).
  // - Only rewrites when Content-Type has prefix "multipart/form-data".
  // - Preserves audio file bytes byte-identical via io.Copy.
  func rewriteMultipartModelViaResolver(body []byte, contentType string,
      resolver *models.Resolver, upstreamName string) ([]byte, string, int, error)

Plain audio proxy (internal/proxy/audio.go):
  func NewAudioProxy(upstreamURL string, log *slog.Logger,
      interceptors ...ProxyResponseInterceptor) (*httputil.ReverseProxy, error)
  // Director: BuildDirector(u)  ← NO resolver, NO model rewrite (the bug, path 1).

Override proxy (internal/proxy/dynamic_override.go):
  func dynamicOverrideDirector(overrideURL func() (string, bool)) func(*http.Request)
      // currently: BuildDirector(u)(r)  ← NO model rewrite (the bug, path 2).
  func NewDynamicOverrideProxy(role string, overrideURL func() (string, bool),
      flushInterval time.Duration, transport *http.Transport, log *slog.Logger,
      interceptors ...ProxyResponseInterceptor) http.Handler

Wiring (internal/cmd/gateway/main.go):
  // line ~576: audioRP, err = proxy.NewAudioProxy(cfg.UpstreamSTTURL, log)   ← local-stt
  // line ~670: "emergency_pod_stt": proxy.NewDynamicOverrideProxy("stt",
  //               func() (string, bool) { return loader.Tier0OverrideURL("stt") },
  //               0, &http.Transport{...}, log)                              ← override
  // resolver is already constructed in scope (var resolver *models.Resolver).

Shared test helpers (package proxy, already defined in sibling _test.go files):
  buildMultipartBody(t, modelValues []string, fileName string, fileBytes []byte) ([]byte, string)
  parseMultipartFromBytes(body []byte, contentType string) (model string, file []byte, err error)
  applyDirector(t, director, method, path, ct, body, ...) (*http.Request, []byte)   // openai_whisper_director_test.go
  captureUpstream(t) (*httptest.Server, ...)                                          // openai_whisper_director_test.go
  discardLogger() *slog.Logger
  models.NewResolverForTesting(map[[2]string]string) *models.Resolver                 // internal/models/testing.go
</interfaces>

@gateway/internal/proxy/openai_whisper_director.go
@gateway/internal/proxy/audio.go
@gateway/internal/proxy/dynamic_override.go
@gateway/internal/proxy/openai_whisper_director_test.go
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Rewrite STT model on local-stt audio path + register local-stt alias</name>
  <files>gateway/internal/proxy/openai_whisper_director.go, gateway/internal/proxy/audio.go, gateway/cmd/gateway/main.go, gateway/internal/proxy/stt_model_rewrite_test.go</files>
  <read_first>
    - gateway/internal/proxy/openai_whisper_director.go (canonicalAliasForUpstream map L66-74; BuildOpenAIWhisperDirector L92-153 — note authBearer=="" already skips the bearer inject)
    - gateway/internal/proxy/audio.go (NewAudioProxy — Director is BuildDirector(u), the plain pass-through to replace)
    - gateway/cmd/gateway/main.go L570-581 (audioRP build site) and the `resolver := models.NewResolver(...)` construction (~L253)
    - gateway/internal/proxy/openai_whisper_director_test.go L104-180 (test pattern + shared helpers buildMultipartBody/parseMultipartFromBytes/applyDirector/captureUpstream/discardLogger)
  </read_first>
  <behavior>
    - Test: NewResolverForTesting{(whisper,local-stt):"Systran/faster-whisper-large-v3"}; a multipart body with model=whisper forwarded through the local-stt director yields forwarded model="Systran/faster-whisper-large-v3".
    - Test: audio file bytes (use a tricky payload with 0x00 / 0xff / a fake \r\n--boundary sequence) are byte-identical after rewrite.
    - Test: with an empty resolver (NewResolverForTesting(nil)), model=whisper passes through unchanged (passthrough — no crash, no auth header).
    - Test: no Authorization header is set on the forwarded request (local-stt has NO bearer; authBearer must be "").
  </behavior>
  <action>
    Add the entry "local-stt": "whisper" to canonicalAliasForUpstream in
    openai_whisper_director.go (so a missing-model multipart request still injects
    the resolved target). Update the doc comment to note local-stt is a tier-0
    Speaches upstream reusing this director with an EMPTY bearer.

    Change NewAudioProxy in audio.go to accept a `resolver *models.Resolver`
    parameter (place it after `log`, before the variadic interceptors) and set the
    proxy Director to `BuildOpenAIWhisperDirector(u, "", resolver, "local-stt", log)`
    instead of `BuildDirector(u)`. The empty authBearer means the bearer-inject is
    skipped (already conditional in BuildOpenAIWhisperDirector L102). Keep the
    existing Transport (fallthroughRoundTripper), ResponseHeaderTimeout 60s,
    ErrorHandler("stt", log), and ModifyResponse(ComposeInterceptors(...)) exactly
    as-is — do NOT touch streaming/body-cap behavior. Add the models import.

    Update the single call site in main.go (~L576) to pass `resolver`:
    `proxy.NewAudioProxy(cfg.UpstreamSTTURL, log, resolver)`. The resolver variable
    is already in scope from the boot-time construction.

    Create stt_model_rewrite_test.go in package proxy with the local-stt tests
    described in <behavior>, mirroring the structure of
    TestOpenAIWhisperDirector_RewritesModelInMultipart (Setenv
    UPSTREAM_STT_OPENAI_MODEL="" is NOT needed here — local-stt is not in
    upstreamEnvVarMap — but DO set no STT env). Reuse buildMultipartBody /
    parseMultipartFromBytes / applyDirector / captureUpstream / discardLogger.

    DO NOT change BuildDirector, the openai-whisper or groq-whisper directors,
    rewriteMultipartModelViaResolver, WhisperAbortGuard, the dispatcher, or
    maxSTTBodyBuffer / prepareReplayBody.
  </action>
  <verify>
    <automated>cd gateway && go build ./... && go test ./internal/proxy/ -run 'STT|Whisper' -count=1</automated>
  </verify>
  <done>
    canonicalAliasForUpstream has the local-stt entry; NewAudioProxy rewrites
    model=whisper → Systran/faster-whisper-large-v3 with byte-identical audio and
    no Authorization header; main.go passes resolver; new local-stt tests pass;
    openai-whisper / groq-whisper tests still pass.
  </done>
  <acceptance_criteria>
    - go build ./... exits 0.
    - A unit test asserts the local-stt director rewrites model=whisper to
      Systran/faster-whisper-large-v3 AND preserves audio bytes byte-identical.
    - A unit test asserts no Authorization header is set on the local-stt path.
    - All existing TestOpenAIWhisperDirector_* tests still pass.
  </acceptance_criteria>
</task>

<task type="auto" tdd="true">
  <name>Task 2: Rewrite STT model on the primary-override path (emergency_pod_stt)</name>
  <files>gateway/internal/proxy/dynamic_override.go, gateway/cmd/gateway/main.go, gateway/internal/proxy/stt_model_rewrite_test.go</files>
  <read_first>
    - gateway/internal/proxy/dynamic_override.go (dynamicOverrideDirector L28-40; NewDynamicOverrideProxy L47-57 — note role is the only discriminator)
    - gateway/cmd/gateway/main.go L667-674 (sttRoleProxies["emergency_pod_stt"] build site) and the llm override at L637-642 (do NOT alter the llm override)
    - gateway/internal/proxy/openai_whisper_director.go (rewriteMultipartModelViaResolver reuse + the multipart-prefix guard pattern at L106-109)
  </read_first>
  <behavior>
    - Test: an STT override director built with resolver{(whisper,local-stt):"Systran/faster-whisper-large-v3"} and a live overrideURL forwards model=whisper as model=Systran/faster-whisper-large-v3 (resolved against "local-stt", NOT against any synthetic override name).
    - Test: audio bytes byte-identical through the override path.
    - Test: a NON-multipart request (e.g. JSON body, defensive) passes through the override director untouched.
    - Test (regression guard): the llm override path (role="llm") does NOT attempt multipart STT rewrite — its director is unchanged for JSON chat bodies.
  </behavior>
  <action>
    Make the override director STT-aware WITHOUT changing the llm/tts override
    behavior. Add an optional resolver-driven STT rewrite to dynamic_override.go:

    Introduce a new constructor variant (e.g.
    `NewDynamicOverrideSTTProxy(overrideURL func() (string, bool), flushInterval
    time.Duration, transport *http.Transport, resolver *models.Resolver, log
    *slog.Logger, interceptors ...ProxyResponseInterceptor) http.Handler`) whose
    Director first resolves the override target host via the same per-request
    `overrideURL()` + `url.Parse` + `BuildDirector(u)(r)` sequence as
    dynamicOverrideDirector, THEN — when Content-Type has prefix
    "multipart/form-data" and r.Body != nil — reads the body and calls
    `rewriteMultipartModelViaResolver(body, ct, resolver, "local-stt")`, applying
    the SAME success / parse-error / duplicate-400 switch that
    BuildOpenAIWhisperDirector uses (on parse error or duplicate, forward the
    ORIGINAL body via rewriteRequestBody — never 500; the dispatcher's
    WhisperAbortGuard is not on this path, so duplicate just forwards unchanged
    and the pod rejects). role label for ErrorHandler is "stt".

    Rationale for upstreamName="local-stt": the override pod runs the SAME Speaches
    that local-stt points at; the synthetic override name (emergency_pod_stt) is
    not in model_aliases, so resolving against it would miss → passthrough →
    literal "whisper" → 404. Resolving against "local-stt" hits the schema alias
    row (migration 0029) and yields Systran/faster-whisper-large-v3.

    Keep the existing NewDynamicOverrideProxy untouched (llm + tts override paths
    keep using it). Only the STT override in main.go switches constructors.

    Update main.go sttRoleProxies["emergency_pod_stt"] (~L670) to call the new
    STT constructor, passing `resolver` and keeping the same overrideURL closure
    (loader.Tier0OverrideURL("stt")), flushInterval 0, and the same Transport
    (MaxIdleConns 20 / per-host 4 / IdleConnTimeout 90s / ResponseHeaderTimeout
    60s). Do NOT touch the llm override (L637) or tts override (L741).

    Add the override-path tests from <behavior> to stt_model_rewrite_test.go.
    Use captureUpstream to get a live URL and pass `func() (string,bool){ return
    srv.URL, true }` as overrideURL.

    DO NOT change: breaker logic, dispatcher.go, maxSTTBodyBuffer /
    prepareReplayBody, gemini-stt proxy, WhisperAbortGuard, the llm/tts override
    constructors.
  </action>
  <verify>
    <automated>cd gateway && go build ./... && go test ./internal/proxy/ ./internal/models/ -count=1</automated>
  </verify>
  <done>
    The STT override path rewrites model=whisper → Systran/faster-whisper-large-v3
    by resolving against "local-stt"; audio bytes byte-identical; non-multipart
    bodies pass through; llm/tts override paths unchanged; new override tests pass.
  </done>
  <acceptance_criteria>
    - go build ./... exits 0.
    - A unit test asserts the STT override director rewrites model=whisper to
      Systran/faster-whisper-large-v3 (resolved against local-stt) AND preserves
      audio bytes.
    - A unit test asserts a non-multipart override request is forwarded untouched.
    - go test ./internal/proxy/ ./internal/models/ passes (openai-whisper,
      groq-whisper, gemini-stt, WhisperAbortGuard, resolver tests all green).
  </acceptance_criteria>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| client → gateway STT | Untrusted multipart audio + model field crosses here; bytes must be preserved, never decoded as text |
| gateway → Speaches pod | Gateway rewrites the model alias to the upstream-specific target; must not leak client auth, must not corrupt audio |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-jod-01 | Tampering | multipart rewrite (audio bytes) | mitigate | Reuse rewriteMultipartModelViaResolver which streams file parts via io.Copy (byte-identical); unit test asserts byte-equality on a tricky payload (0x00/0xff/boundary-like) |
| T-jod-02 | Information Disclosure | override director forwarding client Authorization to pod | mitigate | BuildDirector strips client auth; local-stt path uses empty authBearer so no bearer is injected; override path reuses BuildDirector before rewrite |
| T-jod-03 | Denial of Service | unbounded body read in override director | accept | Request bodies are capped at the server level (25 MiB http.MaxBytesHandler) and by dispatcher maxSTTBodyBuffer; the director reads an already-capped body |
| T-jod-04 | Tampering | resolver miss silently sending literal alias | mitigate | (whisper,local-stt) alias row exists in schema (migration 0029); on miss the helper forwards unchanged and the pod 4xx's (breaker classifies 4xx as non-failure) — no gateway 500 |
| T-jod-SC | Tampering | npm/pip/cargo installs | mitigate | No new dependencies added; pure stdlib (mime/multipart, io) reused from existing helper |
</threat_model>

<verification>
- `cd gateway && go build ./...` exits 0.
- `cd gateway && go test ./internal/...` exits 0.
- New tests prove model rewrite (whisper → Systran/faster-whisper-large-v3) on
  BOTH the local-stt path AND the emergency_pod_stt override path, with
  byte-identical audio.
- Existing openai-whisper / groq-whisper / gemini-stt / WhisperAbortGuard /
  resolver tests remain green (no regression).
</verification>

<success_criteria>
- Both Speaches-bound STT paths (local-stt + primary override) rewrite the
  multipart model field to Systran/faster-whisper-large-v3.
- Audio file bytes preserved byte-identical on both paths.
- No change to breaker logic, dispatcher body-cap/replay, gemini-stt fallback,
  or duplicate-model WhisperAbortGuard behavior.
- `cd gateway && go build ./... && go test ./internal/...` exits 0.
</success_criteria>

<output>
Create `.planning/quick/260617-jod-rewrite-stt-model-alias-to-upstream-targ/260617-jod-SUMMARY.md` when done.
Deploy (develop → build → GHCR → pull) is OUT OF SCOPE — stop at build+test green.
</output>
