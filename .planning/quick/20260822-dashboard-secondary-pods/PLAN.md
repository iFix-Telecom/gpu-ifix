---
phase: quick-20260822-dashboard-secondary-pods
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - gateway/internal/emerg/vast/types.go
  - gateway/internal/emerg/vast/client.go
  - gateway/internal/emerg/vast/client_test.go
  - gateway/internal/admin/operations.go
  - gateway/internal/admin/operations_test.go
  - gateway/cmd/gateway/main.go
  - dashboard/src/lib/gateway.ts
  - dashboard/src/lib/format.ts
  - dashboard/src/components/operacao-secondary-pods-panel.tsx
  - dashboard/src/app/(dashboard)/operacao/page.tsx
autonomous: true
requirements: [DASH-SECPODS-01]
must_haves:
  truths:
    - "The Operação page renders a section listing every Vast instance on the account EXCEPT the active primary."
    - "The 3060 STT/TTS pod (instance 48294952, managed externally by vast3060.py) appears in that section today."
    - "Any future pod added to the Vast account appears automatically with no code change."
    - "The section is read-only — no start/stop/destroy controls."
    - "The active primary pod (FSM ActiveInstanceID) is filtered OUT of the list."
    - "A Vast API failure or nil client never breaks the /admin/operations response — the section is served empty."
  artifacts:
    - path: "gateway/internal/emerg/vast/client.go"
      provides: "ListInstances(ctx) ([]Instance, error) against GET /instances/"
      contains: "func (c *Client) ListInstances"
    - path: "gateway/internal/admin/operations.go"
      provides: "SecondaryPods section on OperationsResponse, populated + primary-filtered + 60s cached"
      contains: "SecondaryPods"
    - path: "dashboard/src/components/operacao-secondary-pods-panel.tsx"
      provides: "Outros pods card rendering secondary_pods with empty state"
      contains: "Outros pods"
    - path: "dashboard/src/lib/gateway.ts"
      provides: "OperationsSecondaryPod type + secondary_pods on OperationsResponse"
      contains: "secondary_pods"
  key_links:
    - from: "gateway/internal/admin/operations.go"
      to: "vast.Client.ListInstances"
      via: "vastLister interface field on OperationsHandler"
      pattern: "ListInstances"
    - from: "dashboard/src/app/(dashboard)/operacao/page.tsx"
      to: "OperacaoSecondaryPodsPanel"
      via: "query.data.secondary_pods prop"
      pattern: "secondary_pods"
---

<objective>
The ai-dashboard "Operação" page shows only the PRIMARY pod (the 3090 LLM managed by the gateway FSM). A second real pod — the 3060 STT/TTS, managed by an EXTERNAL script (ops/vast-3060/vast3060.py, instance 48294952) — is invisible. This plan makes the dashboard show ALL Vast instances on the account except the active primary, read-only, so the 3060 shows today AND any future pod appears automatically.

Purpose: answer the user's "dashboard vê os 2 pods? e se eu inserir mais?" — yes, and yes automatically.
Output: a new Vast `ListInstances` client method, a `secondary_pods` section on `/admin/operations` (primary-filtered, 60s cached, nil/failure-safe), and an "Outros pods" card on the Operação page.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@CLAUDE.md

<interfaces>
<!-- Extracted from the codebase. Use these directly — no exploration needed. -->

Vast Instance struct — gateway/internal/emerg/vast/types.go (already parses these fields):
```go
type Instance struct {
    ID             int64                    `json:"id"`
    ActualStatus   string                   `json:"actual_status"`
    IntendedStatus string                   `json:"intended_status"`
    SshHost        string                   `json:"ssh_host"`
    SshPort        int                      `json:"ssh_port"`
    PublicIPAddr   string                   `json:"public_ipaddr"`
    MachineID      int64                    `json:"machine_id"`
    HostID         int64                    `json:"host_id"`
    DphTotal       float64                  `json:"dph_total"`
    ImageUUID      string                   `json:"image_uuid"`
    Label          string                   `json:"label"`
    Ports          map[string][]PortBinding `json:"ports"`
    StatusMsg      string                   `json:"status_msg"`
}
```
MISSING fields the dashboard needs (add them): `GpuName string json:"gpu_name"`, `NumGpus int json:"num_gpus"`, `StartDate float64 json:"start_date"` (Vast returns epoch seconds as a float).

GetInstance decode pattern (client.go:296-340) — `GET /instances/{id}/` returns `{"instances": {...single object...}}`. The LIST endpoint `GET /instances/` returns `{"instances": [ ...array... ]}` under the SAME key. Mirror SearchOffers' decode-into-array pattern (client.go:224-233), not GetInstance's peek-message.

