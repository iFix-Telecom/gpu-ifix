// Package upstreams (loader_override_test.go): quick 260930-uru — url_override
// precedence, validation fallback, TargetURL and the OnURLChange hook.
package upstreams

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// stubLoaderQueries satisfies loaderQueries with a mutable row set.
type stubLoaderQueries struct {
	mu   sync.Mutex
	rows []gen.ListEnabledUpstreamsRow
}

func (s *stubLoaderQueries) ListEnabledUpstreams(context.Context) ([]gen.ListEnabledUpstreamsRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]gen.ListEnabledUpstreamsRow, len(s.rows))
	copy(out, s.rows)
	return out, nil
}

func (s *stubLoaderQueries) set(rows ...gen.ListEnabledUpstreamsRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = rows
}

type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newStubLoader(t *testing.T, q *stubLoaderQueries, level slog.Level) (*Loader, *logBuf) {
	t.Helper()
	buf := &logBuf{}
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level}))
	return &Loader{q: q, log: log, tier0Override: newTier0OverrideMap()}, buf
}

func row(name, role, urlEnv string, override *string) gen.ListEnabledUpstreamsRow {
	r := gen.ListEnabledUpstreamsRow{
		ID:      uuid.New(),
		Name:    name,
		Role:    role,
		Tier:    0,
		UrlEnv:  urlEnv,
		Enabled: true,
	}
	if override != nil {
		r.UrlOverride = pgtype.Text{String: *override, Valid: true}
	}
	return r
}

func strp(s string) *string { return &s }

