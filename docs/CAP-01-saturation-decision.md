# CAP-01 — Saturation / Capacity Decision

**Status:** Decision (doc-only, D-19). Implementation, if adopted, becomes a future phase.
**Date:** 2026-06-13
**Owner:** gpu-ifix gateway
**Inputs:** Phase 11-06 load-test baseline (`.planning/phases/11-prod-hardening/11-06-EVIDENCE.md`); Phase 12 chaos validation (12-04 dev, 12-05 prod).

---

## 1. The baseline (Phase 11-06, what the numbers actually were)

A 30-minute sustained load-replay ran against a **1×RTX 5090 single-GPU primary**
at `--max-concurrency 50 --speedup 4` (a deliberately aggressive peak-replay).
Per-route P95 vs the v1.0 SLOs:

| Route | p50 | **p95** | p99 | SLO (p95) | Verdict |
|-------|-----|---------|-----|-----------|---------|
| chat  | 8.9 s | **21.7 s** (21680 ms) | — | ≤ 5 s | **FAIL** |
| embed | — | 2.66 s (2662 ms) | — | ≤ 1 s | **FAIL** |
| stt   | — | 2.8 s | — | ≤ 10 s | PASS |

Error rate 2.73% (33 transient `upstream_5xx` under saturation; **0 panics, 0 data loss**).

### Root cause
The single GPU **serializes LLM decode** across 50 in-flight chat requests. Once the
GPU's batch slots are full, additional requests queue, and **queueing time dominates
end-to-end latency** — the chat p95 of **21.7 s** is overwhelmingly wait time, not
compute time. The p50/p95 spread (8.9 s → 21.7 s) is the signature of a saturated
single-server queue, not a slow model. embed shares the same host and GPU, so it
degrades in lockstep under chat saturation.

This was an honest **saturation characterization**, not a clean SLO pass: at this
offered load the single-GPU shape cannot hold the v1.0 chat/embed P95 SLOs.

---

## 2. What Phase 12 already changed (and what it did NOT)

Phase 12 (RES-11/12/13) made the gateway **survive a primary death** — autonomous
death detection, tier-1 fallthrough with zero connection-class 502, sensitive 503
fail-closed. Validated live on real Vast kills (12-04 dev, 12-05 prod).

**Phase 12 is about availability under failure, not throughput under load.** It does
NOT change the saturation ceiling: a live single-GPU primary at concurrency 50 still
produces the 21.7 s chat p95. CAP-01 is the separate capacity question.

---

## 3. Options considered

| # | Option | Effect | Cost / risk |
|---|--------|--------|-------------|
| A | **Concurrency cap + admission control** (bound in-flight per GPU, shed/queue beyond a depth) | Caps tail latency: requests fail fast or queue with a known bound instead of all degrading to 21.7 s | Some requests shed/429 under burst; needs a queue-depth knob + shed policy |
| B | **Multi-GPU primary shape** (2×3090 or 2×GPU, llama tensor-split / more batch slots) | Raises the saturation ceiling — more parallel decode slots | ~2× hourly GPU cost; multi-GPU CDI validated (2×3090 spike) but adds provisioning surface |
| C | **Revise SLO / pacing assumptions** (the p95 ≤ 5 s SLO assumed lighter offered load than speedup-4 × concurrency-50) | Aligns the SLO with realistic prod arrival rates | Risk of masking real saturation if prod genuinely hits concurrency 50 |
| D | **Do nothing** | — | Tail latency unbounded under burst; users wait 20 s+ |

---

## 4. Decision

**Adopt A (concurrency cap + admission control) as the primary lever, with C
(SLO/pacing realism) as a companion, and keep B (multi-GPU) as the scale-out path
when real arrival rates justify the cost.**

Rationale:
- The 21.7 s chat p95 is a **queueing** failure, not a compute failure. The correct
  first response to unbounded queueing is to **bound the queue** (admission control),
  not to buy more GPU — a bounded queue turns a 21.7 s silent degradation into a fast,
  observable shed (429/`Retry-After`) that callers can handle.
- `--max-concurrency 50 --speedup 4` is an **aggressive synthetic peak**, not a
  measured prod arrival rate. Before paying ~2× for multi-GPU (B), prod must
  instrument its **real** concurrency distribution and decide the SLO against it (C).
- Multi-GPU (B) remains the scale-out answer once real sustained concurrency
  approaches the single-GPU ceiling; it is complementary to A, not a substitute.

### Recommended initial parameters (to validate in the implementation phase)
- **Per-primary in-flight cap:** start near the single-GPU healthy batch size
  (empirically the point before p50→p95 diverges; ~8–12 in-flight on 1×3090/5090 from
  the 11-06 curve). Tune against measured prod concurrency.
- **Queue depth beyond cap:** a small bounded queue (e.g. 1–2× the in-flight cap) with
  a wait ceiling; beyond it, shed with HTTP 429 + `Retry-After`.
- **Shedding is tenant-aware:** sensitive tenants keep the RES-08 fail-closed contract;
  normal tenants may also fall through to tier-1 under shed (reusing the RES-13 path).

---

## 5. Scope / non-goals

- **This document is the decision; it ships no code (D-19 doc-only).** Implementing
  the concurrency cap / admission control / queue-depth knob is a **future phase**,
  gated on instrumenting real prod concurrency first.
- The Phase 12 resilience gates (zero-502 failover, death detection, sensitive 503)
  are independent and already shipped — CAP-01 does not block them.

## 6. References
- Baseline: `.planning/phases/11-prod-hardening/11-06-EVIDENCE.md` (chat p95 **21.7 s** @ concurrency 50, 1×5090).
- Resilience validation: `12-04-DEV-CHAOS-UAT.md`, `12-05-PROD-CHAOS-GATE.md`.
- Multi-GPU feasibility: 2×3090 CDI spike (single-pod, llama auto-split validated).