SearchOffers decode shape to mirror (client.go:200-234):
```go
var body struct { Offers []Offer `json:"offers"` }
// ...decode, nil→empty slice
```

OperationsHandler (operations.go:118-165) — fields q, breakers, rec (*primary.Reconciler, nil-safe), emergFSM, podCfg, cfg, log. Two constructors: NewOperationsHandler (prod, takes *gen.Queries) and newOperationsHandlerWithQueries (test, takes operationsQueries iface). Both must gain the new vastLister param.

fsmSection already reads the active primary id (operations.go:281-294):
```go
snap := h.rec.Snapshot()
sec.ActiveInstanceID = snap.ActiveInstanceID   // this is the primary to filter OUT
```

cfg.USDToBRLRate is the USD→BRL forex already used server-side for cost (operations.go:240). dph_total from Vast is USD/h.

main.go wiring (cmd/gateway/main.go:1340):
```go
adminOperationsHandler := admin.NewOperationsHandler(gen.New(pool), breakerSet, primaryReconciler, emergFSM, podCfgLoader, cfg, log)
```
Vast enablement gate elsewhere in main.go: `if cfg.VastAIAPIKey == "" { ...disabled... }`. `vast.NewClient(cfg.VastAIAPIKey)` is the constructor (see main.go:839, 1017).