func TestValidateUpstreamURL(t *testing.T) {
	for _, ok := range []string{"http://1.2.3.4:5000", "https://h", "http://h:1/x"} {
		if err := ValidateUpstreamURL(ok); err != nil {
			t.Errorf("ValidateUpstreamURL(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "ftp://h", "http://", "1.2.3.4:80", "not a url"} {
		if err := ValidateUpstreamURL(bad); err == nil {
			t.Errorf("ValidateUpstreamURL(%q) = nil, want error", bad)
		}
	}
}

func TestRefresh_OverrideBeatsEnv(t *testing.T) {
	t.Setenv("URU_TEST_STT_URL", "http://env-host:8000")
	q := &stubLoaderQueries{}
	q.set(row("local-stt", "stt", "URU_TEST_STT_URL", strp("http://10.0.0.1:41000")))
	l, _ := newStubLoader(t, q, slog.LevelInfo)
	if err := l.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	u, ok := l.Get("local-stt")
	if !ok || u.URL != "http://10.0.0.1:41000" || u.URLSource != URLSourceOverride {
		t.Fatalf("got %+v ok=%v, want override URL", u, ok)
	}
	tu, ok := l.TargetURL("local-stt")
	if !ok || tu.Host != "10.0.0.1:41000" {
		t.Fatalf("TargetURL = %v ok=%v", tu, ok)
	}
}

func TestRefresh_InvalidOverrideFallsBackToEnv(t *testing.T) {
	t.Setenv("URU_TEST_STT_URL", "http://env-host:8000")
	q := &stubLoaderQueries{}
	q.set(row("local-stt", "stt", "URU_TEST_STT_URL", strp("ftp://nope")))
	l, buf := newStubLoader(t, q, slog.LevelInfo)
	if err := l.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	u, ok := l.Get("local-stt")
	if !ok || u.URL != "http://env-host:8000" || u.URLSource != URLSourceEnv {
		t.Fatalf("got %+v ok=%v, want env URL", u, ok)
	}
	if !strings.Contains(buf.String(), "invalid_url_override") {
		t.Fatalf("expected WARN with status invalid_url_override, log=%s", buf.String())
	}
}

func TestRefresh_OverrideOnlyRowLoads(t *testing.T) {
	q := &stubLoaderQueries{}
	q.set(row("embed-gpu", "embed", "URU_TEST_UNSET_ENV", strp("http://10.0.0.2:7998")))
	l, _ := newStubLoader(t, q, slog.LevelInfo)
	if err := l.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if u, ok := l.Get("embed-gpu"); !ok || u.URL != "http://10.0.0.2:7998" {
		t.Fatalf("override-only row must load, got %+v ok=%v", u, ok)
	}
}

func TestRefresh_NoOverrideNoEnvSkipped(t *testing.T) {
	q := &stubLoaderQueries{}
	q.set(row("rerank-gpu", "rerank", "URU_TEST_UNSET_ENV", nil))
	l, buf := newStubLoader(t, q, slog.LevelInfo)
	if err := l.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Get("rerank-gpu"); ok {
		t.Fatal("row without override and env must be skipped")
	}
	if _, ok := l.TargetURL("rerank-gpu"); ok {
		t.Fatal("TargetURL must be (nil,false) for skipped row")
	}
	if !strings.Contains(buf.String(), "missing_url_env") {
		t.Fatalf("expected missing_url_env WARN, log=%s", buf.String())
	}
}

func TestTargetURL_StablePointerUntilRefresh(t *testing.T) {
	q := &stubLoaderQueries{}
	q.set(row("kokoro-tts", "tts", "URU_TEST_UNSET_ENV", strp("http://10.0.0.3:8021")))
	l, _ := newStubLoader(t, q, slog.LevelInfo)
	if err := l.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, _ := l.TargetURL("kokoro-tts")
	b, _ := l.TargetURL("kokoro-tts")
	if a != b {
		t.Fatal("TargetURL must return the same pointer between refreshes")
	}
	if u, ok := l.TargetURL("missing"); ok || u != nil {
		t.Fatalf("missing name: got %v ok=%v", u, ok)
	}
}

func TestOnURLChange_Hook(t *testing.T) {
	q := &stubLoaderQueries{}
	q.set(row("local-stt", "stt", "URU_TEST_UNSET_ENV", strp("http://a:1")))
	l, buf := newStubLoader(t, q, slog.LevelInfo)

	type call struct{ name, oldURL, newURL string }
	var calls []call
	l.OnURLChange(func(name, oldURL, newURL string) {
		calls = append(calls, call{name, oldURL, newURL})
	})

	ctx := context.Background()
	if err := l.Refresh(ctx); err != nil { // 1st: no previous snapshot
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("first refresh must not call hook, got %v", calls)
	}

	q.set(row("local-stt", "stt", "URU_TEST_UNSET_ENV", strp("http://b:2")))
	if err := l.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != (call{"local-stt", "http://a:1", "http://b:2"}) {
		t.Fatalf("want 1 call A->B, got %v", calls)
	}
	if !strings.Contains(buf.String(), "upstream effective url changed") {
		t.Fatalf("expected INFO url changed, log=%s", buf.String())
	}

	if err := l.Refresh(ctx); err != nil { // unchanged
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("unchanged refresh must not call hook, got %v", calls)
	}

	// New row appears: no hook call for it.
	q.set(
		row("local-stt", "stt", "URU_TEST_UNSET_ENV", strp("http://b:2")),
		row("embed-gpu", "embed", "URU_TEST_UNSET_ENV", strp("http://c:3")),
	)
	if err := l.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("new row must not call hook, got %v", calls)
	}
}

func TestRefresh_UnchangedLogsDebugNotInfo(t *testing.T) {
	q := &stubLoaderQueries{}
	q.set(row("local-stt", "stt", "URU_TEST_UNSET_ENV", strp("http://a:1")))
	l, buf := newStubLoader(t, q, slog.LevelInfo)
	ctx := context.Background()
	if err := l.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(buf.String(), "upstreams refreshed"); got != 1 {
		t.Fatalf("first refresh must log INFO once, got %d: %s", got, buf.String())
	}
	if err := l.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(buf.String(), "upstreams refreshed"); got != 1 {
		t.Fatalf("unchanged refresh must not log at INFO, got %d: %s", got, buf.String())
	}
}
