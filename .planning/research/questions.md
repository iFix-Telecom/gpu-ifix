# Research Questions

Open questions surfaced during exploration/planning. Resolved during the relevant phase's research.

## Phase 17 — Dashboard pod-config control

- **Q (2026-06-29): Enumerate the FULL primary-pod config surface and classify each setting hot-reloadable vs boot-only/structural.**
  Required before planning Phase 17. Sweep `gateway/internal/.../config.go` + the running prod env for every `PRIMARY_*` (and related: schedule UpHour/DownHour/`provision_lead_seconds`, `coldstart_budget`, `shape0_cap`/price cap, port-bind budget, `PRIMARY_NUM_GPUS`/shape, `PRIMARY_TEMPLATE_IMAGE`, allowlist, blocklist, DCGM URL, any DSN/infra). For each: (a) is it read once at boot or per-reconciler-tick? (b) can the reconciler safely re-read it live without restarting in-flight provisioning/lifecycles? (c) which are "hot" (live reload) vs "structural" (need `os.Exit(0)` self-restart)? (d) what are the safe validation bounds per field (min/max/enum)? (e) which are "dangerous" (require confirm: restart, cap-down, shape change)?
  Output feeds: the `pod_config` table schema, the hot-reload reconciler change, the dashboard field set + bounds, and the structural-vs-hot UI flagging.
  Depends on understanding: `config.go` fail-fast env-at-boot pattern; reconciler tick loop; whether moving a given setting to DB-read mid-lifecycle is safe.