Dashboard OperationsResponse TS (gateway.ts:197-264) — field-for-field mirror of the Go struct. formatBrl exists (format.ts:25). Card/CardHeader/CardTitle/CardContent from @/components/ui/card, Badge from @/components/ui/badge. FSM panel (operacao-fsm-panel.tsx) is the styling reference: Field helper, badge classes, grid layout. Page fetches via fetchOperations + react-query, renders panels in order (page.tsx:65-75).
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Vast client ListInstances + Instance fields</name>
  <files>gateway/internal/emerg/vast/types.go, gateway/internal/emerg/vast/client.go, gateway/internal/emerg/vast/client_test.go</files>
  <behavior>
    - GET /instances/ returning {"instances":[{id,gpu_name,num_gpus,actual_status,label,dph_total,start_date}, ...]} → ListInstances returns the parsed slice, len matches, fields populated.
    - {"instances":[]} or missing key → returns empty non-nil slice, nil error (mirror SearchOffers nil→empty).
    - Non-200 → returns c.parseErrorBody(resp) error (mirror GetInstance).
    - Auth header carries Bearer key on the wire (existing TestClientAuthHeader posture — no new key-leak surface).
  </behavior>
  <action>
    In types.go add three fields to the Instance struct: `GpuName string json:"gpu_name"`, `NumGpus int json:"num_gpus"`, `StartDate float64 json:"start_date"` (Vast returns start_date as epoch seconds, a float). Do not touch existing fields or methods.

    In client.go add `ListInstances(ctx context.Context) ([]Instance, error)` = `GET {baseURL}/instances/`. Copy the exact shape of GetInstance for request build, setAuthHeader, obs.GatewayVastAPIRequestsTotal metric labels (use verb "list"), defer resp.Body.Close, and the non-200 → parseErrorBody branch. For the 200 body, decode into `struct { Instances []Instance json:"instances" }` (the list endpoint returns an ARRAY under the "instances" key — do NOT reuse GetInstance's single-object peek-message). If the decoded slice is nil, return an empty non-nil slice (mirror SearchOffers client.go:230-232). Add a doc comment matching the file's style noting this is the account-wide instance list used by the dashboard secondary-pods panel (read-only, no lifecycle side effects).

    In client_test.go add a test that mirrors TestSearchOffers_HappyPath: newTestServer serving `/instances/` with a 2-instance array fixture (include gpu_name, num_gpus, actual_status, label, dph_total, start_date), assert len==2 and the new fields parse. Add a second test for the empty-array case asserting a non-nil empty slice.
  </action>
  <verify>
    <automated>cd gateway && gofmt -l internal/emerg/vast/ && go test ./internal/emerg/vast/...</automated>
  </verify>
  <done>ListInstances exists and is unit-tested against httptest fixtures; Instance parses gpu_name/num_gpus/start_date; gofmt clean; vast package tests green.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: /admin/operations secondary_pods section (filtered, cached, nil-safe)</name>
  <files>gateway/internal/admin/operations.go, gateway/internal/admin/operations_test.go, gateway/cmd/gateway/main.go</files>
  <behavior>
    - vastLister returning 2 instances (one whose ID == rec.Snapshot().ActiveInstanceID, one not) → SecondaryPods has EXACTLY the non-primary one.
    - nil vastLister → SecondaryPods is an empty (non-nil) slice; the rest of the response is unchanged and still 200.
    - vastLister returning an error → SecondaryPods empty, response still 200 (never a 5xx from this section), a WARN is logged.
    - dph_brl == dph_total(USD) * cfg.USDToBRLRate; uptime_seconds == now - start_date, clamped >= 0.
  </behavior>
  <action>
    Add `SecondaryPods []SecondaryPodRow json:"secondary_pods"` to OperationsResponse (after VastCost). Define `SecondaryPodRow` with json fields: `id` (int64), `gpu_name` (string), `num_gpus` (int), `status` (string, from ActualStatus), `label` (string), `dph_brl` (float64), `uptime_seconds` (int64). DESIGN DECISION: expose the cost pre-converted to BRL server-side (`dph_brl = DphTotal * cfg.USDToBRLRate`) so the whole operations response stays BRL-consistent (matches VastCostSection which is BRL-only) and the dashboard reuses formatBrl — do NOT ship raw USD to the client.

    Define a minimal interface in this package: `type vastLister interface { ListInstances(ctx context.Context) ([]vast.Instance, error) }` (import gateway/internal/emerg/vast — no cycle; admin already imports internal/emerg). Add fields to OperationsHandler: `vast vastLister` and a 60s cache guarded by sync.Mutex — `mu sync.Mutex`, `secCacheAt time.Time`, `secCache []vast.Instance` (add "sync" to imports). Add the `vast` param to BOTH NewOperationsHandler and newOperationsHandlerWithQueries (place it after emergFSM/podCfg — pick one consistent position and update all call sites) and assign it.

    Add a method `secondaryPods(ctx, activeInstanceID int64) []SecondaryPodRow`: return `[]SecondaryPodRow{}` immediately if `h.vast == nil`. Otherwise, under the mutex, serve `h.secCache` when `time.Since(h.secCacheAt) < 60s`; on cache-miss call `h.vast.ListInstances(ctx)` — on error, log WARN and fall back to the last cached slice (or empty), NEVER fail; on success store the slice + timestamp. Then build rows: skip any instance whose ID == activeInstanceID (the primary), map the rest to SecondaryPodRow with `dph_brl = inst.DphTotal * h.cfg.USDToBRLRate` and `uptime_seconds = max(0, now.Unix() - int64(inst.StartDate))` (0 when StartDate is 0). Return a non-nil slice. Mirror the existing defensive posture (schedule parse-error path operations.go:305-320): a data-source problem degrades this ONE section, never the whole /admin/operations response.

    In ServeHTTP, populate `resp.SecondaryPods = h.secondaryPods(ctx, resp.FSM.ActiveInstanceID)` after fsmSection is built (fsmSection already surfaces the active primary id). Keep the existing 2xx metric increment.

    In operations_test.go add a fake implementing vastLister (returns a fixed []vast.Instance) and a test: build a rec via NewReconcilerFull whose Snapshot().ActiveInstanceID matches one fixture instance (reuse the TestOperationsHandler_ScheduleFromLiveRule reconciler-construction pattern), assert SecondaryPods len==1 and it's the non-primary id, and assert dph_brl/uptime_seconds. Add a nil-vast test asserting empty SecondaryPods + 200. Update every existing newOperationsHandlerWithQueries call site in this test file to pass the new param (nil).

    In cmd/gateway/main.go update the NewOperationsHandler call (line ~1340): pass a Vast lister — `vast.NewClient(cfg.VastAIAPIKey)` when `cfg.VastAIAPIKey != ""`, else nil (declare `var opsVastLister admin.vastLister`... note: vastLister is unexported — instead pass a `*vast.Client` or nil typed value; the param type on the exported constructor must be an EXPORTED interface OR *vast.Client). DESIGN DECISION: make the constructor param type the exported concrete need explicit — declare the interface EXPORTED as `VastLister` in operations.go (so main.go can name the nil type) OR simply type the constructor param as the interface and pass `vast.NewClient(...)` / a nil `*vast.Client`. Prefer: keep interface unexported `vastLister`, type the NewOperationsHandler param as `vastLister`, and in main.go pass either `vast.NewClient(cfg.VastAIAPIKey)` (a *vast.Client satisfies it) or a nil `*vast.Client` cast — build a small local: `var opsVast *vast.Client; if cfg.VastAIAPIKey != "" { opsVast = vast.NewClient(cfg.VastAIAPIKey) }` then pass `opsVast`. Confirm main.go already imports the vast package (it does — main.go:839).
  </action>
  <verify>
    <automated>cd gateway && gofmt -l internal/admin/ cmd/gateway/ && go build ./... && go test ./internal/admin/...</automated>
  </verify>
  <done>OperationsResponse carries secondary_pods; the active primary is filtered out; nil/error vast never 5xxs the endpoint; 60s cache in place; dph_brl + uptime_seconds computed; main.go wires a real client when VAST_AI_API_KEY set; gofmt clean, build + admin tests green.</done>
</task>

<task type="auto">
  <name>Task 3: Dashboard "Outros pods" section + TS type</name>
  <files>dashboard/src/lib/gateway.ts, dashboard/src/lib/format.ts, dashboard/src/components/operacao-secondary-pods-panel.tsx, dashboard/src/app/(dashboard)/operacao/page.tsx</files>
  <action>
    In gateway.ts add an interface mirroring the Go SecondaryPodRow FIELD-FOR-FIELD: `OperationsSecondaryPod { id: number; gpu_name: string; num_gpus: number; status: string; label: string; dph_brl: number; uptime_seconds: number; }` with a doc comment pointing at admin.SecondaryPodRow. Add `secondary_pods: OperationsSecondaryPod[];` to the OperationsResponse interface (after vast_cost).

    In format.ts add `formatUptime(seconds: number): string` — humanize to pt-BR compact form: `<60s → "Ns"`, `<3600 → "Nm"`, `<86400 → "Nh Mm"`, else `"Nd Mh"`. Guard negatives/NaN → "—".

    Create operacao-secondary-pods-panel.tsx (client component, styling copied from operacao-fsm-panel.tsx): a Card titled "Outros pods". Empty array → render the empty state text "Nenhum outro pod ativo." Otherwise render each pod as a row/card showing: GPU as `{gpu_name} ×{num_gpus}`, a status Badge (reuse a status→class mapping like primaryStateClass — map running→green, loading/scheduling→amber, exited/offline/unknown→muted/destructive; keep it local to this file), label (or "—"), custo as `formatBrl(dph_brl)` + "/h", and uptime via formatUptime(uptime_seconds). Use the Field helper pattern from the FSM panel for label/value pairs. Include the id somewhere subtle (e.g. small muted text) so operators can cross-reference Vast.

    In operacao/page.tsx import and render `<OperacaoSecondaryPodsPanel pods={query.data.secondary_pods} />` in the success branch, after OperacaoCostPanel (so it sits with the cost/pod info group).
  </action>
  <verify>
    <automated>cd dashboard && npx tsc --noEmit && (npm run lint --if-present || true)</automated>
  </verify>
  <done>Operação page renders an "Outros pods" section listing secondary_pods with GPU/status/label/custo/uptime and an empty state; TS type mirrors the Go struct; tsc clean.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| gateway → Vast API | outbound authenticated call (Bearer VAST_AI_API_KEY); Vast response is untrusted input decoded into Instance |
| dashboard → gateway /admin/operations | admin-key-gated read; response rendered in the operator UI |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-secpods-01 | Information Disclosure | ListInstances / secondary_pods | mitigate | Project only non-secret fields (id, gpu_name, num_gpus, status, label, dph_brl, uptime) into SecondaryPodRow — no ssh_host/ports/image_uuid/api-key surfaced; VAST_AI_API_KEY stays in setAuthHeader only (existing TestClientNeverLogsAPIKey guard unchanged) |
| T-secpods-02 | Denial of Service | /admin/operations polled every ~10s hitting Vast | mitigate | 60s in-handler cache on ListInstances; on Vast error serve last-good/empty and never block or 5xx the endpoint |
| T-secpods-03 | Tampering | malformed/oversized Vast list body | accept | admin-key-gated read of first-party Vast API; JSON decode of a bounded operator-owned account; no package installs, no new untrusted input path beyond existing Vast client |
</threat_model>

<verification>
- `cd gateway && gofmt -l . ` prints nothing (empty).
- `cd gateway && go build ./...` succeeds.
- `cd gateway && go test ./internal/emerg/vast/... ./internal/admin/...` green (CI runs integration tags).
- `cd dashboard && npx tsc --noEmit` clean; `npm run lint` clean if present.
- Manual read: the active primary instance id (FSM ActiveInstanceID) does NOT appear in secondary_pods; instance 48294952 (3060) does.
</verification>

<success_criteria>
- The Operação page shows an "Outros pods" section that lists every non-primary Vast instance on the account.
- The 3060 STT/TTS pod appears today; any future pod appears with no code change.
- Section is read-only; no control affordances.
- A Vast API failure or missing VAST_AI_API_KEY leaves /admin/operations 200 with an empty secondary_pods.
- Conventional commit(s) mentioning "dashboard secondary pods"; committed, NOT pushed.
</success_criteria>

<output>
Create `.planning/quick/20260822-dashboard-secondary-pods/SUMMARY.md` when done.
</output>
