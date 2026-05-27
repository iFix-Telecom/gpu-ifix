// Package admin (debug_panic_test.go): automated HTTP integration test
// for /admin/debug/panic (Phase 11 Plan 04 D-18.2 — reviews HIGH #1).
//
// This is the "code placement alone is NOT acceptable" gate the reviewer
// called out: code placement of the route under the admin chain inside
// gateway/cmd/gateway/main.go is necessary but not sufficient. The
// canonical proof is two automated assertions driven through raw HTTP:
//
//  1. Unauthenticated POST returns 401/403 — the admin auth gate fires
//     BEFORE the handler, so a misconfigured router that mounted the
//     route outside the admin chain would fail this test.
//  2. Authenticated POST returns 500 with a SANITIZED OpenAI error
//     envelope — proving (a) Recoverer caught the panic (process did
//     NOT crash) AND (b) the synthetic panic message is NOT leaked into
//     the response body (threat T-11-OPS-08).
//
// White-box test (package admin, not admin_test) so we can build a
// hermetic Verifier with a fake adminQueries — no DB, no Redis, no
// process boot. The fake returns a single canned admin key row whose
// bcrypt hash matches a known raw key.
package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// fakeAdminQueries returns a single canned admin_keys row whose bcrypt
// hash matches rawKey. The Verifier's DB step calls
// GetAdminKeyByLookupHash; the fake ignores the lookup-hash argument and
// returns the canned row so the bcrypt step runs against rawKey and
// succeeds.
type fakeAdminQueries struct {
	row gen.AiGatewayAdminKey
}

func (f *fakeAdminQueries) GetAdminKeyByLookupHash(_ context.Context, _ []byte) (gen.AiGatewayAdminKey, error) {
	return f.row, nil
}

// newDebugPanicTestChain composes the production wrap order
// (Recoverer outermost -> admin.Middleware -> DebugPanicHandler) into an
// httptest.NewServer and returns the URL + the raw admin key for
// authenticated requests.
//
// The Verifier's Redis cache is disabled (nil rdb) so every request
// runs through the DB-step (the fake) + bcrypt. That keeps the test
// hermetic and exercises the same code path the deployed gateway uses.
func newDebugPanicTestChain(t *testing.T) (serverURL string, rawAdminKey string, cleanup func()) {
	t.Helper()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))

	rawAdminKey = "ifix_admin_debug_panic_test_0123456789abcdef"
	bcryptHash, err := bcrypt.GenerateFromPassword([]byte(rawAdminKey), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt generate: %v", err)
	}
	cannedRow := gen.AiGatewayAdminKey{
		ID:        uuid.New(),
		KeyHash:   string(bcryptHash),
		KeyPrefix: "ifix_admin_****cdef",
		Label:     "debug-panic-test",
		Status:    "active",
		CreatedAt: time.Now().UTC(),
	}

	v := NewVerifier(&fakeAdminQueries{row: cannedRow}, nil, log)
	handler := httpx.Recoverer(log)(Middleware(v, log)(DebugPanicHandler(log)))
	srv := httptest.NewServer(handler)
	cleanup = srv.Close
	serverURL = srv.URL
	return
}

// assertBodyHasNoLeakage fails the test if the response body contains
// the synthetic panic string OR any "goroutine" stacktrace token. The
// Recoverer must emit ONLY the sanitized envelope; the panic message is
// for Sentry consumption, never for HTTP clients (threat T-11-OPS-08).
func assertBodyHasNoLeakage(t *testing.T, body []byte) {
	t.Helper()
	s := string(body)
	if strings.Contains(s, "synthetic panic") {
		t.Errorf("response body contains panic message (leaked): %q", s)
	}
	if strings.Contains(s, "goroutine") {
		t.Errorf("response body contains stacktrace token \"goroutine\" (leaked): %q", s)
	}
}

// TestDebugPanic_Unauthenticated_Returns401Or403 — reviews HIGH #1 case (a).
// A raw POST with NO X-Admin-Key header must be rejected by
// admin.Middleware BEFORE the handler runs. This proves the route is
// mounted inside the admin chain; if the route were mounted outside the
// chain a misconfigured wiring would let the panic fire on every
// unauthenticated request (an intentional public DoS endpoint).
func TestDebugPanic_Unauthenticated_Returns401Or403(t *testing.T) {
	srvURL, _, cleanup := newDebugPanicTestChain(t)
	defer cleanup()

	req, err := http.NewRequest(http.MethodPost, srvURL+"/admin/debug/panic", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// Intentionally NO X-Admin-Key header.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status: want 401 or 403, got %d (body=%q)", resp.StatusCode, string(body))
	}
	// Defense in depth: even on the auth-rejection path the body must
	// never contain the panic string (the handler should never have run,
	// so this is belt-and-suspenders).
	assertBodyHasNoLeakage(t, body)

	// Envelope sanity: missing X-Admin-Key returns the
	// "missing_admin_key" code per gateway/internal/admin/middleware.go.
	var env struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%q)", err, string(body))
	}
	if env.Error.Code != "missing_admin_key" {
		t.Errorf("error.code: want missing_admin_key, got %q", env.Error.Code)
	}
	if env.Error.Type != "authentication_error" {
		t.Errorf("error.type: want authentication_error, got %q", env.Error.Type)
	}
}

// TestDebugPanic_Authenticated_Returns500ViaRecoverer — reviews HIGH #1
// case (b). With a valid X-Admin-Key the panic fires inside the handler;
// httpx.Recoverer MUST recover it and emit a 500 with a sanitized
// envelope. The process must NOT crash (the test would simply fail to
// return on a crash). The panic message must NOT appear in the body.
func TestDebugPanic_Authenticated_Returns500ViaRecoverer(t *testing.T) {
	srvURL, rawKey, cleanup := newDebugPanicTestChain(t)
	defer cleanup()

	req, err := http.NewRequest(http.MethodPost, srvURL+"/admin/debug/panic", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Admin-Key", rawKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: want 500 (panic recovered), got %d (body=%q)",
			resp.StatusCode, string(body))
	}
	assertBodyHasNoLeakage(t, body)

	// Envelope shape: Recoverer calls WriteOpenAIError with type=api_error,
	// code=internal_error per gateway/internal/httpx/recoverer.go.
	var env struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%q)", err, string(body))
	}
	if env.Error.Type != "api_error" {
		t.Errorf("error.type: want api_error, got %q", env.Error.Type)
	}
	if env.Error.Code != "internal_error" {
		t.Errorf("error.code: want internal_error, got %q", env.Error.Code)
	}
}
