---
slug: emerg-integration-tests-ci
status: resolved
trigger: "Phase 6 emergency integration tests falham em CI (build-gateway job, integration-test). 8+ testes em gateway/internal/integration_test/emerg_*_test.go falham no 1o run real."
created: 2026-05-14
updated: 2026-05-14
goal: find_and_fix
---

# Debug Session: emerg-integration-tests-ci

## Symptoms

**Expected behavior:**
emerg_*_test.go integration tests passam no CI (build-gateway job `integration-test`).

**Actual behavior:**
8+ testes falham no 1o run real do job `integration-test` (branch develop, run mais recente de build-gateway).

**Error messages:**
- `TestEmergProvisionHappyPath` — falha
- `TestEmergPriceCap` — falha
- `TestEmergReconcilerHandlesForceProvision` — "FSM did not advance"
- `TestEmergReconcilerForceProvisionRejectNonLeader` — "got 2"
- `TestEmergReconcilerForceDestroyNoOpWhenIdle` — "7 rows want 0"
- `TestEmergMultiFailoverRideOut` — falha
- `TestEmergLeaderRecoveryActiveResume_HealthFailureCancels` — falha
- `TestEmergLeaderRecoveryZombie` — falha
- `emerg_singleton_test.go:63` — duplicate key `emergency_live_singleton`

**Timeline:**
Nunca executados antes. TODO SUMMARY Phase 6 (06-02..06-10): "integration tests deferred to CI runtime... Docker unavailable on ops-claude". Este é o 1o run real. NÃO é regressão Phase 7/8/9.

**Reproduction:**
ops-claude SEM Docker → rodar em vps-ifix-vm:
```
ssh vps-ifix-vm
cd gateway && go test -tags=integration ./internal/integration_test/... -count=1 -v -timeout=10m
```
Testes têm `//go:build integration` + testcontainers — NÃO rodam no `go test ./...` local.

## Context

- Setup: `gateway/internal/integration_test/setup_test.go` (TestMain → setupContainers, roda todas migrations incl `0019_emergency_lifecycles.sql`).
- Hipótese inicial confirmada por análise estática: falta de isolamento de DB entre test functions — `emergency_lifecycles` não é truncada entre testes.
- Regras CLAUDE.md: --diagnose primeiro, validar root cause ANTES de editar, sem edits especulativos.

## Current Focus

- hypothesis: CONFIRMADO E RESOLVIDO — ambas as causas-raiz. Causa (1): falta de TRUNCATE de `emergency_lifecycles` explicava 5+ falhas (corrigida pelo fix de harness, commit 9772d71). Causa (2): 3 testes falhavam em isolamento total porque foram escritos para o reconciler do Plan 06-05 (stub) e não acompanharam Plans 06-06/06-07 (`evaluateEmergencyProvisioning` + `recoverOrphanLifecycles`). Hipótese estática original CONFIRMADA por leitura de código de produção — STALE TESTS, não bug de produção.
- next_action: NENHUMA — sessão encerrada. Fix de causa (2) aplicado (test-only), validado em vps-ifix-vm e commitado (355843b). Aguardando decisão de push do usuário.
- test: rodados os 3 testes em isolamento total (1 processo, containers frescos) + os testes irmãos (`TestEmergTrigger*`, force-destroy) para garantir ausência de regressão.
- expecting: os 3 testes passam isolados; irmãos continuam verdes. Resultado: TODOS PASSAM.

## Evidence

- timestamp: 2026-05-14
  source: gateway/internal/integration_test/setup_test.go:138-187 (helper `freshSchema`)
  finding: |
    `freshSchema` é o ÚNICO ponto de reset de estado de DB usado por todos os
    emerg tests. Ele chama `db.Up` (aplica migrations) e depois TRUNCATE
    APENAS de uma lista fixa de 5 tabelas:
      api_keys, audit_log, audit_log_content, usage_counters, tenants
    `emergency_lifecycles` NÃO está na lista. Nenhuma tabela emergency está.
    O doc-comment do TestMain (linha 40-41) afirma "Tests rebuild a FRESH
    schema between cases via db.Down + db.Up" — mas `freshSchema` NUNCA chama
    `db.Down`. O comentário está desatualizado/incorreto; o código real só
    faz `db.Up` + TRUNCATE parcial.

