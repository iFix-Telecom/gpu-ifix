// Package vast — REST client for the Vast.ai API
// (https://console.vast.ai/api/v0). Phase 6 of the gpu-ifix project.
//
// # Operation surface (5 + Ping)
//
//   - Ping(ctx)                                   — GET /users/current  (boot validation, D-A5)
//   - SearchOffers(ctx, filter)                   — GET /bundles?q=...   (D-A2 filter shape)
//   - CreateInstance(ctx, offerID, req)           — PUT /asks/{offer_id}/ (returns Instance{ID: NewContract})
//   - GetInstance(ctx, instanceID)                — GET /instances/{id}/  (poll loop in lifecycle.go)
//   - DestroyInstance(ctx, instanceID)            — DELETE /instances/{id}/  (idempotent — 404 returns nil)
//
// # Threat model (T-6-01)
//
// VAST_AI_API_KEY is passed ONLY via the Authorization: Bearer header.
// It MUST NOT appear in:
//
//   - log lines     (must NOT pass the key field to slog/log methods)
//   - Sentry breadcrumbs / extras
//   - error messages (VastError.Error() formats only the HTTP status)
//   - panic stack traces
//
// `TestClientNeverLogsAPIKey` enforces this via a regex grep over the
// client.go source file at build time.
//
// # HTTP timeout (D-A1)
//
// Fixed at 30 seconds via the package-level `httpTimeout` constant. Vast
// is slow under load (search returns 100+ offers; create involves a
// queue). 10s flaps under load; 60s wastes budget on dead requests.
// Operators retune in the field by editing this file — not env-tunable.
//
// # Defensive error parsing
//
// Vast.ai returns inconsistent error envelopes (4xx vs 5xx, with `msg`
// vs `message` keys, and 4xx may or may not include a JSON body at all).
// `parseErrorBody` reads up to 16 KiB, attempts JSON decode, and maps
// status codes to sentinel errors:
//
//   - 401, 403           → ErrUnauthorized
//   - 404, 410 + no_such_ask     → ErrOfferGone
//   - 404 + no_such_instance     → ErrInstanceNotFound
//   - 429                → ErrRateLimited
//   - 5xx                → *VastError{Status, Code:"server_error"}
//   - other              → *VastError{Status, Code, Msg}
//
// # Idempotent destroy (RESEARCH lines 717-719)
//
// DestroyInstance returns nil when the API responds 404 + no_such_instance
// (the instance was already destroyed by Vast or another operator). This
// keeps leader-recovery cleanup simple — the lifecycle row gets closed
// regardless of whether the underlying Vast instance still exists.
package vast

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const (
	// DefaultBaseURL is the canonical Vast.ai REST host as of 2026-05-13.
	// The legacy `https://vast.ai/api/v0` returns HTTP 308 to this host;
	// callers can override via NewClientWithBaseURL for httptest fixtures.
	DefaultBaseURL = "https://console.vast.ai/api/v0"

	// httpTimeout is fixed at 30 seconds per CONTEXT.md D-A1. Package-level
	// constant; NOT env-tunable. Search / create / get / destroy all share
	// this budget — a single op blocking longer than 30s indicates a Vast
	// outage that the lifecycle should surface to Sentry, not retry under.
	httpTimeout = 30 * time.Second
)

// Client is the thin Vast.ai REST client. Construct via NewClient (uses
// DefaultBaseURL) or NewClientWithBaseURL (for tests). All methods are
// safe to call concurrently because *http.Client is goroutine-safe.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient returns a Client wired against `DefaultBaseURL` with a 30s
// HTTP timeout. The `apiKey` is held in the Client struct only for the
// duration of the request — it is never logged or persisted.
func NewClient(apiKey string) *Client {
	return &Client{
		baseURL:    DefaultBaseURL,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: httpTimeout},
	}
}

// NewClientWithBaseURL is the test-friendly constructor — pass the
// httptest.Server.URL to point the client at a local mock. The trailing
// slash on baseURL is stripped if present.
func NewClientWithBaseURL(apiKey, baseURL string) *Client {
	c := NewClient(apiKey)
	c.baseURL = strings.TrimRight(baseURL, "/")
	return c
}

// HTTPTimeout exposes the package-level timeout for tests that want to
// assert "the client uses a 30s deadline" without reaching into the
// unexported httpClient field.
func (c *Client) HTTPTimeout() time.Duration {
	return c.httpClient.Timeout
}

