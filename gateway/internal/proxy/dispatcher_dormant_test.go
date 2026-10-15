package proxy

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// Phase 23-14 — upstream dormente (linha habilitada, sem proxy) não gera WARN
// por request: só o 1º pulo por upstream é WARN, o resto vai para Debug.
func TestLogNoProxySkip_WarnOncePerUpstream(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	name := "dormant-test-" + t.Name()
	for i := 0; i < 5; i++ {
		logNoProxySkip(log, "tier-1 candidate has no registered proxy; skipping", name)
	}
	out := buf.String()
	if got := strings.Count(out, "level=WARN"); got != 1 {
		t.Fatalf("WARN emitidos = %d, want 1; log=%s", got, out)
	}
	if got := strings.Count(out, "level=DEBUG"); got != 4 {
		t.Fatalf("DEBUG emitidos = %d, want 4", got)
	}
	if !strings.Contains(out, "upstream="+name) {
		t.Fatalf("log sem o nome do upstream: %s", out)
	}
}
