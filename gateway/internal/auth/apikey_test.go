package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/pkg/openai"
)

// fakeQueries is a controllable stub of authQueries. Counters track
// hot-path call frequency so tests can assert the cache short-circuits.
type fakeQueries struct {
	mu sync.Mutex
	// keyed by hex(lookup_hash) → row to return.
	rows         map[string]gen.GetActiveKeyByLookupHashRow
	lookupCalls  int64
	touchCalls   int64
	forceErr     error
	verifyCalls  int64
	hashOverride map[string]string // raw → hash; if not present, key not known
}

func newFakeQueries() *fakeQueries {
	return &fakeQueries{
		rows:         make(map[string]gen.GetActiveKeyByLookupHashRow),
		hashOverride: make(map[string]string),
	}
}

func (f *fakeQueries) addKey(t *testing.T, raw string, status, dataClass string) gen.GetActiveKeyByLookupHashRow {
	t.Helper()
	hash, err := HashKey(raw)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	id := uuid.New()
	tenantID := uuid.New()
	row := gen.GetActiveKeyByLookupHashRow{
		ID:            id,
		TenantID:      tenantID,
		KeyHash:       hash,
		KeyLookupHash: LookupHash(raw),
		KeyPrefix:     KeyPrefix + "****" + raw[len(raw)-4:],
		Status:        status,
		DataClass:     dataClass,
	}
	f.mu.Lock()
	f.rows[hexLookup(raw)] = row
	f.hashOverride[raw] = hash
	f.mu.Unlock()
	return row
}

func hexLookup(raw string) string {
	h := LookupHash(raw)
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(h)*2)
	for i, b := range h {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return string(out)
}

