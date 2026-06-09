// Package admin (debug_panic.go): operator-only synthetic panic emitter
// used by `gatewayctl debug emit-error` (Phase 11 Plan 04 D-18.2) to
// exercise the httpx.Recoverer -> sentry.CurrentHub().Recover ->
// sentry.Flush(500ms) chain in PROD. The handler is the canonical S11
// panic-path proof — Phase 10 only verified the code shape, never the
// runtime path.
//
// Wiring (gateway/cmd/gateway/main.go):
//   - Mount at POST /admin/debug/panic under the admin sub-router so
//     admin.Middleware (X-Admin-Key bcrypt) fires before the handler.
//   - httpx.Recoverer is applied GLOBALLY by buildRouter (r.Use line
//     ~1152). The wrap order is therefore Recoverer(adminMiddleware(
//     DebugPanicHandler)) — Recoverer outermost. If a future refactor
//     inverts that order, the panic will crash the gateway process
//     instead of returning a sanitized 500. Threat T-11-OPS-04.
//
// Why a dedicated file (vs adding a route to an existing handler):
// the synthetic panic message string ("synthetic panic emitted by
// gatewayctl debug emit-error") is the Sentry event search string the
// runbook references. Keeping it in a small, named handler makes the
// breadcrumb obvious and the wrap-order invariant testable.
package admin

import (
	"log/slog"
	"net/http"
)

// DebugPanicHandler returns an http.Handler that always panics. Used by
// `gatewayctl debug emit-error` to prove the Recoverer + Sentry path
// end-to-end in PROD.
//
// Operator audit trail: when AdminContext is present in r.Context (set
// by admin.Middleware), the handler emits a WARN log line carrying
// admin_key_id + label BEFORE the panic so a misconfigured Sentry SDK
// (or a Recoverer regression that crashes the process) still leaves a
// local breadcrumb identifying the operator who tripped it.
//
// The panic message is intentionally generic — Recoverer's WriteOpenAIError
// emits a sanitized envelope only, NEVER the panic string itself
// (threat T-11-OPS-08). The string is used solely as a Sentry event
// search anchor.
func DebugPanicHandler(log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if ac, ok := FromContext(r.Context()); ok {
			log.WarnContext(r.Context(),
				"synthetic panic about to fire",
				"admin_key_id", ac.AdminKeyID.String(),
				"label", ac.Label,
				"key_prefix", ac.KeyPrefix,
			)
		} else {
			log.WarnContext(r.Context(),
				"synthetic panic about to fire (no admin context — middleware not in chain?)",
			)
		}
		panic("synthetic panic emitted by gatewayctl debug emit-error")
	})
}
