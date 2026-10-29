package proxy

// Card 17u65kmhj0r: evento de contexto longo publicado só para chat com
// entrada > 100k num upstream com degrau 5× (Haiku direto ou Claude via
// OpenRouter).

import (
	"fmt"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

func longCtxBody(model string, prompt int) string {
	return fmt.Sprintf(`{"model":%q,"usage":{"prompt_tokens":%d,"completion_tokens":10,"total_tokens":%d}}`,
		model, prompt, prompt+10)
}

func runLongCtx(t *testing.T, upstream, route, model string, prompt int) (redisx.LongContextEvent, bool) {
	t.Helper()
	ix, capt := newCostInterceptor(haikuPrices(true))
	got := make(chan redisx.LongContextEvent, 1)
	ix.SetLongContextPublisher(func(ev redisx.LongContextEvent) { got <- ev })
	drive(t, ix, capt, usageResp("application/json", longCtxBody(model, prompt), upstream, route))
	select {
	case ev := <-got:
		return ev, true
	case <-time.After(200 * time.Millisecond):
		return redisx.LongContextEvent{}, false
	}
}

func TestLongContextPublishedForHaikuAbove100k(t *testing.T) {
	ev, ok := runLongCtx(t, "anthropic-haiku", "chat", "claude-haiku-5-5", 120000)
	if !ok {
		t.Fatal("want long-context event, got none")
	}
	if ev.TokensIn != 120000 || ev.Upstream != "anthropic-haiku" || ev.Model != "claude-haiku-5-5" || ev.RequestID == "" {
		t.Errorf("unexpected event: %+v", ev)
	}
}

func TestLongContextPublishedForClaudeViaOpenRouter(t *testing.T) {
	if _, ok := runLongCtx(t, "openrouter-chat", "chat", "anthropic/claude-haiku-5.5", 150000); !ok {
		t.Fatal("want event for Claude via OpenRouter")
	}
}

func TestLongContextNotPublished(t *testing.T) {
	cases := []struct {
		name, upstream, route, model string
		prompt                       int
	}{
		{"at threshold", "anthropic-haiku", "chat", "claude-haiku-5-5", 100000},
		{"deepseek no 5x step", "openrouter-deepseek", "chat", "deepseek/deepseek-v4.1-flash", 300000},
		{"not chat route", "anthropic-haiku", "embed", "claude-haiku-5-5", 200000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if ev, ok := runLongCtx(t, c.upstream, c.route, c.model, c.prompt); ok {
				t.Errorf("want no event, got %+v", ev)
			}
		})
	}
}
