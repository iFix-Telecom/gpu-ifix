// Package quota provides rate-limit (RPS/RPM token bucket) and daily/monthly
// quota enforcement per tenant. This file declares the sentinel errors used
// by middleware in Wave 2 and by the discriminated-error envelope emitted
// by the gateway (D-A4).
package quota

import "errors"

var (
	// ErrRateLimitRPS — token bucket exhausted in 1s window. HTTP 429,
	// OpenAI envelope type "rate_limit_error", code "rate_limit_rps", header Retry-After: 1.
	ErrRateLimitRPS = errors.New("quota: rate limit exceeded (RPS)")
	// ErrRateLimitRPM — token bucket exhausted in 60s window. HTTP 429,
	// type "rate_limit_error", code "rate_limit_rpm".
	ErrRateLimitRPM = errors.New("quota: rate limit exceeded (RPM)")

	// ErrQuotaExceededDailyTokens — daily LLM-token quota exhausted. HTTP 429,
	// type "insufficient_quota", code "quota_exceeded_daily_tokens".
	ErrQuotaExceededDailyTokens = errors.New("quota: daily token quota exceeded")
	// ErrQuotaExceededDailyAudioMinutes — daily STT minutes exhausted. code "quota_exceeded_daily_audio_minutes".
	ErrQuotaExceededDailyAudioMinutes = errors.New("quota: daily audio-minutes quota exceeded")
	// ErrQuotaExceededDailyEmbeds — daily embed-requests exhausted. code "quota_exceeded_daily_embeds".
	ErrQuotaExceededDailyEmbeds = errors.New("quota: daily embed quota exceeded")

	// ErrQuotaExceededMonthlyTokens — monthly LLM-token quota exhausted. code "quota_exceeded_monthly_tokens".
	ErrQuotaExceededMonthlyTokens = errors.New("quota: monthly token quota exceeded")
	// ErrQuotaExceededMonthlyAudioMinutes — code "quota_exceeded_monthly_audio_minutes".
	ErrQuotaExceededMonthlyAudioMinutes = errors.New("quota: monthly audio-minutes quota exceeded")
	// ErrQuotaExceededMonthlyEmbeds — code "quota_exceeded_monthly_embeds".
	ErrQuotaExceededMonthlyEmbeds = errors.New("quota: monthly embed quota exceeded")

	// ErrQuotaCheckUnavailable — Postgres usage_counters lookup failed; D-A2 fail-closed.
	// HTTP 503, type "service_unavailable", code "quota_check_unavailable".
	ErrQuotaCheckUnavailable = errors.New("quota: check unavailable (fail-closed)")
)
