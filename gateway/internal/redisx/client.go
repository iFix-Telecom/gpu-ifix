// Package redisx wraps redis.Client construction with the gateway's
// standard namespace convention (keys prefixed `gw:*`) and a fail-fast
// Ping at startup. One shared client per gateway process.
package redisx

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
)

// NewClient connects to cfg.RedisAddr and Ping()s with a 2s budget.
// Password is optional (CONTEXT.md: Redis Ifix may run without auth).
func NewClient(ctx context.Context, cfg config.Config) (*redis.Client, error) {
	rc := redis.NewClient(&redis.Options{
		Addr:         cfg.RedisAddr,
		Password:     cfg.RedisPassword,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := rc.Ping(pingCtx).Result(); err != nil {
		_ = rc.Close()
		return nil, fmt.Errorf("redisx: ping %s: %w", cfg.RedisAddr, err)
	}
	return rc, nil
}
