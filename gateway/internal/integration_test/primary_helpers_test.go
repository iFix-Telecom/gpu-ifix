//go:build integration

// Phase 6.6 Plan 06.6-10 — shared test helpers for primary_*_test.go
// integration tests. Helpers in this file:
//
//   - primaryTestCfg: returns a config.Config with Wave 0 LOCKED defaults
//   - accelerated timings for sub-30s test runs.
//   - fakeVastPrimary: in-process fake of primary.VastAPI (subset of the
//     real *vast.Client). Per-test scriptable closures for SearchOffers /
//     CreateInstance / GetInstance / DestroyInstance.
//   - fakePrimaryLoader: records OverrideTier0 / RestoreTier0 calls for
//     the 3 primary roles (llm / stt / tts — embed left the pod, D-03).
//   - fakePrimaryDCGM: records the most-recent SetURL value.
//   - fakePrimaryInflight: scriptable per-upstream inflight counter.
//   - alwaysInPeakRule / neverInPeakRule: ScheduleRule fixtures for tests
//     that need deterministic IsInPeak / ShouldBeProvisioned behaviour.
//   - runningPrimaryInstance: vast.Instance with all 4 host port mappings
//     populated (8000/8001/8003/9400 → 33000/33001/33003/33400).
//
// Wave 0 orthogonality: every helper is at the orchestration-layer
// boundary; the supervisord 4-service single-container model is exposed
// only through the 4 host-port mappings + the per-URL HealthCheck
// closure pattern (mock HealthCheck per URL = simulate which supervisord
// child is healthy).
package integration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
)

// primaryTestCfg returns a config.Config populated with Wave 0 LOCKED
// defaults + accelerated test timings. Wave 0 SHA-pinned image digests
// + non-empty Whisper / BGE-M3 SHAs (so buildCreateRequest's fail-fast
// gate does not bail out).
func primaryTestCfg(t *testing.T) config.Config {
	t.Helper()
	cfg, err := loadBaseConfig()
	if err != nil {
		t.Fatalf("loadBaseConfig: %v", err)
	}
	// Wave 0 LOCKED image digests + weights (parity lifecycle_test.go
	// cfgWithDefaults).
	cfg.PrimaryTemplateImage = "ghcr.io/ggml-org/llama.cpp:server-cuda-b9191@sha256:cb375311f4170bb1aa18840e946f64f99e6094b90bde69dcb6e0a62a183d7ba3"
	// Phase 11.2 D-B5′: PrimarySpeachesImage restored (revert 11.1 D-A1 —
	// tier-0 STT re-added by migration 0029).
	cfg.PrimaryInfinityImage = "michaelf34/infinity:0.0.77@sha256:11e8b3921b9f1a58965afaad4a844c435c9807cbc82c51e47cb147b7d977fc88"
	cfg.PrimaryDCGMImage = "nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless@sha256:60d3b00ac80b4ae77f94dae2f943685605585ad9e92fdccda3154d009ae317cc"
	cfg.PrimaryQwenWeightsKey = "qwen3.6-27b-Q4_K_M/v1.0.0/model.gguf"
	cfg.PrimaryQwenWeightsSHA256 = "a7cbd3ecc0e3f9b333edee61ae66bc87ed713c5d49587a8355814722ed329e0f"
	// Phase 11.2 D-B5′: PRIMARY_WHISPER_WEIGHTS_* restored (revert 11.1 D-A4 —
	// `buildCreateRequest` fail-fast gate is back so SHA must be non-empty).
	cfg.PrimaryWhisperWeightsKey = "whisper-large-v3/v1.0.0/model.tar.gz"
	cfg.PrimaryWhisperWeightsSHA256 = "wh1sp3rsh4test256"
	cfg.PrimaryBGEM3WeightsKey = "bge-m3/v1.0.0/model.tar.gz"
	cfg.PrimaryBGEM3WeightsSHA256 = "bg3m35h4test256"
	cfg.MinioEndpoint = "https://s3.example.com"
	cfg.MinioBucket = "ai-gateway"
	cfg.MinioAccessKey = "AKID-test"
	cfg.MinioSecretKey = "SK-test"

	// Accelerated timings (Pitfall 13 parity).
	cfg.PrimaryProvisionColdStartBudgetSeconds = 10
	cfg.PrimaryProvisionFailureCooldownSeconds = 1
	cfg.PrimaryPodScheduleGraceRampDownSeconds = 1
	cfg.PrimaryPodScheduleProvisionLeadSeconds = 0
	cfg.PrimaryVastPriceCapDPH = 0.40
	cfg.USDToBRLRate = 5.0
	cfg.MonthlyPrimaryBudgetBRL = 800
	return cfg
}

