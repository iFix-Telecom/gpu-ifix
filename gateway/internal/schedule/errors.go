// Package schedule implements peak/24-7 routing decisions per tenant. The
// middleware consults the tenants loader to decide pre-dispatch whether a
// request in peak mode + off-hours should skip tier-0 local and go direct
// to OpenRouter. This file declares the sentinel errors used by Wave 2
// middleware (D-C2).
package schedule

import "errors"

var (
	// ErrOffHoursUpstreamUnavailable — peak-mode tenant outside business hours
	// and the tier-1 OpenRouter upstream is also unavailable. HTTP 503,
	// type "service_unavailable", code "off_hours_upstream_unavailable".
	// Per Phase 3 D-C4: NO fall-of-fallback to OpenAI direct chat (Qwen fixo).
	ErrOffHoursUpstreamUnavailable = errors.New("schedule: off-hours upstream unavailable")
)
