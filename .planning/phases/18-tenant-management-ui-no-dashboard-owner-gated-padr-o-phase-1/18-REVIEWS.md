---
phase: 18
reviewers: [gemini]
reviewed_at: 2026-07-05T22:20:55Z
plans_reviewed: [18-01-PLAN.md, 18-02-PLAN.md, 18-03-PLAN.md, 18-04-PLAN.md]
---

# Cross-AI Plan Review — Phase 18

> Reviewers tentados: gemini (✓), codex (✗ sem egress p/ api.openai.com neste host), claude (pulado — runtime próprio). 1 reviewer externo efetivo.

## Gemini Review

# Plan Review: Phase 18 — Tenant Management UI

The implementation plans for Phase 18 are **exceptionally well-structured**, demonstrating a deep understanding of the existing architectural patterns (Phase 13/17) and a proactive approach to security and data integrity. The plan successfully addresses the "CLI-only" bottleneck while maintaining the project's strict security invariants.

### Summary
Phase 18 builds the necessary Go gateway handlers and Next.js dashboard surfaces to allow owners to manage tenants and API keys through the browser. The architecture rigorously enforces owner-gating server-side, handles sensitive API key data with care (raw-once display, no hash leakage), and mirrors the proven "Action $\rightarrow$ Core $\rightarrow$ Gateway Admin API" pattern. The decision to place the `data_class` control in the API key generation flow (aligned with the authoritative `api_keys.data_class` column) is a critical win for technical accuracy.

---

### Strengths
*   **Security Alignment:** The plans prioritize the `requireOwner` check as the first operation in every server action, ensuring that UI-level hides remain strictly cosmetic and the server is the source of truth.
*   **Leak-Guard Integration:** Extending the existing `GATEWAY_ADMIN_KEY` leak-guard test to cover the generalized `gateway-admin.ts` ensures that the admin secret never accidentally enters the client-side bundle.
*   **Accurate Data Modeling:** The research correctly identified the "landmine" regarding `data_class` (key vs. tenant visibility) and placed the UI controls in the authoritative flow (Key Generation).
*   **Test-Driven Execution:** The inclusion of fake-query tests in Go and mocked server action tests in TypeScript ensures high confidence before any live UAT.
*   **Operational Readiness:** The "Gate Operacional" in Plan 18-02 to confirm `GATEWAY_BASE_URL` on the consolidated worker-vm stack prevents "ghost gateway" debugging sessions.

---

### Concerns
*   **Key Visibility Friction (LOW):** The "raw-once" display is the most secure approach but has zero recovery if the user accidentally closes the dialog or refreshes.
    *   *Mitigation:* The plan already includes a "copy now" warning and uses standard revoke/re-generate flows for recovery.
*   **Audit Metadata Verbosity (LOW):** Plan 18-03 Task 1 metadata for `tenant.create` includes `name` and `slug`.
    *   *Risk:* Low, provided the `name` doesn't contain PII, which is unlikely for a tenant slug/display name.
*   **Impact String Specificity (MEDIUM):** Plan 18-04 Task 2 mentions an impact string for revoke.
    *   *Risk:* If the string is too generic, owners might underestimate the impact on specific integrated apps (e.g., "ConverseAI will stop responding").
    *   *Mitigation:* The plan specifies using the tenant slug/prefix in the warning.

---

### Suggestions
*   **Enhanced Revoke Feedback:** In the `TenantControls` island, when a key is revoked, ensure the UI state update is immediate or uses a "revoking..." loading state to prevent double-clicks, especially since the gateway handler is idempotent.
*   **Audit UI Verification:** Ensure the `metadata` shown in the dashboard's Incident/Audit page renders the new `tenant.create` and `key.create` events cleanly (JSON-to-human mapping).
*   **Slug Validation Regex:** For `createTenantCore`, use a strict regex (e.g., `/^[a-z0-9](-?[a-z0-9])*$/`) to ensure URLs generated from slugs are clean and follow the existing CLI conventions.

---

### Risk Assessment: LOW
The phase has **Low Risk** due to:
1.  **Pattern Re-use:** It follows a 1:1 mirror of the Phase 17 (pod-config) path which is already live and stable.
2.  **No Schema Changes:** It uses pre-existing `sqlc` queries, eliminating the risk of migration failures.
3.  **Strict Isolation:** The admin API is already gated by `X-Admin-Key` and `admin.Middleware`.
4.  **Autonomous Feasibility:** Tasks 1-3 are highly automatable and verifiable with existing test harnesses.

**Verdict: Approved. Proceed to 18-01-PLAN execution.**

---

## Codex Review

Indisponível — `codex exec` não resolve `wss://api.openai.com` a partir deste host (ops-claude, sem egress OpenAI). Nenhum output produzido.

---

## Consensus Summary

Reviewer único (gemini). Veredito: **Approved — LOW risk**. Alinha com o gate independente do gsd-plan-checker (sonnet) que já rodou no plan-phase e corrigiu 2 issues (leak-guard integrity + nil-guard).

### Agreed Strengths
- `requireOwner` como 1ª operação em toda server action (UI-hide é só cosmético; server é fonte da verdade).
- Leak-guard do `GATEWAY_ADMIN_KEY` estendido ao `gateway-admin.ts` generalizado — segredo nunca entra no bundle client.
- `data_class` modelado corretamente por-KEY (fluxo de gerar key), não por-tenant (RES-08).
- Zero schema change (reusa queries sqlc) → sem risco de migration.
- Gate operacional `GATEWAY_BASE_URL` (18-02) evita "ghost gateway".

### Agreed Concerns
- **[MEDIUM] Impact-string do revoke** (18-04 T2): se genérica demais, owner subestima impacto. Mitigação já no plano: usar slug/prefix do tenant no aviso (ex. "ConverseAI vai parar de responder").
- **[LOW] Raw-key one-shot**: zero recuperação se o owner fechar o diálogo. Mitigação: aviso "copie agora" + fluxo revoke/regenerate.
- **[LOW] Audit metadata verbosity** (18-03 T1): `tenant.create` inclui name+slug — ok se sem PII (improvável em slug/display name).

### Actionable Suggestions (não-bloqueantes — considerar no execute)
1. **Slug validation regex** em `createTenantCore`: `/^[a-z0-9](-?[a-z0-9])*$/` (URL-safe, alinha convenção da CLI). — reforça V5 input validation.
2. **Loading state no revoke** (`TenantControls`): estado "revogando…" p/ evitar double-click (handler é idempotente, mas UX).
3. **Audit UI rendering**: garantir que a página de audit renderiza `tenant.create`/`key.create` (JSON→humano) limpo.

### Divergent Views
Nenhuma — reviewer único.
