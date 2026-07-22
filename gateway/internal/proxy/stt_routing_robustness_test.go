package proxy

import (
	"net/http"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// TestResolveSTTTarget_CanonicalFallback (Fix A): an unregistered inbound STT
// model (e.g. Maestro's "whisper-large-v3-turbo") falls back to the upstream's
// canonical alias so the upstream gets ITS real model instead of a 404.
func TestResolveSTTTarget_CanonicalFallback(t *testing.T) {
	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"whisper", "local-stt"}:      "Systran/faster-whisper-large-v3",
		{"whisper", "openai-whisper"}: "whisper-1",
	})

	cases := []struct {
		name, alias, upstream, want string
	}{
		{"unregistered->canonical (pod)", "whisper-large-v3-turbo", "local-stt", "Systran/faster-whisper-large-v3"},
		{"unregistered->canonical (openai)", "whisper-large-v3-turbo", "openai-whisper", "whisper-1"},
		{"registered alias wins", "whisper", "local-stt", "Systran/faster-whisper-large-v3"},
		{"no canonical for upstream -> passthrough", "whisper-large-v3-turbo", "mystery-stt", "whisper-large-v3-turbo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveSTTTarget(resolver, c.alias, c.upstream); got != c.want {
				t.Fatalf("resolveSTTTarget(%q,%q) = %q; want %q", c.alias, c.upstream, got, c.want)
			}
		})
	}
}

// TestIsRetryableSTTStatus (Fix B): cascade-worthy statuses only.
func TestIsRetryableSTTStatus(t *testing.T) {
	retry := []int{404, 408, 425, 429, 500, 502, 503, 504, 599}
	terminal := []int{200, 201, 400, 401, 403, 409, 413, 415, 422}
	for _, c := range retry {
		if !isRetryableSTTStatus(c) {
			t.Errorf("status %d: want retryable", c)
		}
	}
	for _, c := range terminal {
		if isRetryableSTTStatus(c) {
			t.Errorf("status %d: want terminal", c)
		}
	}
}

// TestSTTRetryableStatusInterceptor (Fix B): raises the retryable sentinel on a
// cascade-worthy status, nil otherwise — so ComposeInterceptors/ErrorHandler
// drive the tier cascade.
func TestSTTRetryableStatusInterceptor(t *testing.T) {
	ic := sttRetryableStatusInterceptor{}
	if err := ic.Intercept(&http.Response{StatusCode: 404}); err != errUpstreamRetryable {
		t.Fatalf("404: want errUpstreamRetryable, got %v", err)
	}
	if err := ic.Intercept(&http.Response{StatusCode: 429}); err != errUpstreamRetryable {
		t.Fatalf("429: want errUpstreamRetryable, got %v", err)
	}
	if err := ic.Intercept(&http.Response{StatusCode: 200}); err != nil {
		t.Fatalf("200: want nil, got %v", err)
	}
	if err := ic.Intercept(&http.Response{StatusCode: 400}); err != nil {
		t.Fatalf("400: want nil (terminal), got %v", err)
	}
}