func (f *fakeQueries) GetActiveKeyByLookupHash(ctx context.Context, lookup []byte) (gen.GetActiveKeyByLookupHashRow, error) {
	atomic.AddInt64(&f.lookupCalls, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forceErr != nil {
		return gen.GetActiveKeyByLookupHashRow{}, f.forceErr
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(lookup)*2)
	for i, b := range lookup {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	row, ok := f.rows[string(out)]
	if !ok {
		return gen.GetActiveKeyByLookupHashRow{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeQueries) TouchKeyLastUsed(ctx context.Context, id uuid.UUID) error {
	atomic.AddInt64(&f.touchCalls, 1)
	return nil
}

func newTestVerifierFull(t *testing.T) (*miniredis.Miniredis, *fakeQueries, *Verifier, *TouchBuffer) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	q := newFakeQueries()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tb := NewTouchBuffer(q.TouchKeyLastUsed, time.Hour, log, nil, nil)
	v := NewVerifierWithQueries(q, rdb, log, tb)
	return mr, q, v, tb
}

func TestExtractKey_AuthorizationWins(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	r.Header.Set("Authorization", "Bearer ifix_sk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	r.Header.Set("X-API-Key", "ifix_sk_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	got := ExtractKey(r)
	if got != "ifix_sk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractKey_XAPIKeyFallback(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	r.Header.Set("X-API-Key", "ifix_sk_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if got := ExtractKey(r); got != "ifix_sk_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractKey_EmptyWhenNoBearer(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	r.Header.Set("Authorization", "Basic deadbeef")
	if got := ExtractKey(r); got != "" {
		t.Fatalf("got %q want empty (Basic auth not Bearer)", got)
	}
}

func TestVerify_MissingReturnsErrMissing(t *testing.T) {
	_, _, v, _ := newTestVerifierFull(t)
	_, err := v.Verify(context.Background(), "")
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("err=%v want ErrMissingAPIKey", err)
	}
}

func TestVerify_MalformedShortCircuits(t *testing.T) {
	_, q, v, _ := newTestVerifierFull(t)
	_, err := v.Verify(context.Background(), "wrong_prefix_foo")
	if !errors.Is(err, ErrMalformedKey) {
		t.Fatalf("err=%v want ErrMalformedKey", err)
	}
	if got := atomic.LoadInt64(&q.lookupCalls); got != 0 {
		t.Fatalf("lookupCalls=%d want 0 (malformed must short-circuit before DB)", got)
	}
}

func TestVerify_ValidKeyFirstCallHitsDB(t *testing.T) {
	_, q, v, _ := newTestVerifierFull(t)
	raw, _, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	q.addKey(t, raw, "active", "normal")
	ac, err := v.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ac.DataClass != DataClassNormal {
		t.Errorf("DataClass=%s want normal", ac.DataClass)
	}
	if ac.TenantID == "" || ac.APIKeyID == "" {
		t.Errorf("AuthContext fields empty: %+v", ac)
	}
	if got := atomic.LoadInt64(&q.lookupCalls); got != 1 {
		t.Errorf("lookupCalls=%d want 1", got)
	}
}

func TestVerify_ValidKeySecondCallHitsCache(t *testing.T) {
	_, q, v, _ := newTestVerifierFull(t)
	raw, _, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	q.addKey(t, raw, "active", "normal")
	ctx := context.Background()
	if _, err := v.Verify(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt64(&q.lookupCalls); got != 1 {
		t.Fatalf("after 1st call lookupCalls=%d want 1", got)
	}
	if _, err := v.Verify(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt64(&q.lookupCalls); got != 1 {
		t.Fatalf("after 2nd call lookupCalls=%d want 1 (cache hit)", got)
	}
}

func TestVerify_UnknownKeyHitsNegativeCache(t *testing.T) {
	mr, q, v, _ := newTestVerifierFull(t)
	raw := "ifix_sk_zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"
	ctx := context.Background()

	if _, err := v.Verify(ctx, raw); !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("err=%v want ErrInvalidAPIKey", err)
	}
	if got := atomic.LoadInt64(&q.lookupCalls); got != 1 {
		t.Fatalf("after 1st call lookupCalls=%d want 1", got)
	}

	if _, err := v.Verify(ctx, raw); !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("err=%v want ErrInvalidAPIKey", err)
	}
	if got := atomic.LoadInt64(&q.lookupCalls); got != 1 {
		t.Fatalf("after 2nd call lookupCalls=%d want 1 (negative-cache hit)", got)
	}

	mr.FastForward(negCacheTTL + time.Second)
	if _, err := v.Verify(ctx, raw); !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("err=%v want ErrInvalidAPIKey", err)
	}
	if got := atomic.LoadInt64(&q.lookupCalls); got != 2 {
		t.Fatalf("after expiry lookupCalls=%d want 2 (cache expired → DB queried again)", got)
	}
}

func TestVerify_SHA256CollisionDefense(t *testing.T) {
	_, q, v, _ := newTestVerifierFull(t)
	rawA, _, _, _, _ := GenerateAPIKey()
	rawB, _, _, _, _ := GenerateAPIKey()
	// Plant a row keyed by sha256(rawB) but whose key_hash is for rawA.
	hashA, err := HashKey(rawA)
	if err != nil {
		t.Fatal(err)
	}
	row := gen.GetActiveKeyByLookupHashRow{
		ID:            uuid.New(),
		TenantID:      uuid.New(),
		KeyHash:       hashA,
		KeyLookupHash: LookupHash(rawB),
		KeyPrefix:     KeyPrefix + "****1234",
		Status:        "active",
		DataClass:     "normal",
	}
	q.mu.Lock()
	q.rows[hexLookup(rawB)] = row
	q.mu.Unlock()

	if _, err := v.Verify(context.Background(), rawB); !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("err=%v want ErrInvalidAPIKey on collision-defense path", err)
	}
	// And next call should hit negative cache (no second DB lookup).
	if _, err := v.Verify(context.Background(), rawB); !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("2nd: err=%v", err)
	}
	if got := atomic.LoadInt64(&q.lookupCalls); got != 1 {
		t.Fatalf("lookupCalls=%d want 1 (neg-cache after collision-defense)", got)
	}
}