// fakeVastPrimary is the in-process fake of primary.VastAPI consumed by
// integration tests. Closures are scriptable per-test; concurrent
// callers are serialised via mu.
type fakeVastPrimary struct {
	mu sync.Mutex

	SearchOffersFn   func(ctx context.Context, f vast.SearchFilter) ([]vast.Offer, error)
	CreateInstanceFn func(ctx context.Context, offerID int64, req vast.CreateRequest) (vast.Instance, error)
	GetInstanceFn    func(ctx context.Context, id int64) (vast.Instance, error)
	DestroyFn        func(ctx context.Context, id int64) error

	DestroyCalls atomic.Int32
	Destroyed    sync.Map // int64 -> struct{}
}

func (f *fakeVastPrimary) SearchOffers(ctx context.Context, filter vast.SearchFilter) ([]vast.Offer, error) {
	f.mu.Lock()
	fn := f.SearchOffersFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("fakeVastPrimary: SearchOffersFn not set")
	}
	return fn(ctx, filter)
}

func (f *fakeVastPrimary) CreateInstance(ctx context.Context, offerID int64, req vast.CreateRequest) (vast.Instance, error) {
	f.mu.Lock()
	fn := f.CreateInstanceFn
	f.mu.Unlock()
	if fn == nil {
		return vast.Instance{}, errors.New("fakeVastPrimary: CreateInstanceFn not set")
	}
	return fn(ctx, offerID, req)
}

func (f *fakeVastPrimary) GetInstance(ctx context.Context, id int64) (vast.Instance, error) {
	f.mu.Lock()
	fn := f.GetInstanceFn
	f.mu.Unlock()
	if fn == nil {
		return vast.Instance{}, errors.New("fakeVastPrimary: GetInstanceFn not set")
	}
	return fn(ctx, id)
}

func (f *fakeVastPrimary) DestroyInstance(ctx context.Context, id int64) error {
	f.DestroyCalls.Add(1)
	f.Destroyed.Store(id, struct{}{})
	f.mu.Lock()
	fn := f.DestroyFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, id)
	}
	return nil
}

// HasDestroyed returns true if DestroyInstance was called with id.
func (f *fakeVastPrimary) HasDestroyed(id int64) bool {
	_, ok := f.Destroyed.Load(id)
	return ok
}

// fakePrimaryLoader records OverrideTier0 / RestoreTier0 calls per role.
// Tests assert the 3-role invariant (llm / stt / embed).
type fakePrimaryLoader struct {
	mu        sync.Mutex
	overrides map[string]string // role -> URL
	restored  []string          // ordered list of restored roles
	refreshes atomic.Int32
}

func newFakePrimaryLoader() *fakePrimaryLoader {
	return &fakePrimaryLoader{overrides: map[string]string{}}
}

func (f *fakePrimaryLoader) OverrideTier0(role, url string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.overrides[role] = url
}

func (f *fakePrimaryLoader) RestoreTier0(role string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.overrides, role)
	f.restored = append(f.restored, role)
}

func (f *fakePrimaryLoader) Refresh(ctx context.Context) error {
	f.refreshes.Add(1)
	_ = ctx
	return nil
}

