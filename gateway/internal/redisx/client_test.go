package redisx

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
)

func TestNewClient_FailsOnUnreachable(t *testing.T) {
	cfg := config.Config{RedisAddr: "127.0.0.1:1"} // port 1 unrouteable
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := NewClient(ctx, cfg)
	if err == nil {
		t.Fatal("expected error on unreachable addr")
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("NewClient took %v, want <4s (2s ping budget)", time.Since(start))
	}
}

func TestNewClient_SucceedsAgainstMiniredis(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	cfg := config.Config{RedisAddr: mr.Addr()}
	c, err := NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()
	if _, err := c.Ping(context.Background()).Result(); err != nil {
		t.Fatalf("ping: %v", err)
	}
}