func TestVerify_TouchBuffered(t *testing.T) {
	_, q, v, tb := newTestVerifierFull(t)
	raw, _, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	q.addKey(t, raw, "active", "normal")
	for i := 0; i < 100; i++ {
		if _, err := v.Verify(context.Background(), raw); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	if got := tb.PendingCount(); got != 1 {
		t.Fatalf("pending=%d want 1 (coalesced)", got)
	}
}

func TestVerify_RevokedReturnsErrRevoked(t *testing.T) {
	_, q, v, _ := newTestVerifierFull(t)
	raw, _, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	// Revoked active=false rows ARE returned by GetActiveKeyByLookupHash only
	// when status='active'. We simulate a freshly-revoked-after-cache scenario
	// by inserting a row with status="revoked" (but addKey defaults to active
	// so call manually).
	hash, err := HashKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	row := gen.GetActiveKeyByLookupHashRow{
		ID:            uuid.New(),
		TenantID:      uuid.New(),
		KeyHash:       hash,
		KeyLookupHash: LookupHash(raw),
		KeyPrefix:     KeyPrefix + "****" + raw[len(raw)-4:],
		Status:        "revoked",
		DataClass:     "normal",
	}
	q.mu.Lock()
	q.rows[hexLookup(raw)] = row
	q.mu.Unlock()

	_, err = v.Verify(context.Background(), raw)
	if !errors.Is(err, ErrRevokedAPIKey) {
		t.Fatalf("err=%v want ErrRevokedAPIKey", err)
	}
}

func TestMiddleware_NoKey401Envelope(t *testing.T) {
	_, _, v, _ := newTestVerifierFull(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mw := Middleware(v, log)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
	var env openai.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != "no_api_key" {
		t.Errorf("code=%q want no_api_key", env.Error.Code)
	}
	if env.Error.Type != "authentication_error" {
		t.Errorf("type=%q want authentication_error", env.Error.Type)
	}
}

func TestMiddleware_MalformedKey401Code(t *testing.T) {
	_, _, v, _ := newTestVerifierFull(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mw := Middleware(v, log)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	r.Header.Set("Authorization", "Bearer wrong_prefix_foo")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
	var env openai.ErrorResponse
	_ = json.NewDecoder(rec.Body).Decode(&env)
	if env.Error.Code != "malformed_api_key" {
		t.Errorf("code=%q want malformed_api_key", env.Error.Code)
	}
}

func TestMiddleware_ValidKeyCallsNext(t *testing.T) {
	_, q, v, _ := newTestVerifierFull(t)
	raw, _, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	q.addKey(t, raw, "active", "normal")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mw := Middleware(v, log)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac := MustFromContext(r.Context())
		_, _ = w.Write([]byte(ac.TenantID))
	}))
	r := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	r.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if rec.Body.String() == "" {
		t.Fatal("handler did not see AuthContext")
	}
}

func TestMiddleware_EnrichesLogger(t *testing.T) {
	_, q, v, _ := newTestVerifierFull(t)
	raw, _, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	q.addKey(t, raw, "active", "sensitive")

	cap := newCapturingHandler()
	baseLog := slog.New(cap)
	mw := Middleware(v, baseLog)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rl := httpx.LoggerFrom(r.Context())
		rl.Info("inside handler")
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	r.Header.Set("Authorization", "Bearer "+raw)
	// Pre-stash a baseline logger in ctx so middleware enriches it.
	ctx := httpx.WithLogger(r.Context(), baseLog)
	r = r.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	// Look for the "inside handler" record and check its attrs.
	cap.store.mu.Lock()
	defer cap.store.mu.Unlock()
	var found bool
	for _, rec := range cap.store.records {
		if rec.msg != "inside handler" {
			continue
		}
		found = true
		if rec.attrs["tenant_id"] == "" {
			t.Errorf("missing tenant_id attr: %+v", rec.attrs)
		}
		if rec.attrs["api_key_id"] == "" {
			t.Errorf("missing api_key_id attr: %+v", rec.attrs)
		}
		if rec.attrs["data_class"] != "sensitive" {
			t.Errorf("data_class=%q want sensitive", rec.attrs["data_class"])
		}
	}
	if !found {
		t.Fatal("did not capture inside-handler log record")
	}
}

// capturingHandler is a slog.Handler that stores every record + flattened
// attribute map, including those added via WithAttrs. The shared store is
// behind a pointer so WithAttrs/WithGroup can return new handler views
// without copying the mutex.
type capturingHandler struct {
	store  *captureStore
	attrs  []slog.Attr
	groups []string
}

type captureStore struct {
	mu      sync.Mutex
	records []capturedRecord
}

type capturedRecord struct {
	msg   string
	attrs map[string]string
}

func newCapturingHandler() *capturingHandler {
	return &capturingHandler{store: &captureStore{}}
}

func (h *capturingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	flat := make(map[string]string)
	for _, a := range h.attrs {
		flat[a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool {
		flat[a.Key] = a.Value.String()
		return true
	})
	h.store.mu.Lock()
	h.store.records = append(h.store.records, capturedRecord{msg: r.Message, attrs: flat})
	h.store.mu.Unlock()
	return nil
}

func (h *capturingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cp := &capturingHandler{
		store:  h.store,
		attrs:  append(append([]slog.Attr{}, h.attrs...), attrs...),
		groups: append([]string{}, h.groups...),
	}
	return cp
}

func (h *capturingHandler) WithGroup(name string) slog.Handler {
	cp := &capturingHandler{
		store:  h.store,
		attrs:  append([]slog.Attr{}, h.attrs...),
		groups: append(append([]string{}, h.groups...), name),
	}
	return cp
}
