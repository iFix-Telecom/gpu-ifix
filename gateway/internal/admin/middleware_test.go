package admin_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/admin"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// TestAdminMiddleware_MissingHeader_401: no X-Admin-Key → 401 +
// "missing_admin_key". Exercises the contract without any DB round-trip.
func TestAdminMiddleware_MissingHeader_401(t *testing.T) {
	// Verifier without DB — every Verify call on empty key hits the
	// missing-key path before the DB lookup.
	v := admin.NewVerifier(nil, nil, discardLog())
	h := admin.Middleware(v, discardLog())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("inner handler should not run")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/usage", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if body.Error.Code != "missing_admin_key" {
		t.Errorf("error.code: want missing_admin_key, got %q", body.Error.Code)
	}
	if body.Error.Type != "authentication_error" {
		t.Errorf("error.type: want authentication_error, got %q", body.Error.Type)
	}
}

// TestAdminMiddleware_InvalidKey_401: X-Admin-Key present but DB returns
// no row (nil queries → ErrInvalidAdminKey per defensive branch). 401 +
// "invalid_admin_key".
func TestAdminMiddleware_InvalidKey_401(t *testing.T) {
	v := admin.NewVerifier(nil, nil, discardLog())
	h := admin.Middleware(v, discardLog())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("inner handler should not run")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/usage", nil)
	req.Header.Set("X-Admin-Key", "ifix_admin_nonexistent")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "invalid_admin_key" {
		t.Errorf("error.code: want invalid_admin_key, got %q", body.Error.Code)
	}
}

// TestAdminMiddleware_FromContextReturnsFalseOnUnauthed — FromContext on
// a stock context (no middleware injection) returns (_, false).
func TestAdminMiddleware_FromContextReturnsFalseOnUnauthed(t *testing.T) {
	req := httptest.NewRequest("GET", "/admin/usage", nil)
	if _, ok := admin.FromContext(req.Context()); ok {
		t.Error("FromContext should return ok=false without middleware")
	}
}