// Tier0OverrideURL mirrors the real Loader.Tier0OverrideURL — returns
// (url, true) when the role has a non-empty override, else ("", false).
// Added Phase 06.7 (D-13) for the evaluateReady re-assert loop.
func (f *fakePrimaryLoader) Tier0OverrideURL(role string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	url, ok := f.overrides[role]
	if !ok || url == "" {
		return "", false
	}
	return url, true
}

func (f *fakePrimaryLoader) Snapshot() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.overrides))
	for k, v := range f.overrides {
		out[k] = v
	}
	return out
}

func (f *fakePrimaryLoader) RestoredRoles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.restored))
	copy(out, f.restored)
	return out
}

// fakePrimaryDCGM records the most-recent SetURL value.
type fakePrimaryDCGM struct {
	url atomic.Pointer[string]
}

func (f *fakePrimaryDCGM) SetURL(url string) {
	u := url
	f.url.Store(&u)
}

func (f *fakePrimaryDCGM) Last() string {
	if p := f.url.Load(); p != nil {
		return *p
	}
	return ""
}

// fakePrimaryInflight returns scripted per-upstream inflight counts.
type fakePrimaryInflight struct {
	mu     sync.Mutex
	counts map[string]int64
}

func newFakePrimaryInflight() *fakePrimaryInflight {
	return &fakePrimaryInflight{counts: map[string]int64{}}
}

func (f *fakePrimaryInflight) Count(upstream string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[upstream]
}

func (f *fakePrimaryInflight) Set(upstream string, n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[upstream] = n
}

// alwaysInPeakRule constructs a ScheduleRule whose IsInPeak +
// ShouldBeProvisioned always return true regardless of wall-clock hour.
// Implemented via an overnight-wrap window with UpHour == DownHour == 0:
// IsInPeak's wrap branch then returns Days[weekday] for every hour in
// [0, 24). Previous form (UpHour=0, DownHour=23) excluded 23:00–23:59
// UTC and caused deterministic CI failures whenever the runner clock
// landed in that hour.
func alwaysInPeakRule() primary.ScheduleRule {
	loc, _ := time.LoadLocation("UTC")
	return primary.ScheduleRule{
		Timezone: loc,
		UpHour:   0,
		DownHour: 0,
		Days: map[time.Weekday]bool{
			time.Sunday: true, time.Monday: true, time.Tuesday: true,
			time.Wednesday: true, time.Thursday: true, time.Friday: true,
			time.Saturday: true,
		},
		GraceRampDownS: 1,
		ProvisionLeadS: 0,
		Disabled:       false,
	}
}

// neverInPeakRule returns a ScheduleRule where IsInPeak +
// ShouldBeProvisioned always return false (Disabled=true).
func neverInPeakRule() primary.ScheduleRule {
	r := alwaysInPeakRule()
	r.Disabled = true
	return r
}

// runningPrimaryInstance returns a vast.Instance ready for markReady (running
// + 4 host port mappings + IP). Mirrors primary/reconciler_test.go
// runningInstanceWithAllPorts but lives in the integration_test package.
func runningPrimaryInstance(id int64) vast.Instance {
	return vast.Instance{
		ID:           id,
		ActualStatus: "running",
		PublicIPAddr: "203.0.113.7",
		Ports: map[string][]vast.PortBinding{
			"8000/tcp": {{HostIP: "0.0.0.0", HostPort: "33000"}},
			"8001/tcp": {{HostIP: "0.0.0.0", HostPort: "33001"}},
			"8003/tcp": {{HostIP: "0.0.0.0", HostPort: "33003"}},
			"9400/tcp": {{HostIP: "0.0.0.0", HostPort: "33400"}},
		},
	}
}

// alwaysHealthy is a HealthCheck closure returning true for any URL.
func alwaysHealthy(_ context.Context, _ string) bool { return true }
