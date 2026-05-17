# Phase 6 Wave 0 — Operator Gates

**Plan:** 06-01 (scaffolding) — Wave 0 prerequisite for plans 06-02..06-11.
**Created:** 2026-05-13
**Owner to fill:** Pedro (operator)

This file gates Phase 6 execution. Plans 06-02..06-11 MUST NOT start until
all five env-var decisions below are recorded **and** the
`VAST_AI_API_KEY` GitHub Secret confirmation step is checked off.

---

## 1. Production env-var values

For each row, the operator must pick one of:

- `default-aceito` — accept the documented Wave-0 default as production
- `override:VAL` — replace with `VAL` (e.g. `override:0.55`)
- `defer:NN` — defer to plan-NN if the choice genuinely cannot be made
  now (rare; only when downstream context is required)

| # | Env var                                | Default | Decision (`default-aceito`\|`override:VAL`\|`defer:NN`) | Notas                                                                         |
| - | -------------------------------------- | ------- | ------------------------------------------------------- | ----------------------------------------------------------------------------- |
| 1 | `VAST_PRICE_CAP_DPH`                   | `0.40`  | `default-aceito`                                        | RES Pitfall 5 — epsilon comparison `cap+0.0001` (D-A2)                        |
| 2 | `MONTHLY_EMERGENCY_BUDGET_BRL`         | `200`   | `default-aceito`                                        | D-D2 — Sentry WARNING only when crossed; **does not** auto-block provisioning |
| 3 | `USD_TO_BRL_RATE`                      | `5.0`   | `default-aceito`                                        | D-D4 — operator updates quarterly for cost audit reports                      |
| 4 | `EMERGENCY_POD_IMAGE_TAG`              | `v1.0`  | `default-aceito`                                        | Phase 1 publishes both `:v1.0` and `:latest`; pin for reproducibility         |
| 5 | `PRIMARY_HOST_ID`                      | `0`     | `default-aceito`                                        | D-A2 — `host_id != PRIMARY_HOST_ID` filter applied only when known (≠0)       |

Two additional Phase 6 timing knobs (the plan calls them out specifically
in the checkpoint description) are defaulted in `gateway/internal/config/config.go`
but operators should also confirm if these need overrides under Brazilian
business-hours expectations:

| # | Env var                                  | Default | Decision (`default-aceito`\|`override:VAL`\|`defer:NN`) | Notas                                                       |
| - | ---------------------------------------- | ------- | ------------------------------------------------------- | ----------------------------------------------------------- |
| 6 | `PROVISION_TRIGGER_FAILED_OVER_SECONDS`  | `120`   | `default-aceito`                                        | D-C1 — bate SC-1 example "e.g., 2 min"; pode encurtar (60s) sob outage crítico |
| 7 | `PROVISION_COLDSTART_BUDGET_SECONDS`     | `600`   | `default-aceito`                                        | D-A4 — bate SC-1 literal "≤10min once /health passes"       |

---

## 2. `VAST_AI_API_KEY` confirmation checklist

- [x] **GitHub Secret present:** `gh secret list -R IfixTelecom/gpu-ifix | grep VAST_AI_API_KEY` returns a row (added 2026-05-12 per CLAUDE.md token store; operator confirmed not rotated/expired 2026-05-13).
- [x] **Portainer stack env var planned:** operator will add `VAST_AI_API_KEY` to the `ai-gateway-dev` Portainer stack env vars **before** running plan 06-11 LIVE UAT (confirmed 2026-05-13).
- [ ] **Optional rotation:** skipped — operator did not flag transcript leak risk. Re-evaluate before Phase 10 GA cutover.

---

## 3. Resume signal

Type `approved` in the orchestrator chat **after** every row in section 1
has a non-`__` decision and every checkbox in section 2 is checked.
Alternatively, describe any blocker (missing key, defer needed, override
requires architectural change) and the orchestrator will route back to a
decision checkpoint.
