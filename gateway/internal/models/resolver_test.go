package models

import (
	"io"
	"log/slog"
	"sync"
	"testing"
)

// newResolverFromMap builds a Resolver with a preloaded alias map without
// touching Postgres. Exercises Resolve directly.
func newResolverFromMap(m map[aliasKey]string) *Resolver {
	r := &Resolver{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		aliases: m,
	}
	return r
}

func TestResolver_ResolveKnownAlias(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "llm"}: "qwen-v1",
	})
	if got := r.Resolve("qwen", "llm"); got != "qwen-v1" {
		t.Fatalf("Resolve(qwen,llm)=%q; want qwen-v1", got)
	}
}

func TestResolver_ResolveUnknownAliasPassesThrough(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{})
	if got := r.Resolve("gpt-5", "llm"); got != "gpt-5" {
		t.Fatalf("Resolve(gpt-5,llm)=%q; want gpt-5 (pass-through)", got)
	}
}

func TestResolver_SameAliasDifferentUpstreams(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "llm"}:   "qwen-llm-target",
		{"qwen", "embed"}: "qwen-embed-target",
	})
	if got := r.Resolve("qwen", "llm"); got != "qwen-llm-target" {
		t.Errorf("llm target=%q", got)
	}
	if got := r.Resolve("qwen", "embed"); got != "qwen-embed-target" {
		t.Errorf("embed target=%q", got)
	}
}

func TestResolver_ConcurrentRefreshSafe(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "llm"}: "qwen-v1",
	})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = r.Resolve("qwen", "llm")
				}
			}
		}()
	}
	// Concurrent writer simulating Refresh swapping the map.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			select {
			case <-stop:
				return
			default:
				r.mu.Lock()
				r.aliases = map[aliasKey]string{{"qwen", "llm"}: "qwen-vX"}
				r.mu.Unlock()
			}
		}
	}()
	close(stop)
	wg.Wait()
}
