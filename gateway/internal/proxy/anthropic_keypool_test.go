package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// Phase 23-05 (HAIKU-02): pool de chaves das contas Max. Chaves de teste são
// fictícias (sk-test-*); nenhuma chave real existe ainda — tudo roda contra um
// fake da Anthropic em httptest + miniredis.

const (
	testKeyA = "sk-test-a-0123456789"
	testKeyB = "sk-test-b-9876543210"
)

func testFP(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:8]
}

func spLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	return loc
}

// ---------------------------------------------------------------------------
// Respostas do fake Anthropic
// ---------------------------------------------------------------------------

const (
	bodyCreditLow  = `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`
	bodyUsageLimit = `{"type":"error","error":{"type":"invalid_request_error","message":"You have reached your specified API usage limits. You will regain access on 2026-11-01 at 00:00 UTC."}}`
	bodySpendLimit = `{"type":"error","error":{"type":"rate_limit_error","message":"Spend limit reached","details":{"error_code":"enforced_spend_limit_reached"}}}`
	bodyRateLimit  = `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`
	bodyOverloaded = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	bodyAuth       = `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	bodyPrefill    = `{"type":"error","error":{"type":"invalid_request_error","message":"This model does not support assistant message prefill. The conversation must end with a user message."}}`
	bodyOK         = `{"id":"msg_01","type":"message","content":[{"type":"text","text":"ok"}]}`
)

type fakeReply struct {
	status int
	body   string
	header map[string]string
}

var (
	replyOK        = fakeReply{status: 200, body: bodyOK}
	replyCreditLow = fakeReply{status: 400, body: bodyCreditLow}
	replyAuth      = fakeReply{status: 401, body: bodyAuth}
	replyOverload  = fakeReply{status: 529, body: bodyOverloaded}
)

type fakeCall struct {
	key    string
	header http.Header
	body   []byte
}

// fakeAnthropic decide a resposta pelo header x-api-key recebido.
type fakeAnthropic struct {
	mu      sync.Mutex
	replies map[string]fakeReply
	calls   []fakeCall
	srv     *httptest.Server
}

func newFakeAnthropic(t *testing.T) *fakeAnthropic {
	t.Helper()
	f := &fakeAnthropic{replies: map[string]fakeReply{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := r.Header.Get("x-api-key")
		f.mu.Lock()
		f.calls = append(f.calls, fakeCall{key: key, header: r.Header.Clone(), body: b})
		rep, ok := f.replies[key]
		f.mu.Unlock()
		if !ok {
			rep = fakeReply{status: 401, body: bodyAuth}
		}
		for k, v := range rep.header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAnthropic) set(key string, rep fakeReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[key] = rep
}

func (f *fakeAnthropic) takeCalls() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func callKeys(calls []fakeCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.key)
	}
	return out
}

// ---------------------------------------------------------------------------
// Ambiente de teste
// ---------------------------------------------------------------------------

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type kpEnv struct {
	t     *testing.T
	fake  *fakeAnthropic
	mr    *miniredis.Miniredis
	rdb   *redis.Client
	cur   time.Time
	loc   *time.Location
	pool  *KeyPool
	rt    http.RoundTripper
	logs  *syncBuf
	keys  []AnthropicKey
	clkMu sync.Mutex
}

func (e *kpEnv) now() time.Time {
	e.clkMu.Lock()
	defer e.clkMu.Unlock()
	return e.cur
}

func (e *kpEnv) advance(d time.Duration) {
	e.clkMu.Lock()
	defer e.clkMu.Unlock()
	e.cur = e.cur.Add(d)
}

func newKPEnv(t *testing.T, raw string) *kpEnv {
	t.Helper()
	e := &kpEnv{t: t, loc: spLoc(t), logs: &syncBuf{}}
	e.cur = time.Date(2026, 10, 15, 10, 0, 0, 0, e.loc)
	e.fake = newFakeAnthropic(t)
	e.mr = miniredis.RunT(t)
	e.rdb = redis.NewClient(&redis.Options{
		Addr:         e.mr.Addr(),
		MaxRetries:   -1,
		DialTimeout:  200 * time.Millisecond,
		ReadTimeout:  200 * time.Millisecond,
		WriteTimeout: 200 * time.Millisecond,
	})
	t.Cleanup(func() { _ = e.rdb.Close() })
	keys, err := ParseAnthropicKeys(raw)
	if err != nil {
		t.Fatalf("ParseAnthropicKeys: %v", err)
	}
	e.keys = keys
	log := slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.pool = NewKeyPool(keys, e.rdb, e.now, e.loc, log)
	e.rt = NewAnthropicKeyPoolTransport(http.DefaultTransport, e.pool, log)
	return e
}

const testReqBody = `{"model":"claude-haiku-5-5","max_tokens":16,"messages":[{"role":"user","content":"oi"}]}`

func (e *kpEnv) do() (*http.Response, error) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.fake.srv.URL+"/v1/messages", strings.NewReader(testReqBody))
	if err != nil {
		e.t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer ifix_sk_cliente")
	req.Header.Set("Content-Type", "application/json")
	return e.rt.RoundTrip(req)
}

func (e *kpEnv) mustDo() *http.Response {
	e.t.Helper()
	resp, err := e.do()
	if err != nil {
		e.t.Fatalf("RoundTrip: %v", err)
	}
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

type redisKeyState struct {
	State       string     `json:"state"`
	Until       *time.Time `json:"until"`
	NextProbeAt *time.Time `json:"next_probe_at"`
	Reason      string     `json:"reason"`
}

func (e *kpEnv) redisState(secret string) (redisKeyState, bool) {
	e.t.Helper()
	raw, err := e.mr.Get(AnthropicKeyStatePrefix + testFP(secret))
	if err != nil {
		return redisKeyState{}, false
	}
	var st redisKeyState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		e.t.Fatalf("redis state JSON inválido: %v (%s)", err, raw)
	}
	return st, true
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func assertKeys(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("chaves chamadas = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Parse
// ---------------------------------------------------------------------------

func TestParseAnthropicKeys(t *testing.T) {
	t.Run("valid_two_keys_with_and_without_day", func(t *testing.T) {
		keys, err := ParseAnthropicKeys("max-pedro=sk-a@17,max-b=sk-b")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(keys) != 2 {
			t.Fatalf("len = %d, want 2", len(keys))
		}
		if keys[0].Label != "max-pedro" || keys[0].RenewalDay != 17 || keys[0].Fingerprint != testFP("sk-a") {
			t.Fatalf("keys[0] = %+v", keys[0])
		}
		if keys[1].Label != "max-b" || keys[1].RenewalDay != 0 || keys[1].Fingerprint != testFP("sk-b") {
			t.Fatalf("keys[1] = %+v", keys[1])
		}
		if s := keys[0].String(); s != "max-pedro("+testFP("sk-a")+")" || strings.Contains(s, "sk-a") {
			t.Fatalf("String() = %q", s)
		}
	})

	t.Run("empty_is_nil_nil", func(t *testing.T) {
		keys, err := ParseAnthropicKeys("  ")
		if err != nil || keys != nil {
			t.Fatalf("got (%v, %v), want (nil, nil)", keys, err)
		}
	})

	for _, raw := range []string{"max-pedro=", "semigual", "max-x=sk-secretvalue@99", "=sk-secretvalue"} {
		raw := raw
		t.Run("malformed_does_not_echo_"+strings.ReplaceAll(raw, "=", "_"), func(t *testing.T) {
			_, err := ParseAnthropicKeys(raw)
			if err == nil {
				t.Fatalf("ParseAnthropicKeys(%q) err = nil, want erro", raw)
			}
			msg := err.Error()
			if i := strings.Index(raw, "="); i >= 0 && i+1 < len(raw) {
				if strings.Contains(msg, raw[i+1:]) || strings.Contains(msg, "secretvalue") {
					t.Fatalf("mensagem ecoa a chave: %q", msg)
				}
			}
			if strings.Contains(msg, "semigual") {
				t.Fatalf("mensagem ecoa a entrada: %q", msg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Classificador
// ---------------------------------------------------------------------------

func TestClassifyAnthropicKeyError(t *testing.T) {
	hdr := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i+1 < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	cases := []struct {
		name   string
		status int
		body   string
		h      http.Header
		want   keyVerdict
		dur    time.Duration
	}{
		{"credit_low_400", 400, bodyCreditLow, nil, keyExhausted, 0},
		{"credit_low_any_4xx_403", 403, bodyCreditLow, nil, keyExhausted, 0},
		{"payment_required_402", 402, `whatever`, nil, keyExhausted, 0},
		{"spend_limit_429", 429, bodySpendLimit, nil, keyExhausted, 0},
		{"usage_limit_400", 400, bodyUsageLimit, nil, keyExhausted, 0},
		{"unauthorized_401", 401, bodyAuth, nil, keyRevoked, 0},
		{"rate_limit_retry_after_12", 429, bodyRateLimit, hdr("retry-after", "12"), keyCooldown, 12 * time.Second},
		{"rate_limit_no_header_30s", 429, bodyRateLimit, nil, keyCooldown, 30 * time.Second},
		{"server_500", 500, `{"type":"error","error":{"type":"api_error","message":"boom"}}`, nil, notKeyProblem, 0},
		{"overloaded_529", 529, bodyOverloaded, nil, notKeyProblem, 0},
		{"timeout_504", 504, `gateway timeout`, nil, notKeyProblem, 0},
		{"prefill_validation_400", 400, bodyPrefill, nil, notKeyProblem, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h := tc.h
			if h == nil {
				h = http.Header{}
			}
			v, d := classifyAnthropicKeyError(tc.status, []byte(tc.body), h)
			if v != tc.want || d != tc.dur {
				t.Fatalf("classify = (%v, %v), want (%v, %v)", v, d, tc.want, tc.dur)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Transporte + Redis
// ---------------------------------------------------------------------------

func TestKeyPool(t *testing.T) {
	t.Run("exhausted_switches_to_next_and_marks_redis_until_renewal_day", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+"@17,max-b="+testKeyB+"@3")
		e.fake.set(testKeyA, replyCreditLow)
		e.fake.set(testKeyB, replyOK)

		resp := e.mustDo()
		if resp.StatusCode != 200 || readBody(t, resp) != bodyOK {
			t.Fatalf("cliente viu status %d, want 200 de B", resp.StatusCode)
		}
		calls := e.fake.takeCalls()
		assertKeys(t, callKeys(calls), testKeyA, testKeyB)

		st, ok := e.redisState(testKeyA)
		if !ok {
			t.Fatalf("Redis sem marcação de A")
		}
		wantUntil := time.Date(2026, 10, 17, 0, 0, 0, 0, e.loc)
		if st.State != "exhausted" || st.Until == nil || !st.Until.Equal(wantUntil) {
			t.Fatalf("estado A = %+v, want exhausted until %v", st, wantUntil)
		}
		if st.NextProbeAt == nil || !st.NextProbeAt.Equal(e.now().Add(AnthropicKeyRetestInterval)) {
			t.Fatalf("next_probe_at = %v, want now+6h", st.NextProbeAt)
		}
		if ttl := e.mr.TTL(AnthropicKeyStatePrefix + testFP(testKeyA)); ttl <= 0 {
			t.Fatalf("TTL = %v, want > 0", ttl)
		}
	})

	t.Run("next_request_goes_straight_to_b", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+"@17,max-b="+testKeyB)
		e.fake.set(testKeyA, replyCreditLow)
		e.fake.set(testKeyB, replyOK)
		e.mustDo()
		e.fake.takeCalls()

		e.advance(5 * time.Hour)
		resp := e.mustDo()
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyB)
	})

	t.Run("retest_after_6h_tries_a_first_and_clears_on_200", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+"@17,max-b="+testKeyB)
		e.fake.set(testKeyA, replyCreditLow)
		e.fake.set(testKeyB, replyOK)
		e.mustDo()
		e.fake.takeCalls()

		e.fake.set(testKeyA, replyOK) // saldo voltou
		e.advance(6*time.Hour + time.Minute)
		resp := e.mustDo()
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyA)
		if _, ok := e.redisState(testKeyA); ok {
			t.Fatalf("marcação de A deveria ter sido apagada do Redis")
		}
		snap := e.pool.Snapshot(context.Background())
		if snap[0].State != "ok" {
			t.Fatalf("snapshot A = %+v, want ok", snap[0])
		}
	})

	t.Run("no_renewal_day_exhausted_until_now_plus_6h", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+",max-b="+testKeyB)
		e.fake.set(testKeyA, replyCreditLow)
		e.fake.set(testKeyB, replyOK)
		e.mustDo()
		st, ok := e.redisState(testKeyA)
		if !ok || st.Until == nil || !st.Until.Equal(e.now().Add(6*time.Hour)) {
			t.Fatalf("estado A = %+v, want until now+6h", st)
		}
	})

	t.Run("all_exhausted_returns_retryable_skip_breaker_without_calling", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+"@17,max-b="+testKeyB+"@17")
		e.fake.set(testKeyA, replyCreditLow)
		e.fake.set(testKeyB, replyCreditLow)
		if _, err := e.do(); err == nil {
			t.Fatalf("1ª chamada: err = nil, want erro (as duas esgotaram)")
		}
		e.fake.takeCalls()

		resp, err := e.do()
		if resp != nil {
			_ = resp.Body.Close()
			t.Fatalf("resp != nil")
		}
		if !errors.Is(err, errUpstreamRetryable) || !errors.Is(err, errNoUsableAnthropicKey) {
			t.Fatalf("err = %v, want errUpstreamRetryable + errNoUsableAnthropicKey", err)
		}
		var sb interface{ SkipBreaker() bool }
		if !errors.As(err, &sb) || !sb.SkipBreaker() {
			t.Fatalf("err não expõe SkipBreaker()=true: %v", err)
		}
		if calls := e.fake.takeCalls(); len(calls) != 0 {
			t.Fatalf("fake chamado %d vezes, want 0", len(calls))
		}
	})

	t.Run("overloaded_529_passes_through_without_switching", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+",max-b="+testKeyB)
		e.fake.set(testKeyA, replyOverload)
		e.fake.set(testKeyB, replyOK)
		resp := e.mustDo()
		if resp.StatusCode != 529 || readBody(t, resp) != bodyOverloaded {
			t.Fatalf("status = %d, want 529 intacto", resp.StatusCode)
		}
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyA)
		if _, ok := e.redisState(testKeyA); ok {
			t.Fatalf("529 não pode marcar a chave")
		}
	})

	t.Run("rate_limited_429_cooldown_retry_after", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+",max-b="+testKeyB)
		e.fake.set(testKeyA, fakeReply{status: 429, body: bodyRateLimit, header: map[string]string{"retry-after": "5"}})
		e.fake.set(testKeyB, replyOK)
		resp := e.mustDo()
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyA, testKeyB)
		st, ok := e.redisState(testKeyA)
		if !ok || st.State != "cooldown" || st.Until == nil || !st.Until.Equal(e.now().Add(5*time.Second)) {
			t.Fatalf("estado A = %+v, want cooldown until now+5s", st)
		}
		e.advance(3 * time.Second)
		e.mustDo()
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyB)
		e.fake.set(testKeyA, replyOK)
		e.advance(3 * time.Second)
		e.mustDo()
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyA)
	})

	t.Run("renewal_day_31_clamps_to_last_day_of_30_day_month", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+"@31,max-b="+testKeyB)
		e.cur = time.Date(2026, 11, 10, 9, 0, 0, 0, e.loc) // novembro tem 30 dias
		e.fake.set(testKeyA, replyCreditLow)
		e.fake.set(testKeyB, replyOK)
		e.mustDo()
		st, _ := e.redisState(testKeyA)
		want := time.Date(2026, 11, 30, 0, 0, 0, 0, e.loc)
		if st.Until == nil || !st.Until.Equal(want) {
			t.Fatalf("until = %v, want %v", st.Until, want)
		}
	})

	t.Run("renewal_day_already_passed_rolls_to_next_month", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+"@3,max-b="+testKeyB)
		e.fake.set(testKeyA, replyCreditLow)
		e.fake.set(testKeyB, replyOK)
		e.mustDo()
		st, _ := e.redisState(testKeyA)
		want := time.Date(2026, 11, 3, 0, 0, 0, 0, e.loc)
		if st.Until == nil || !st.Until.Equal(want) {
			t.Fatalf("until = %v, want %v", st.Until, want)
		}
	})

	t.Run("headers_authorization_removed_api_key_and_version_set", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA)
		e.fake.set(testKeyA, replyOK)
		e.mustDo()
		calls := e.fake.takeCalls()
		if len(calls) != 1 {
			t.Fatalf("calls = %d", len(calls))
		}
		h := calls[0].header
		if h.Get("Authorization") != "" {
			t.Fatalf("Authorization vazou para a Anthropic: %q", h.Get("Authorization"))
		}
		if h.Get("x-api-key") != testKeyA || h.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("headers = %v", h)
		}
	})

	t.Run("body_resent_identical_on_each_attempt", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+",max-b="+testKeyB)
		e.fake.set(testKeyA, replyCreditLow)
		e.fake.set(testKeyB, replyOK)
		e.mustDo()
		calls := e.fake.takeCalls()
		if len(calls) != 2 {
			t.Fatalf("calls = %d", len(calls))
		}
		for i, c := range calls {
			if string(c.body) != testReqBody {
				t.Fatalf("tentativa %d body = %q", i, c.body)
			}
		}
	})

	t.Run("redis_down_uses_memory_in_fixed_order_warns_once", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+"@17,max-b="+testKeyB)
		e.mr.Close()
		e.fake.set(testKeyA, replyCreditLow)
		e.fake.set(testKeyB, replyOK)
		for i := 0; i < 3; i++ {
			resp := e.mustDo()
			if resp.StatusCode != 200 {
				t.Fatalf("req %d status = %d", i, resp.StatusCode)
			}
		}
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyA, testKeyB, testKeyB, testKeyB)
		if n := strings.Count(e.logs.String(), "redis indisponível"); n != 1 {
			t.Fatalf("WARN de redis indisponível logado %d vezes, want 1\n%s", n, e.logs.String())
		}
	})

	t.Run("reconcile_rewrites_memory_only_mark_when_redis_returns", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+"@17,max-b="+testKeyB)
		e.mr.Close()
		e.fake.set(testKeyA, replyCreditLow)
		e.fake.set(testKeyB, replyOK)
		e.mustDo() // A esgotada só em memória
		e.fake.takeCalls()

		if err := e.mr.Restart(); err != nil {
			t.Fatalf("miniredis Restart: %v", err)
		}
		e.mustDo()
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyB)
		st, ok := e.redisState(testKeyA)
		if !ok || st.State != "exhausted" {
			t.Fatalf("marcação de A não foi regravada no Redis: %+v ok=%v", st, ok)
		}
		if ttl := e.mr.TTL(AnthropicKeyStatePrefix + testFP(testKeyA)); ttl <= 0 {
			t.Fatalf("TTL = %v, want > 0", ttl)
		}
		if n := strings.Count(e.logs.String(), "redis recuperado"); n != 1 {
			t.Fatalf("WARN de redis recuperado = %d, want 1", n)
		}
		e.mustDo()
		if n := strings.Count(e.logs.String(), "redis recuperado"); n != 1 {
			t.Fatalf("WARN de redis recuperado repetido: %d", n)
		}
	})

	t.Run("redis_only_mark_from_other_replica_is_respected", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+",max-b="+testKeyB)
		e.fake.set(testKeyA, replyOK)
		e.fake.set(testKeyB, replyOK)
		until := e.now().Add(48 * time.Hour)
		probe := e.now().Add(2 * time.Hour)
		raw, _ := json.Marshal(redisKeyState{State: "exhausted", Until: &until, NextProbeAt: &probe, Reason: "outra réplica"})
		e.mr.Set(AnthropicKeyStatePrefix+testFP(testKeyA), string(raw))

		e.mustDo()
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyB)
		snap := e.pool.Snapshot(context.Background())
		if snap[0].State != "exhausted" {
			t.Fatalf("snapshot A = %+v, want exhausted", snap[0])
		}
	})

	t.Run("divergence_memory_vs_redis_most_restrictive_wins", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+",max-b="+testKeyB)
		e.fake.set(testKeyA, replyCreditLow)
		e.fake.set(testKeyB, replyOK)
		e.mustDo() // memória+Redis: next_probe = now+6h
		e.fake.takeCalls()

		// Outra réplica gravou um re-teste mais distante (now+10h).
		until := e.now().Add(20 * time.Hour)
		probe := e.now().Add(10 * time.Hour)
		raw, _ := json.Marshal(redisKeyState{State: "exhausted", Until: &until, NextProbeAt: &probe, Reason: "outra réplica"})
		e.mr.Set(AnthropicKeyStatePrefix+testFP(testKeyA), string(raw))

		e.fake.set(testKeyA, replyOK)
		e.advance(7 * time.Hour) // passou o próprio re-teste, não o do Redis
		e.mustDo()
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyB)

		// Caso inverso: Redis menos restritivo (marcação apagada) → memória vale.
		e2 := newKPEnv(t, "max-a="+testKeyA+",max-b="+testKeyB)
		e2.fake.set(testKeyA, replyCreditLow)
		e2.fake.set(testKeyB, replyOK)
		e2.mustDo()
		e2.fake.takeCalls()
		short := e2.now().Add(time.Minute)
		raw2, _ := json.Marshal(redisKeyState{State: "exhausted", Until: &short, NextProbeAt: &short, Reason: "x"})
		e2.mr.Set(AnthropicKeyStatePrefix+testFP(testKeyA), string(raw2))
		e2.advance(2 * time.Hour)
		e2.mustDo()
		assertKeys(t, callKeys(e2.fake.takeCalls()), testKeyB)
	})

	t.Run("revoked_401_retested_every_24h", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+"@17,max-b="+testKeyB)
		e.fake.set(testKeyA, replyAuth)
		e.fake.set(testKeyB, replyOK)
		e.mustDo()
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyA, testKeyB)
		st, ok := e.redisState(testKeyA)
		if !ok || st.State != "revoked" || st.Until != nil {
			t.Fatalf("estado A = %+v, want revoked sem until", st)
		}
		if st.NextProbeAt == nil || !st.NextProbeAt.Equal(e.now().Add(24*time.Hour)) {
			t.Fatalf("next_probe_at = %v, want now+24h", st.NextProbeAt)
		}
		if n := strings.Count(e.logs.String(), "level=ERROR"); n != 1 {
			t.Fatalf("ERROR logs = %d, want 1", n)
		}

		e.advance(12 * time.Hour) // ainda não passou 24h, nem 6h-retest serve
		e.mustDo()
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyB)

		e.advance(12*time.Hour + time.Minute) // passou 24h → re-teste; novo 401
		e.mustDo()
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyA, testKeyB)
		st, _ = e.redisState(testKeyA)
		if st.State != "revoked" || st.NextProbeAt == nil || !st.NextProbeAt.Equal(e.now().Add(24*time.Hour)) {
			t.Fatalf("re-armado = %+v, want revoked next_probe now+24h", st)
		}
		if n := strings.Count(e.logs.String(), "level=ERROR"); n != 2 {
			t.Fatalf("ERROR logs = %d, want 2", n)
		}

		e.fake.set(testKeyA, replyOK)
		e.advance(24*time.Hour + time.Minute)
		e.mustDo()
		assertKeys(t, callKeys(e.fake.takeCalls()), testKeyA)
		if _, ok := e.redisState(testKeyA); ok {
			t.Fatalf("200 no re-teste deveria limpar revoked")
		}
	})

	t.Run("no_log_line_contains_the_secret", func(t *testing.T) {
		e := newKPEnv(t, "max-a="+testKeyA+"@17,max-b="+testKeyB)
		e.fake.set(testKeyA, replyCreditLow)
		e.fake.set(testKeyB, replyAuth)
		_, _ = e.do()
		e.mr.Close()
		e.fake.set(testKeyB, fakeReply{status: 429, body: bodyRateLimit})
		e.advance(25 * time.Hour)
		_, _ = e.do()
		_ = e.mr.Restart()
		e.advance(25 * time.Hour)
		_, err := e.do()
		logs := e.logs.String()
		if logs == "" {
			t.Fatalf("nenhum log capturado — teste não prova nada")
		}
		for _, s := range []string{testKeyA, testKeyB} {
			if strings.Contains(logs, s) {
				t.Fatalf("log contém a chave:\n%s", logs)
			}
			if err != nil && strings.Contains(err.Error(), s) {
				t.Fatalf("erro contém a chave: %v", err)
			}
			for _, k := range e.mr.Keys() {
				v, _ := e.mr.Get(k)
				if strings.Contains(k, s) || strings.Contains(v, s) {
					t.Fatalf("Redis contém a chave: %s=%s", k, v)
				}
			}
		}
		for _, st := range e.pool.Snapshot(context.Background()) {
			if strings.Contains(st.Label+st.Fingerprint+st.Reason, "sk-test") {
				t.Fatalf("snapshot contém a chave: %+v", st)
			}
		}
		if !strings.Contains(logs, "key_fp="+testFP(testKeyA)) || !strings.Contains(logs, "key_label=max-a") {
			t.Fatalf("logs sem key_label/key_fp:\n%s", logs)
		}
	})
}
