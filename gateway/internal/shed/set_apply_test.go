package shed

// Quick 260930-vkt: Set.ApplyConfigs aplica shed_arm/recover_seconds do
// circuit_config nas FSMs existentes.

import (
	"log/slog"
	"testing"
	"time"
)

var satSig = Signals{InflightOverMax: true, P95OverMax: true}

func TestSet_ApplyConfigs_UpdatesExistingFSM(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{DefaultArmSeconds: 30, DefaultRecoverSeconds: 60})
	s.Rebuild([]string{"a"})
	s.ApplyConfigs(map[string]Config{"a": {ArmSeconds: 5, RecoverSeconds: 7}})
	f, _ := s.Get("a")

	cfg := f.cfg.Load()
	if cfg.ArmSeconds != 5 || cfg.RecoverSeconds != 7 || cfg.Upstream != "a" {
		t.Fatalf("cfg=%+v want arm=5 recover=7 upstream=a", *cfg)
	}

	// Comportamento: Armed→On só após ≥5s (não 30s).
	t0 := time.Now()
	f.Evaluate(t0, satSig)
	if f.State() != StateArmed {
		t.Fatalf("state=%s want armed", f.State())
	}
	f.Evaluate(time.Unix(f.EnteredAt().Unix()+4, 0), satSig)
	if f.State() != StateArmed {
		t.Fatalf("após 4s state=%s want armed", f.State())
	}
	f.Evaluate(time.Unix(f.EnteredAt().Unix()+5, 0), satSig)
	if f.State() != StateOn {
		t.Fatalf("após 5s state=%s want on (arm=5 aplicado)", f.State())
	}
}

func TestSet_ApplyConfigs_ZeroFallsBackToDefault(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{DefaultArmSeconds: 30, DefaultRecoverSeconds: 60})
	s.Rebuild([]string{"a"})
	s.ApplyConfigs(map[string]Config{"a": {ArmSeconds: 5, RecoverSeconds: 7}})
	s.ApplyConfigs(map[string]Config{"a": {}}) // config custom removida
	f, _ := s.Get("a")
	cfg := f.cfg.Load()
	if cfg.ArmSeconds != 30 || cfg.RecoverSeconds != 60 {
		t.Fatalf("cfg=%+v want default 30/60", *cfg)
	}
	s.ApplyConfigs(map[string]Config{"a": {ArmSeconds: -1, RecoverSeconds: 9}})
	cfg = f.cfg.Load()
	if cfg.ArmSeconds != 30 || cfg.RecoverSeconds != 9 {
		t.Fatalf("cfg=%+v want 30/9", *cfg)
	}
}

func TestSet_ApplyConfigs_PreservesState(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{})
	s.Rebuild([]string{"a"})
	f, _ := s.Get("a")
	f.Transition(StateOn, "test")
	s.ApplyConfigs(map[string]Config{"a": {ArmSeconds: 2, RecoverSeconds: 3}})
	if f.State() != StateOn {
		t.Fatalf("state=%s want on (D-C5)", f.State())
	}
	if f2, _ := s.Get("a"); f2 != f {
		t.Fatal("ponteiro da FSM mudou")
	}
}

func TestSet_ApplyConfigs_UnknownNameIgnored(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{})
	s.Rebuild([]string{"a"})
	s.ApplyConfigs(map[string]Config{"ghost": {ArmSeconds: 1}})
	s.ApplyConfigs(nil)
	if _, ok := s.Get("ghost"); ok {
		t.Fatal("ApplyConfigs não deve criar FSM")
	}
	f, _ := s.Get("a")
	if cfg := f.cfg.Load(); cfg.ArmSeconds != 30 {
		t.Fatalf("FSM fora do mapa não deve mudar: %+v", *cfg)
	}
}
