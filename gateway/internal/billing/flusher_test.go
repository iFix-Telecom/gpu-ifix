package billing

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// Phase 23 (HAIKU-04): o mapeamento Event → InsertBillingEventParams leva as
// colunas de cache e a origem do custo.
func TestInsertParams_CacheFieldsAndCostSource(t *testing.T) {
	e := Event{
		TS:               time.Unix(1_790_000_000, 0).UTC(),
		RequestID:        uuid.New(),
		TenantID:         uuid.New(),
		Route:            "chat",
		Upstream:         "anthropic-haiku",
		Model:            "claude-haiku-5-5",
		TokensIn:         12000,
		TokensOut:        300,
		TokensCacheRead:  11800,
		TokensCacheWrite: 200,
		CostSource:       "provider_reported",
		Source:           "final",
	}
	p := insertParams(e)
	if p.TokensCacheRead != 11800 {
		t.Fatalf("TokensCacheRead = %d, want 11800", p.TokensCacheRead)
	}
	if p.TokensCacheWrite != 200 {
		t.Fatalf("TokensCacheWrite = %d, want 200", p.TokensCacheWrite)
	}
	if !p.CostSource.Valid || p.CostSource.String != "provider_reported" {
		t.Fatalf("CostSource = %+v, want valid provider_reported", p.CostSource)
	}
	if p.TokensIn != 12000 || p.TokensOut != 300 || p.Source != "final" {
		t.Fatalf("campos legados mapeados errado: %+v", p)
	}
}

func TestInsertParams_DefaultsWithoutNewFields(t *testing.T) {
	p := insertParams(Event{RequestID: uuid.New(), TenantID: uuid.New(), Route: "chat", Source: "final"})
	if p.TokensCacheRead != 0 || p.TokensCacheWrite != 0 {
		t.Fatalf("cache tokens = %d/%d, want 0/0", p.TokensCacheRead, p.TokensCacheWrite)
	}
	if p.CostSource.Valid {
		t.Fatalf("CostSource vazio deve virar NULL, veio %+v", p.CostSource)
	}
	if p.ApiKeyID.Valid {
		t.Fatalf("APIKeyID zero deve virar NULL")
	}
}

func TestInsertParams_PriceTableCostSource(t *testing.T) {
	p := insertParams(Event{CostSource: "price_table"})
	if !p.CostSource.Valid || p.CostSource.String != "price_table" {
		t.Fatalf("CostSource = %+v, want price_table", p.CostSource)
	}
}
