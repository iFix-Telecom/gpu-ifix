//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// TestIntegration_Migration0038_URLOverrideHotReload — quick 260930-uru.
// UPDATE url_override must fire the 0038 trigger, the LISTEN must Refresh the
// loader, TargetURL must follow the override and OnURLChange must fire. NULL
// reverts to the env URL.
func TestIntegration_Migration0038_URLOverrideHotReload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	resetUpstreamsTable(t, ctx, pool)

	clearUpstreamEnvs(t)
	t.Setenv("UPSTREAM_LLM_URL", "http://local-llm:8000")
	t.Setenv("UPSTREAM_EMBED_URL", "http://local-embed:8002")

	loader, err := upstreams.NewLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	if u, ok := loader.TargetURL("local-llm"); !ok || u.Host != "local-llm:8000" {
		t.Fatalf("TargetURL before override = %v ok=%v", u, ok)
	}
	if sharedPGDSN == "" {
		t.Skip("sharedPGDSN not set")
	}

	var mu sync.Mutex
	var changes [][3]string
	loader.OnURLChange(func(name, oldURL, newURL string) {
		mu.Lock()
		changes = append(changes, [3]string{name, oldURL, newURL})
		mu.Unlock()
	})

	listenCtx, listenCancel := context.WithCancel(ctx)
	defer listenCancel()
	go func() {
		_ = upstreams.ListenAndReload(listenCtx, sharedPGDSN, loader, func() {}, discardLogger())
	}()
	time.Sleep(500 * time.Millisecond)

	waitHost := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if u, ok := loader.TargetURL("local-llm"); ok && u.Host == want {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		u, _ := loader.TargetURL("local-llm")
		t.Fatalf("TargetURL host = %v, want %s within 5s", u, want)
	}

	if _, err := pool.Exec(ctx,
		"UPDATE ai_gateway.upstreams SET url_override = 'http://10.1.1.1:9000' WHERE name = 'local-llm'"); err != nil {
		t.Fatalf("UPDATE override: %v", err)
	}
	waitHost("10.1.1.1:9000")

	mu.Lock()
	got := append([][3]string(nil), changes...)
	mu.Unlock()
	if len(got) != 1 || got[0] != [3]string{"local-llm", "http://local-llm:8000", "http://10.1.1.1:9000"} {
		t.Fatalf("OnURLChange calls = %v", got)
	}

	if _, err := pool.Exec(ctx,
		"UPDATE ai_gateway.upstreams SET url_override = NULL WHERE name = 'local-llm'"); err != nil {
		t.Fatalf("UPDATE clear: %v", err)
	}
	waitHost("local-llm:8000")
}

// TestIntegration_Migration0038_DownUp — 0038 Down drops the column and
// restores the 0009 trigger; Up re-adds both.
func TestIntegration_Migration0038_DownUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	colExists := func() bool {
		var n int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema='ai_gateway' AND table_name='upstreams' AND column_name='url_override'`).Scan(&n); err != nil {
			t.Fatalf("columns query: %v", err)
		}
		return n == 1
	}
	if !colExists() {
		t.Fatal("url_override missing after Up")
	}
	if err := db.Down(ctx, pool, 1); err != nil {
		t.Fatalf("db.Down(1) revert 0038: %v", err)
	}
	if colExists() {
		t.Fatal("url_override still present after 0038 Down")
	}
	var trig int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM pg_trigger
		WHERE tgname='upstreams_update_notify' AND NOT tgisinternal`).Scan(&trig); err != nil {
		t.Fatalf("trigger query: %v", err)
	}
	if trig != 1 {
		t.Fatalf("upstreams_update_notify count after Down = %d, want 1", trig)
	}
	if err := db.Up(ctx, pool); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	if !colExists() {
		t.Fatal("url_override missing after re-Up")
	}
}