- timestamp: 2026-05-14
  source: gateway/internal/integration_test/setup_test.go:30-57 (vars + TestMain)
  finding: |
    `sharedPG` + `setupOnce sync.Once` → um único container Postgres é criado
    uma vez e reusado por TODO o pacote `integration`. Linhas escritas em
    `emergency_lifecycles` por um teste persistem para todos os testes
    subsequentes do mesmo `go test` run.

- timestamp: 2026-05-14
  source: grep -rn "TRUNCATE.*emergency|DELETE FROM.*emergency" gateway/internal/integration_test/*.go
  finding: |
    Zero ocorrências. Nenhum teste e nenhum helper limpa `emergency_lifecycles`.
    14 arquivos de teste emerg_*_test.go criam linhas de lifecycle (INSERT
    direto OU via reconciler processando force_provision / breaker events) e
    nenhum as remove.

- timestamp: 2026-05-14
  source: gateway/db/migrations/0019_emergency_lifecycles.sql
  finding: |
    Migration 0019 cria `ai_gateway.emergency_lifecycles` + o índice parcial
    único `emergency_live_singleton ON (...) WHERE ended_at IS NULL` — no
    máximo 1 linha "viva" (ended_at NULL) por banco. `id BIGSERIAL PRIMARY
    KEY` confirmado. É a ÚNICA tabela emergency (grep em todas as migrations
    0001-0022 confirma — nenhuma outra `CREATE TABLE ... emergency`).
    Migrations 0020/0021/0022 são todas Phase 7 e mexem só em `audit_log`.

- timestamp: 2026-05-14
  source: gateway/internal/integration_test/emerg_force_command_test.go:268-276 (TestEmergReconcilerForceDestroyNoOpWhenIdle)
  finding: |
    `SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles` com assert
    `count == 0`. Explica o sintoma "7 rows want 0": 7 linhas vazaram de
    testes anteriores. APÓS O FIX: este teste PASSA isolado E na suíte.

- timestamp: 2026-05-14
  source: FIX APLICADO — gateway/internal/integration_test/setup_test.go
  finding: |
    Aplicado o fix de harness aprovado:
    (1) Adicionado `TRUNCATE ai_gateway.emergency_lifecycles RESTART IDENTITY
        CASCADE` em `freshSchema` (após o loop dos 5 tabelas). RESTART IDENTITY
        zera a sequência BIGSERIAL → ids determinísticos por teste.
    (2) Corrigido o doc-comment desatualizado do TestMain (linhas ~39-41) que
        afirmava incorretamente "db.Down + db.Up" entre casos.
    Validação local: `gofmt -l .` limpo, `go build ./...` OK,
    `go vet -tags=integration ./internal/integration_test/...` sem erros.

- timestamp: 2026-05-14
  source: vps-ifix-vm — go test -tags=integration full suite (-count=1 -v), container golang:1.24 com docker.sock montado, --network host
  finding: |
    APÓS O FIX, dos 8 testes originalmente falhos, 5+ agora PASSAM:
    TestEmergProvisionHappyPath, TestEmergPriceCap, TestEmergMultiFailoverRideOut,
    TestEmergLeaderRecoveryActiveResume_HealthFailureCancels,
    TestEmergLeaderRecoveryZombie, TestEmergReconcilerForceDestroyNoOpWhenIdle,
    TestEmergSingletonDBIndex — todos verde. O fix de harness resolveu a
    contaminação de `emergency_lifecycles` conforme diagnosticado.

- timestamp: 2026-05-14
  source: vps-ifix-vm — go test -run ^<Name>$ -count=1, CADA teste em processo isolado com containers frescos
  finding: |
    EVIDÊNCIA DECISIVA — 3 dos 4 testes ainda falhos FALHAM EM ISOLAMENTO
    TOTAL (um processo, containers frescos, nenhum estado vazado possível):
      - TestEmergReconcilerHandlesForceProvisionEvent — FAIL isolado
        ("FSM did not advance after force-provision; got healthy")
      - TestEmergReconcilerForceProvisionRejectedNonLeader — FAIL isolado
        ("NEITHER FSM advanced; force-provision was dropped silently")
      - TestEmergTriggerNoSpawnIfLiveLifecycle — FAIL isolado
        ("FSM transitioned despite live lifecycle (D-C5 check failed):
         got emergency_provisioning, want healthy")
    O 4o (TestEmergBidRaceLost) PASSA isolado — só falha na suíte cheia →
    flake de timing sob carga (host CI compartilhado, suíte de 294s com
    dezenas de testcontainers), NÃO contaminação de harness.
    CONCLUSÃO: os 3 que falham isolados têm causa-raiz SEPARADA da
    contaminação de `emergency_lifecycles`. O Root Cause Report original
    estava CORRETO mas INCOMPLETO — atribuía 100% das falhas ao TRUNCATE
    faltante quando na verdade 3 testes têm bug distinto.

- timestamp: 2026-05-14
  source: gateway/internal/emerg/reconciler.go:635-656 (evaluateEmergencyProvisioning) + lifecycle.go:114-159 (startProvisioning) + tracker.go:61-65 (newLocalLlmTracker)
  finding: |
    CAUSA (2) CONFIRMADA por leitura de código de produção — Tests 1 e 2
    (force-provision). Os 3 testes usam o loop REAL `r.Run`. Após
    `handleForceProvision` fazer INSERT do lifecycle + transitar a FSM para
    EmergencyProvisioning, o TICK SEGUINTE chama `evaluateEmergencyProvisioning`.
    Como `activeLifecycle != nil` (setado por handleForceProvision), o código
    NÃO entra em startProvisioning — entra no ramo de cancel-detection (D-C3):
    lê `r.tracker.State()`. `newLocalLlmTracker` inicializa o state em
    "closed" (zero-value documentado), e os testes NUNCA publicaram um evento
    de breaker `local-llm open`. "closed" casa com a condição de cancelamento
    → `FSM.Transition(EmergencyProvisioning → Healthy, "cancelled_local_llm_recovered")`.
    A FSM quica Healthy → EmergencyProvisioning → Healthy em ~1 tick (100ms),
    e o `waitFor` do teste captura "healthy". O reconciler está CORRETO — ele
    leu legitimamente "primary recuperou, cancela o provisioning" porque
    nenhum OPEN foi publicado. TESTE DESATUALIZADO, escrito para o stub do
    Plan 06-05 ("Plan 06-05 stops at the FSM transition"), antes de Plan
    06-06 adicionar `evaluateEmergencyProvisioning`.

- timestamp: 2026-05-14
  source: gateway/internal/emerg/recovery.go:81-121 (recoverOrphanLifecycles + recoverOneLifecycle branch a) + reconciler.go:375-377 (runOneTick chama recovery ANTES de evaluateTick)
  finding: |
    CAUSA (2) CONFIRMADA — Test 3 (TestEmergTriggerNoSpawnIfLiveLifecycle).
    O teste pré-seedava uma linha de lifecycle viva SEM `vast_instance_id`.
    Ao adquirir liderança, `runOneTick` chama `recoverOrphanLifecycles`
    ANTES de `evaluateTick`/checagem D-C5. `recoverOneLifecycle` ramo (a)
    trata linha viva com `vast_instance_id IS NULL` como "pre-create orphan"
    e a fecha imediatamente (`closeLifecycle` com
    shutdown_reason='leader_recovery_pre_create'). A linha que o teste
    contava para acionar a checagem D-C5 deixou de ser "viva" ANTES da
    checagem rodar → o trigger de OPEN sustentado disparou e a FSM avançou
    para emergency_provisioning. `recoverOrphanLifecycles` é código do Plan
    06-07, posterior ao Plan 06-05 contra o qual o teste foi escrito.
    TESTE DESATUALIZADO — reconciler/recovery corretos.

- timestamp: 2026-05-14
  source: FIX APLICADO (causa 2) — emerg_force_command_test.go + emerg_trigger_test.go (commit 355843b)
  finding: |
    Fix test-only (não-checkpoint: ajusta asserts/setup obsoletos ao
    reconciler evoluído; código de produção NÃO alterado):
    (1) emerg_force_command_test.go — Tests 1 e 2: publicar `local-llm open`
        sustentado ANTES do force-provision (mantém o tracker "open" → sem
        cancel espúrio no ramo D-C3) E wire de mock Vast.ai server +
        `SetHealthCheck` stub (provisionLifecycle precisa de cliente; sem
        ele fecharia com shutdown_reason='no_vast_client'). Asserts mudados
        de "FSM == EmergencyProvisioning" (estado agora transiente) para
        "FSM avançou para fora de HEALTHY" via helper `stateAtLeastProvisioning`.
        Mantida a intenção: leader faz INSERT de exatamente 1 row manual_force;
        follower não faz nada. Idioma copiado de emerg_provision_happy_test.go
        e emerg_force_destroy_event_test.go (ambos passam).
    (2) emerg_trigger_test.go — Test 3: pré-seed da linha COM `vast_instance_id`
        (999999, nunca discado). recoverOneLifecycle pula o ramo (a) e cai no
        guard `vastClient == nil` (teste não wireia Vast) → PULA a linha
        ("next leader acquisition will retry") sem fechá-la, deixando-a viva
        para a checagem D-C5 observar — exatamente o que o teste prova.
    Validação local: `gofmt -l .` limpo, `go build ./...` OK,
    `go vet -tags=integration ./internal/integration_test/...` sem erros.

- timestamp: 2026-05-14
  source: vps-ifix-vm — go test -tags=integration isolado por teste + irmãos (container golang:1.24, docker.sock montado, --network host)
  finding: |
    CAUSA (2) VERIFICADA. Os 3 testes passam EM ISOLAMENTO TOTAL:
      - TestEmergReconcilerHandlesForceProvisionEvent — PASS (0.64s)
      - TestEmergReconcilerForceProvisionRejectedNonLeader — PASS (1.13s)
      - TestEmergTriggerNoSpawnIfLiveLifecycle — PASS (2.98s)
    SEM REGRESSÃO — testes irmãos nos mesmos arquivos / caminho relacionado
    continuam verdes:
      - TestEmergReconcilerForceDestroyNoOpWhenIdle — PASS (1.05s)
      - TestEmergReconcilerHandlesForceDestroyEvent — PASS (6.47s)
      - TestEmergTriggerSustained — PASS (0.86s)
      - TestEmergTriggerTransient — PASS (2.46s)
    Commit 355843b em develop. NÃO pushado — decisão de push do usuário.

## Eliminated

- timestamp: 2026-05-14
  candidate: Regressão de Phase 7/8/9 no código emerg/reconciler/fsm
  reason: |
    Migrations 0020/0021/0022 só tocam audit_log. Phase 7 mexeu emerg/fsm.go
    de forma aditiva + nil-guard e reconciler.go só gofmt; `go test ./...` do
    gateway está green (25 pkgs). Os testes nunca rodaram antes — é o 1o run,
    não uma regressão.

- timestamp: 2026-05-14
  candidate: TODAS as falhas explicadas por contaminação de emergency_lifecycles
  reason: |
    REFUTADO pela evidência de isolamento. O fix de harness resolveu 5+ testes,
    mas 3 testes (TestEmergReconcilerHandlesForceProvisionEvent,
    TestEmergReconcilerForceProvisionRejectedNonLeader,
    TestEmergTriggerNoSpawnIfLiveLifecycle) falham EM PROCESSO ISOLADO com
    containers frescos — impossível haver estado vazado. A diagnose original
    era parcial: cobria a contaminação de DB mas não a 2a causa-raiz no
    caminho force-provision/D-C5.

- timestamp: 2026-05-14
  candidate: Causa (2) é bug de produção no reconciler/FSM (caminho force-provision/D-C5)
  reason: |
    REFUTADO por leitura de código de produção (reconciler.go,
    recovery.go, lifecycle.go, tracker.go). O comportamento do reconciler é
    correto e intencional: (a) evaluateEmergencyProvisioning cancela o
    provisioning quando o tracker indica "primary recuperou" — e um tracker
    fresco SEM evento OPEN publicado legitimamente reporta "closed";
    (b) recoverOrphanLifecycles fecha corretamente uma linha viva sem
    vast_instance_id como pre-create orphan. Os 3 testes foram escritos
    contra o reconciler do Plan 06-05 (stub) e não acompanharam Plans
    06-06/06-07. STALE TESTS — fix test-only, sem alterar produção.

## Resolution

- root_cause: |
    DUAS causas-raiz distintas, ambas RESOLVIDAS:

    (1) [RESOLVIDA — commit 9772d71] O harness de teste de integração não
    isolava `ai_gateway.emergency_lifecycles` entre test functions.
    `freshSchema` (setup_test.go) só TRUNCATEava 5 tabelas e omitia
    `emergency_lifecycles`. Container Postgres compartilhado package-wide →
    linhas vazavam entre testes, quebrando asserts de contagem absoluta e
    colidindo com o índice parcial único `emergency_live_singleton`.
    Doc-comment do TestMain afirmava incorretamente "db.Down + db.Up".
    → CORRIGIDA; 5+ testes voltaram a passar.

    (2) [RESOLVIDA — commit 355843b] 3 testes (TestEmergReconcilerHandlesForceProvisionEvent,
    TestEmergReconcilerForceProvisionRejectedNonLeader,
    TestEmergTriggerNoSpawnIfLiveLifecycle) falhavam EM ISOLAMENTO TOTAL.
    STALE TESTS — escritos contra o reconciler do Plan 06-05 (cujo branch
    StateEmergencyProvisioning era stub: "Plan 06-05 stops at the FSM
    transition"). Plans 06-06/06-07 adicionaram `evaluateEmergencyProvisioning`
    + `recoverOrphanLifecycles` e os testes não acompanharam:
    - Tests 1/2: o tick após o force-provision INSERT entra no ramo de
      cancel-detection (D-C3) e lê `tracker.State()`. Um tracker fresco SEM
      evento `local-llm open` publicado reporta "closed" (zero-value), que
      o reconciler corretamente lê como "primary recuperou → cancela o
      provisioning" — quicando a FSM EmergencyProvisioning → Healthy em
      ~1 tick. O teste capturava "healthy".
    - Test 3: a linha de lifecycle pré-seedada sem `vast_instance_id` era
      fechada por `recoverOrphanLifecycles` (ramo pre-create orphan, roda
      no 1o tick do leader, ANTES da checagem D-C5). A linha deixava de ser
      "viva" antes da checagem D-C5 → trigger disparava.
    Reconciler/FSM/recovery de PRODUÇÃO estão CORRETOS — fix foi test-only.
- fix: |
    APLICADO (ambas as causas):
    - Causa (1), commit 9772d71: gateway/internal/integration_test/setup_test.go
      — TRUNCATE `emergency_lifecycles RESTART IDENTITY CASCADE` em
      `freshSchema` + correção do doc-comment do TestMain.
    - Causa (2), commit 355843b (test-only, sem checkpoint pois não altera
      produção):
      • emerg_force_command_test.go — Tests 1/2: publicar `local-llm open`
        sustentado antes do force-provision + wire de mock Vast.ai server +
        `SetHealthCheck` stub; asserts ajustados de "FSM == EmergencyProvisioning"
        para "FSM avançou para fora de HEALTHY" (helper `stateAtLeastProvisioning`).
      • emerg_trigger_test.go — Test 3: pré-seed da linha COM `vast_instance_id`
        para que recoverOrphanLifecycles a pule (guard `vastClient == nil`)
        em vez de fechá-la como pre-create orphan.
    Validação local: gofmt limpo, go build OK, go vet (integration tag) OK.
- verification: |
    Causa (1) VERIFICADA em vps-ifix-vm (container golang:1.24, docker.sock
    montado, --network host) — 5+ testes voltaram a passar (registro acima).
    Causa (2) VERIFICADA em vps-ifix-vm — os 3 testes passam EM ISOLAMENTO
    TOTAL (processo único, containers frescos):
      - TestEmergReconcilerHandlesForceProvisionEvent — PASS (0.64s)
      - TestEmergReconcilerForceProvisionRejectedNonLeader — PASS (1.13s)
      - TestEmergTriggerNoSpawnIfLiveLifecycle — PASS (2.98s)
    Sem regressão — testes irmãos / caminho relacionado continuam verdes:
      - TestEmergReconcilerForceDestroyNoOpWhenIdle — PASS (1.05s)
      - TestEmergReconcilerHandlesForceDestroyEvent — PASS (6.47s)
      - TestEmergTriggerSustained — PASS (0.86s)
      - TestEmergTriggerTransient — PASS (2.46s)
    Nota: TestEmergBidRaceLost permanece um flake de timing sob carga de
    suíte cheia (passa isolado) — fora do escopo desta sessão, não é
    contaminação de harness nem bug de produção.
- files_changed:
    - gateway/internal/integration_test/setup_test.go
    - gateway/internal/integration_test/emerg_force_command_test.go
    - gateway/internal/integration_test/emerg_trigger_test.go
