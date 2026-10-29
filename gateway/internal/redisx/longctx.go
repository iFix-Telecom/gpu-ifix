// Package redisx (longctx.go): evento de prompt acima do degrau de contexto
// longo (card 17u65kmhj0r). O UsageInterceptor publica um evento por request
// de chat cuja entrada passa de billing.AnthropicLongContextThreshold num
// upstream que cobra 5× acima disso (Haiku direto ou via OpenRouter). O
// alerter consome e manda ClickUp + Brevo, com dedup de 1 h por tenant.
package redisx

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// LongContextEventsChannel é o canal Pub/Sub dos eventos de contexto longo.
const LongContextEventsChannel = "gw:longctx:events"

// LongContextEvent é o payload publicado em LongContextEventsChannel.
type LongContextEvent struct {
	TenantSlug string `json:"tenant_slug"`
	TenantID   string `json:"tenant_id"`
	Upstream   string `json:"upstream"`
	Model      string `json:"model"`
	TokensIn   int64  `json:"tokens_in"`
	RequestID  string `json:"request_id"`
	TSUnix     int64  `json:"ts_unix"`
}

// PublishLongContextEvent publica ev com o timeout padrão de 2 s do pacote.
func PublishLongContextEvent(ctx context.Context, rdb *redis.Client, ev LongContextEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return rdb.Publish(ctx, LongContextEventsChannel, payload).Err()
}
