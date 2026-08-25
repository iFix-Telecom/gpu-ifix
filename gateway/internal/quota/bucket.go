// Package quota: bucket configuration (RouteClass enum + BucketConfig).
//
// RouteClass strings are persisted in Redis keys (gw:rate:{tenant}:{class}:*)
// so their exact values are part of the public wire contract. Do not change
// them once deployed — a rename strands the counters of in-flight windows.
package quota

// RouteClass enumerates the rate-limit namespaces used by Phase 4.
// Each class gets its own (RPS, RPM) bucket per-tenant.
type RouteClass string

const (
	// RouteClassChat covers /v1/chat/completions and any LLM text generation.
	RouteClassChat RouteClass = "chat"
	// RouteClassEmbed covers /v1/embeddings.
	RouteClassEmbed RouteClass = "embed"
	// RouteClassSTT covers /v1/audio/transcriptions and related STT routes.
	RouteClassSTT RouteClass = "stt"
	// RouteClassTTS covers /v1/audio/speech (text-to-speech synthesis).
	// Phase 06.7 (D-12). The wire value "tts" is persisted in Redis keys
	// (gw:rate:{tenant}:tts:*) and must not change once deployed.
	RouteClassTTS RouteClass = "tts"
	// RouteClassRerank covers /v1/rerank (cross-encoder reranking).
	// quick 260825. The wire value "rerank" is persisted in Redis keys
	// (gw:rate:{tenant}:rerank:*) and must not change once deployed.
	RouteClassRerank RouteClass = "rerank"
)

// BucketConfig is the per-tenant + per-route bucket capacity pair. It is
// loaded from tenants.TenantConfig (RPSLimit, RPMLimit) at request time.
type BucketConfig struct {
	RPSCapacity int
	RPMCapacity int
}

// RPSRefillPerMs returns the RPS refill rate as tokens-per-millisecond
// (capacity fills the bucket over 1_000 ms).
func (c BucketConfig) RPSRefillPerMs() float64 {
	if c.RPSCapacity <= 0 {
		return 0
	}
	return float64(c.RPSCapacity) / 1000.0
}

// RPMRefillPerMs returns the RPM refill rate as tokens-per-millisecond
// (capacity fills the bucket over 60_000 ms).
func (c BucketConfig) RPMRefillPerMs() float64 {
	if c.RPMCapacity <= 0 {
		return 0
	}
	return float64(c.RPMCapacity) / 60000.0
}