// Ping issues GET /users/current and returns nil on HTTP 200. Used at
// boot per D-A5 to fail-loud when VAST_AI_API_KEY is invalid (returns
// ErrUnauthorized) or when Vast's API is unreachable (transport error).
func (c *Client) Ping(ctx context.Context) error {
	u := c.baseURL + "/users/current"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	c.setAuthHeader(req)

	obs.GatewayVastAPIRequestsTotal.WithLabelValues("ping", "started").Inc()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		obs.GatewayVastAPIRequestsTotal.WithLabelValues("ping", "transport_error").Inc()
		return err
	}
	defer resp.Body.Close()
	obs.GatewayVastAPIRequestsTotal.WithLabelValues("ping", strconv.Itoa(resp.StatusCode)).Inc()

	if resp.StatusCode != http.StatusOK {
		return c.parseErrorBody(resp)
	}
	// Drain body — small response, but be polite to keep-alive.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16*1024))
	return nil
}

// SearchOffers issues GET /bundles?q=... with the JSON-encoded filter as
// the URL query parameter. Returns the `offers` array verbatim — empty
// slice (not nil) when zero offers match.
//
// The caller is responsible for the epsilon comparison against
// VAST_PRICE_CAP_DPH (Pitfall 5); the `dph_total` filter applied
// server-side is best-effort and the lifecycle re-checks each offer.
func (c *Client) SearchOffers(ctx context.Context, filter SearchFilter) ([]Offer, error) {
	q, err := json.Marshal(filter)
	if err != nil {
		return nil, fmt.Errorf("vast: marshal filter: %w", err)
	}
	u := fmt.Sprintf("%s/bundles?q=%s", c.baseURL, url.QueryEscape(string(q)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.setAuthHeader(req)

	obs.GatewayVastAPIRequestsTotal.WithLabelValues("search", "started").Inc()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		obs.GatewayVastAPIRequestsTotal.WithLabelValues("search", "transport_error").Inc()
		return nil, err
	}
	defer resp.Body.Close()
	obs.GatewayVastAPIRequestsTotal.WithLabelValues("search", strconv.Itoa(resp.StatusCode)).Inc()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseErrorBody(resp)
	}
	var body struct {
		Offers []Offer `json:"offers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("vast: decode search response: %w", err)
	}
	if body.Offers == nil {
		body.Offers = []Offer{}
	}
	return body.Offers, nil
}

// CreateInstance issues PUT /asks/{offer_id}/ with the JSON-encoded
// CreateRequest. Returns an Instance with `ID = response.NewContract` on
// success (other Instance fields are left zero — caller polls GetInstance
// to populate them).
//
// Sentinel error mapping:
//   - 404, 410 + no_such_ask     → ErrOfferGone (lifecycle bid race retry)
//   - 401, 403                   → ErrUnauthorized
//   - 429                        → ErrRateLimited
//   - 5xx                        → *VastError{Status, Code:"server_error"}
func (c *Client) CreateInstance(ctx context.Context, offerID int64, body CreateRequest) (Instance, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return Instance{}, fmt.Errorf("vast: marshal create request: %w", err)
	}
	u := fmt.Sprintf("%s/asks/%d/", c.baseURL, offerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(payload))
	if err != nil {
		return Instance{}, err
	}
	c.setAuthHeader(req)
	req.Header.Set("Content-Type", "application/json")

	obs.GatewayVastAPIRequestsTotal.WithLabelValues("create", "started").Inc()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		obs.GatewayVastAPIRequestsTotal.WithLabelValues("create", "transport_error").Inc()
		return Instance{}, err
	}
	defer resp.Body.Close()
	obs.GatewayVastAPIRequestsTotal.WithLabelValues("create", strconv.Itoa(resp.StatusCode)).Inc()

	if resp.StatusCode != http.StatusOK {
		return Instance{}, c.parseErrorBody(resp)
	}
	var cr CreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return Instance{}, fmt.Errorf("vast: decode create response: %w", err)
	}
	if !cr.Success || cr.NewContract == 0 {
		// Defensive: HTTP 200 + success=false is rare but observed on
		// some Vast edge cases (e.g. account suspended mid-request).
		return Instance{}, &VastError{
			Status: resp.StatusCode,
			Code:   "create_failed",
			Msg:    "Vast returned success=false on HTTP 200",
		}
	}
	return Instance{ID: cr.NewContract}, nil
}

// GetInstance issues GET /instances/{id}/ and returns the parsed Instance.
// Vast wraps the body in `{"instances": {...}}` — we unwrap silently.
//
// Returns ErrInstanceNotFound on 404 + no_such_instance (used by leader
// recovery to identify lost lifecycles per D-D5).
//
// Note: when the instance is destroyed, Vast also returns HTTP 200 with
// `{"instances": null}` — we surface that as ErrInstanceNotFound too so
// callers see one consistent signal for "the instance is gone".
func (c *Client) GetInstance(ctx context.Context, instanceID int64) (Instance, error) {
	u := fmt.Sprintf("%s/instances/%d/", c.baseURL, instanceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Instance{}, err
	}
	c.setAuthHeader(req)

	obs.GatewayVastAPIRequestsTotal.WithLabelValues("get", "started").Inc()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		obs.GatewayVastAPIRequestsTotal.WithLabelValues("get", "transport_error").Inc()
		return Instance{}, err
	}
	defer resp.Body.Close()
	obs.GatewayVastAPIRequestsTotal.WithLabelValues("get", strconv.Itoa(resp.StatusCode)).Inc()

	if resp.StatusCode != http.StatusOK {
		return Instance{}, c.parseErrorBody(resp)
	}
	// `instances` may be a single object (current API) or an array (some
	// edge cases in older Vast endpoints). Decode as a peek-message to
	// handle both, mirroring the defensive shape pod/scripts/vast-ai.sh
	// uses (`.instances // .instances[0]`).
	var raw struct {
		Instances json.RawMessage `json:"instances"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Instance{}, fmt.Errorf("vast: decode get response: %w", err)
	}
	if len(raw.Instances) == 0 || string(raw.Instances) == "null" {
		// `{"instances": null}` — Vast convention for "destroyed".
		return Instance{}, ErrInstanceNotFound
	}
	// Try object first (current API), then array.
	var inst Instance
	if err := json.Unmarshal(raw.Instances, &inst); err == nil && inst.ID != 0 {
		return inst, nil
	}
	var arr []Instance
	if err := json.Unmarshal(raw.Instances, &arr); err == nil && len(arr) > 0 {
		return arr[0], nil
	}
	return Instance{}, fmt.Errorf("vast: instances field is neither object nor non-empty array")
}

// DestroyInstance issues DELETE /instances/{id}/ and returns nil on HTTP
// 200 OR on 404 + no_such_instance (idempotent — the instance is gone
// either way, which is what the caller wanted).
func (c *Client) DestroyInstance(ctx context.Context, instanceID int64) error {
	u := fmt.Sprintf("%s/instances/%d/", c.baseURL, instanceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	c.setAuthHeader(req)

	obs.GatewayVastAPIRequestsTotal.WithLabelValues("destroy", "started").Inc()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		obs.GatewayVastAPIRequestsTotal.WithLabelValues("destroy", "transport_error").Inc()
		return err
	}
	defer resp.Body.Close()
	obs.GatewayVastAPIRequestsTotal.WithLabelValues("destroy", strconv.Itoa(resp.StatusCode)).Inc()

	if resp.StatusCode == http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16*1024))
		return nil
	}
	parsedErr := c.parseErrorBody(resp)
	// Idempotent destroy: 404 + no_such_instance is success.
	if parsedErr == ErrInstanceNotFound {
		return nil
	}
	return parsedErr
}

// setAuthHeader is the ONE place the API key touches an http.Request. By
// keeping the assignment behind a method, code review can grep for
// `c.apiKey` and see exactly one site (this method) and confirm it never
// flows into a log/error/breadcrumb.
func (c *Client) setAuthHeader(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
}

// parseErrorBody reads up to 16 KiB of the response body, attempts JSON
// decode, and maps the status code + envelope to the sentinel errors
// declared in errors.go. Body reads are bounded to prevent a malicious
// upstream from flooding our heap.
//
// The returned error NEVER includes the request URL or any header —
// only the HTTP status text and the API-supplied `msg`. This keeps the
// API key out of error logs even when callers wrap with `%w`.
func (c *Client) parseErrorBody(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	var env vastErrorEnvelope
	_ = json.Unmarshal(body, &env)
	msg := env.Msg
	if msg == "" {
		msg = env.Message
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}

	// Vast.ai is inconsistent: PUT /asks/{id}/ for a stale offer can come
	// back as HTTP 400 with body `error="no_such_ask"` instead of the
	// documented 404/410 envelope. Normalise both shapes to ErrOfferGone
	// so the lifecycle's bid-race retry kicks in (3 attempts with fresh
	// SearchOffers) before bubbling up as ErrOfferRaceLost.
	msgLower := strings.ToLower(msg)
	bodyLower := strings.ToLower(string(body))
	if env.Error == "no_such_ask" ||
		strings.Contains(msgLower, "no longer available") ||
		strings.Contains(msgLower, "no_such_ask") ||
		strings.Contains(bodyLower, "no_such_ask") {
		return ErrOfferGone
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusNotFound, http.StatusGone:
		if env.Error == "no_such_instance" {
			return ErrInstanceNotFound
		}
		return &VastError{Status: resp.StatusCode, Code: env.Error, Msg: msg}
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return &VastError{Status: resp.StatusCode, Code: "server_error", Msg: msg}
	}
	return &VastError{Status: resp.StatusCode, Code: env.Error, Msg: msg}
}
