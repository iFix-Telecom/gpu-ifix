// Package httpx (recoverer_test.go): covers the Recoverer + Logger chain for
// client disconnects (http.ErrAbortHandler) vs ordinary panics.
package httpx

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a goroutine-safe bytes.Buffer (httptest.NewServer handlers
// log from server goroutines).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestChain(buf io.Writer, h http.Handler) http.Handler {
	base := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return RequestID(Logger(base)(Recoverer(base)(h)))
}

func newStdLogger(w io.Writer) *log.Logger { return log.New(w, "", 0) }

func serveRecover(h http.Handler, w http.ResponseWriter, r *http.Request) (rec any) {
	defer func() { rec = recover() }()
	h.ServeHTTP(w, r)
	return nil
}

func parseRecords(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func recordsByMsg(recs []map[string]any, msg string) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if r["msg"] == msg {
			out = append(out, r)
		}
	}
	return out
}

func TestRecoverer_ClientAbort(t *testing.T) {
	var buf bytes.Buffer
	const chunk = "data: partial\n\n"
	h := newTestChain(&buf, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(chunk))
		panic(http.ErrAbortHandler)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	rec := serveRecover(h, rr, req)
	if rec != http.ErrAbortHandler {
		t.Fatalf("recovered = %v, want http.ErrAbortHandler", rec)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "internal_error") {
		t.Fatalf("body contains envelope: %q", rr.Body.String())
	}

	recs := parseRecords(t, buf.String())
	warns := recordsByMsg(recs, "client_disconnected")
	if len(warns) != 1 {
		t.Fatalf("client_disconnected records = %d, want 1; log=%s", len(warns), buf.String())
	}
	w := warns[0]
	if w["level"] != "WARN" {
		t.Fatalf("level = %v, want WARN", w["level"])
	}
	if s, _ := w["request_id"].(string); s == "" {
		t.Fatalf("request_id empty: %v", w)
	}
	if w["method"] != http.MethodPost || w["path"] != "/v1/chat/completions" {
		t.Fatalf("method/path = %v %v", w["method"], w["path"])
	}
	if n := len(recordsByMsg(recs, "panic recovered")); n != 0 {
		t.Fatalf("panic recovered records = %d, want 0", n)
	}
	reqs := recordsByMsg(recs, "request")
	if len(reqs) != 1 {
		t.Fatalf("request records = %d, want 1", len(reqs))
	}
	if reqs[0]["status"] != float64(200) {
		t.Fatalf("status = %v, want 200", reqs[0]["status"])
	}
	if reqs[0]["aborted"] != true {
		t.Fatalf("aborted = %v, want true", reqs[0]["aborted"])
	}
	if reqs[0]["bytes"] != float64(len(chunk)) {
		t.Fatalf("bytes = %v, want %d", reqs[0]["bytes"], len(chunk))
	}
}

func TestRecoverer_ClientAbortBeforeHeaders(t *testing.T) {
	var buf bytes.Buffer
	h := newTestChain(&buf, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	rec := serveRecover(h, rr, req)
	if rec != http.ErrAbortHandler {
		t.Fatalf("recovered = %v, want http.ErrAbortHandler", rec)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rr.Body.String())
	}
	recs := parseRecords(t, buf.String())
	if n := len(recordsByMsg(recs, "client_disconnected")); n != 1 {
		t.Fatalf("client_disconnected records = %d, want 1", n)
	}
	reqs := recordsByMsg(recs, "request")
	if len(reqs) != 1 || reqs[0]["aborted"] != true {
		t.Fatalf("request records = %v, want 1 with aborted=true", reqs)
	}
}

func TestRecoverer_OrdinaryPanic(t *testing.T) {
	var buf bytes.Buffer
	h := newTestChain(&buf, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	if rec := serveRecover(h, rr, req); rec != nil {
		t.Fatalf("panic escaped: %v", rec)
	}
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "internal_error") {
		t.Fatalf("body missing envelope: %q", rr.Body.String())
	}
	recs := parseRecords(t, buf.String())
	pr := recordsByMsg(recs, "panic recovered")
	if len(pr) != 1 || pr[0]["level"] != "ERROR" {
		t.Fatalf("panic recovered records = %v, want 1 ERROR", pr)
	}
	reqs := recordsByMsg(recs, "request")
	if len(reqs) != 1 {
		t.Fatalf("request records = %d, want 1", len(reqs))
	}
	if reqs[0]["status"] != float64(500) {
		t.Fatalf("status = %v, want 500", reqs[0]["status"])
	}
	if v, ok := reqs[0]["aborted"]; ok && v != false {
		t.Fatalf("aborted = %v, want absent/false", v)
	}
}

func TestStatusWriter_FirstWriteHeaderWins(t *testing.T) {
	sw := &statusWriter{ResponseWriter: httptest.NewRecorder(), status: 200}
	sw.WriteHeader(200)
	sw.WriteHeader(500)
	if sw.status != 200 {
		t.Fatalf("status = %d, want 200", sw.status)
	}
	sw2 := &statusWriter{ResponseWriter: httptest.NewRecorder(), status: 200}
	_, _ = sw2.Write([]byte("x"))
	sw2.WriteHeader(502)
	if sw2.status != 200 {
		t.Fatalf("implicit-200 status = %d, want 200", sw2.status)
	}
}

// TestRecoverer_ClientAbortOverRealServer exercises the full net/http stack:
// after headers + a chunk are flushed, the handler panics ErrAbortHandler.
// The client must see the body as truncated (read error, not a clean EOF),
// and net/http must not log a panic stack (it silently swallows
// ErrAbortHandler).
func TestRecoverer_ClientAbortOverRealServer(t *testing.T) {
	var logBuf syncBuffer
	var errLog syncBuffer
	done := make(chan struct{})
	h := newTestChain(&logBuf, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: partial\n\n"))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = newStdLogger(&errLog)
	srv.Start()
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, rerr := io.ReadAll(resp.Body)
	if rerr == nil {
		t.Fatalf("read body ended cleanly (%q); want truncated/aborted error", body)
	}
	t.Logf("client read error (connection aborted): %v (unexpected EOF: %v)",
		rerr, errors.Is(rerr, io.ErrUnexpectedEOF))
	if string(body) != "data: partial\n\n" {
		t.Fatalf("body = %q, want the partial chunk", body)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not finish")
	}
	// Give net/http's conn goroutine a moment to (not) log.
	time.Sleep(50 * time.Millisecond)
	if s := errLog.String(); strings.Contains(s, "panic") {
		t.Fatalf("server logged a panic: %s", s)
	}
	recs := parseRecords(t, logBuf.String())
	if n := len(recordsByMsg(recs, "client_disconnected")); n != 1 {
		t.Fatalf("client_disconnected records = %d, want 1", n)
	}
	if n := len(recordsByMsg(recs, "panic recovered")); n != 0 {
		t.Fatalf("panic recovered records = %d, want 0", n)
	}
}
