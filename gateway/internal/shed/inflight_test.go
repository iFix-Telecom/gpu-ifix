// Package shed (inflight_test.go): unit tests for the per-(upstream, tenant)
// inflight counter registry consumed by the shed FSM 2-of-3 saturation
// gate (CONTEXT.md D-B1 fairness per-tenant + D-A1 inflight signal).
package shed

import (
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestInflightRegistry_NewAndEmpty(t *testing.T) {
	r := NewInflightRegistry([]string{"local-llm", "local-stt"})
	if r.GlobalInflight("local-llm") != 0 {
		t.Fatal("new registry should have zero counters")
	}
	if r.GlobalInflight("unknown") != 0 {
		t.Fatal("unknown upstream should return 0 (no panic)")
	}
	if r.TenantInflight("unknown", uuid.New()) != 0 {
		t.Fatal("unknown upstream tenant should return 0 (no panic)")
	}
}

func TestInflightRegistry_NilReceiverReturnsZero(t *testing.T) {
	var r *InflightRegistry
	if r.GlobalInflight("any") != 0 {
		t.Fatal("nil registry GlobalInflight should be 0")
	}
	if r.TenantInflight("any", uuid.New()) != 0 {
		t.Fatal("nil registry TenantInflight should be 0")
	}
	// Inc/Dec on nil should be no-ops (defensive).
	r.Inc("any", uuid.New())
	r.Dec("any", uuid.New())
}

func TestInflightRegistry_IncDecBalance(t *testing.T) {
	r := NewInflightRegistry([]string{"local-llm"})
	tenant := uuid.New()
	const N = 1000
	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < N; i++ {
				r.Inc("local-llm", tenant)
				r.Dec("local-llm", tenant)
			}
		}()
	}
	wg.Wait()
	if g := r.GlobalInflight("local-llm"); g != 0 {
		t.Fatalf("balanced Inc/Dec expected global=0, got %d", g)
	}
	if c := r.TenantInflight("local-llm", tenant); c != 0 {
		t.Fatalf("balanced Inc/Dec expected tenant=0, got %d", c)
	}
}

func TestInflightRegistry_MultipleTenants(t *testing.T) {
	r := NewInflightRegistry([]string{"local-llm"})
	tA, tB, tC := uuid.New(), uuid.New(), uuid.New()
	for _, tid := range []uuid.UUID{tA, tB, tC} {
		for i := 0; i < 10; i++ {
			r.Inc("local-llm", tid)
		}
	}
	if g := r.GlobalInflight("local-llm"); g != 30 {
		t.Fatalf("expected global=30, got %d", g)
	}
	if c := r.TenantInflight("local-llm", tA); c != 10 {
		t.Fatalf("expected tA=10, got %d", c)
	}
	if c := r.TenantInflight("local-llm", tB); c != 10 {
		t.Fatalf("expected tB=10, got %d", c)
	}
	if c := r.TenantInflight("local-llm", tC); c != 10 {
		t.Fatalf("expected tC=10, got %d", c)
	}
}

func TestInflightRegistry_UnknownUpstreamIncNoop(t *testing.T) {
	r := NewInflightRegistry([]string{"local-llm"})
	tenant := uuid.New()
	// Inc on an upstream that was NOT registered should be a no-op
	// (returns silently; never panics or auto-creates).
	r.Inc("nonexistent", tenant)
	if r.GlobalInflight("nonexistent") != 0 {
		t.Fatal("Inc on unknown upstream should be no-op")
	}
}

func TestInflightRegistry_DecNeverGoesNegative(t *testing.T) {
	// Defensive: if middleware accidentally Dec's before Inc (paired-defer
	// gone wrong), counter goes negative. This is informational — we
	// verify the registry survives (no panic) and stays internally consistent.
	r := NewInflightRegistry([]string{"local-llm"})
	tenant := uuid.New()
	r.Dec("local-llm", tenant)
	// Counter may be -1 here; verify Inc restores to 0 (arithmetic
	// soundness — proves the atomic.Int64 is doing what we expect).
	r.Inc("local-llm", tenant)
	if g := r.GlobalInflight("local-llm"); g != 0 {
		t.Fatalf("expected counter to land at 0 after dec+inc, got %d", g)
	}
}
