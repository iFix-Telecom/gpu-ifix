package billing

import (
	"context"
	"fmt"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// insertOne maps an Event to gen.InsertBillingEventParams and issues the
// CTE-form INSERT (billing_events + UPSERT usage_counters). Wrapped in a
// function for unit-testability.
//
// The CTE in queries/billing.sql populates cost_local_brl = 0 literally
// (D-B4: local GPU is fixed-cost, per-request cost is always 0); the Event
// carries the field for symmetry but the column is set in SQL, not bound.
func insertOne(ctx context.Context, q *gen.Queries, e Event) error {
	if err := q.InsertBillingEvent(ctx, insertParams(e)); err != nil {
		return fmt.Errorf("%w: insert: %v", ErrFlushFailed, err)
	}
	return nil
}

// insertParams é o mapeamento puro Event → InsertBillingEventParams.
// CostSource "" vira NULL (cost_source é TEXT NULL; só valores fixos do
// código chegam aqui — T-23-67).
func insertParams(e Event) gen.InsertBillingEventParams {
	return gen.InsertBillingEventParams{
		RequestID:           e.RequestID,
		Ts:                  e.TS,
		TenantID:            e.TenantID,
		ApiKeyID:            nullableUUIDForPG(e.APIKeyID),
		Route:               e.Route,
		Upstream:            e.Upstream,
		Model:               e.Model,
		TokensIn:            int32(e.TokensIn),
		TokensOut:           int32(e.TokensOut),
		AudioSeconds:        float32(e.AudioSeconds),
		EmbedsCount:         int32(e.EmbedsCount),
		CostLocalPhantomBrl: numericFromFloat(e.CostLocalPhantomBRL),
		CostExternalBrl:     numericFromFloat(e.CostExternalBRL),
		Source:              e.Source,
		TokensCacheRead:     int32(e.TokensCacheRead),
		TokensCacheWrite:    int32(e.TokensCacheWrite),
		CostSource:          pgtype.Text{String: e.CostSource, Valid: e.CostSource != ""},
	}
}

// nullableUUIDForPG converts a Go uuid.UUID to pgtype.UUID; zero UUID maps
// to SQL NULL so api_key_id column accepts unauthenticated edge cases.
func nullableUUIDForPG(u uuid.UUID) pgtype.UUID {
	if u == uuid.Nil {
		return pgtype.UUID{Valid: false}
	}
	return pgtype.UUID{Bytes: u, Valid: true}
}

// numericFromFloat builds a pgtype.Numeric for a NUMERIC(10,6) column from
// a float64. Uses string round-trip to keep the decimal form exact to 6
// digits. Negative values clamp to 0 (defensive; should never happen).
func numericFromFloat(f float64) pgtype.Numeric {
	if f < 0 {
		f = 0
	}
	// Multiply by 1e6 and truncate to produce an integer with exp=-6.
	// big.Float.Int handles the overflow/underflow cases gracefully.
	bf := new(big.Float).SetFloat64(f * 1e6)
	intPart, _ := bf.Int(nil)
	if intPart == nil {
		intPart = big.NewInt(0)
	}
	return pgtype.Numeric{
		Int:   intPart,
		Exp:   -6,
		Valid: true,
	}
}
