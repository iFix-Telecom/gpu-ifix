// Package primary implements the primary pod lifecycle for ifix-ai-gateway.
//
// Phase 6.6 introduces a schedule-driven 5-state FSM
// (Asleep|Provisioning|Ready|Draining|Destroying) distinct from the
// emergency-event-driven emerg package. While emerg provisions a single
// llama-server (LLM-only) on user demand, primary co-locates 4 services
// (LLM + Whisper STT + BGE-M3 embeddings + DCGM exporter) in one Vast.ai
// pod managed by supervisord, sized for peak-hour business traffic and
// torn down on schedule to control cost.
//
// # Wave 0 LOCKED (06.6-WAVE0-GATES.md)
//
//   - DinD REJECTED: 06.6-SPIKE-dind-privileged.md empirically proved
//     overlayfs in nested namespace fails on Vast.ai 4090 hosts even with
//     --privileged:1 (Linux kernel namespace limit, not bypassable).
//   - Strategy α custom multi-stage image (pod/primary/Dockerfile) bundles
//     supervisord (PID 1) + the 4 service binaries extracted from
//     SHA-pinned upstream images. supervisord replaces DinD's intent of
//     4-services-in-1-pod while sharing one network namespace, GPU device,
//     and filesystem.
//   - llama.cpp engine tag UPGRADED from b9128 to b9191 for Qwen3.6 27B
//     SSM/hybrid support (b9128 fails with missing tensor
//     blk.64.ssm_conv1d.weight; b9191 includes the merged SSM PRs).
//     Validated end-to-end in 06.6-SPIKE-qwen3.6-jinja.md Round 3.
//   - B1 GGUF-embedded Jinja: Qwen3.6 27B GGUF carries chat_template that
//     renders tool-calling structure via llama-server --jinja alone. No
//     --chat-template-file in default args. Override path
//     (PRIMARY_QWEN_JINJA_KEY / SHA256) reserved for B2 MinIO fallback.
//
// # File layout
//
//   - doc.go         — this file (package-level documentation)
//   - onstart.go     — primaryOnstartHead raw-string + buildPrimaryOnstart
//   - lifecycle.go   — Reconciler skeleton + buildCreateRequest +
//     SHA-fail-fast sentinel errors + primaryPodURLs
//   - lifecycle_test.go — unit tests covering supervisord PID 1 invariant,
//     Wave 0 LOCKED defaults, shell hardening, SHA fail-fast,
//     D-03/D-07 traceability, and structural assertions on
//     pod/primary/Dockerfile + pod/primary/supervisord.conf
//
// See 06.6-CONTEXT.md and 06.6-RESEARCH.md for the broader Phase 6.6
// architecture rationale.
package primary
