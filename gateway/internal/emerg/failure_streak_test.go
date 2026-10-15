// Package emerg (failure_streak_test.go): trava do gatilho automático de
// emergência (incidente 2026-10-14 — primário desligado de propósito,
// breaker local-llm OPEN, 967 pods alugados e destruídos em 16h).
//
// Cobre:
//   - EMERGENCY_PROVISIONING_ENABLED=false: o gatilho nunca arma.
//   - EMERGENCY_MAX_CONSECUTIVE_FAILURES: N falhas seguidas travam o
//     gatilho; só um CLOSED observado (ou force_provision) destrava.
//   - limite 0 = sem trava (comportamento anterior).
package emerg

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

func breakerEvent(state string) redisx.BreakerEvent {
	return redisx.BreakerEvent{Upstream: "local-llm", State: state, SinceUnix: time.Now().Unix()}
}

func newStreakReconciler(cfg config.Config) *Reconciler {
	cfg.ProvisionTriggerFailedOverSeconds = 120
	return NewReconciler(Deps{
		// Redis/DB nil: evaluateHealthy pula a checagem de lifecycle vivo
		// quando r.q == nil e nenhum caminho aqui toca o Redis.
		FSM: NewFSM(slog.New(slog.DiscardHandler), nil),
		Cfg: cfg,
		Log: slog.New(slog.DiscardHandler),
	})
}

// openSustained simula o breaker local-llm OPEN há mais que o limiar.
func openSustained(r *Reconciler) {
	r.tracker.state.Store("open")
	r.tracker.openSince.Store(time.Now().Add(-10 * time.Minute).Unix())
	r.tracker.closedSince.Store(0)
}

func TestEvaluateHealthy_DisabledNeverTriggers(t *testing.T) {
	r := newStreakReconciler(config.Config{EmergencyProvisioningDisabled: true})
	openSustained(r)

	r.evaluateHealthy(context.Background(), time.Now(), slog.New(slog.DiscardHandler))

	require.Equal(t, StateHealthy, r.deps.FSM.State(),
		"com EMERGENCY_PROVISIONING_ENABLED=false o breaker OPEN não pode alugar pod")
}

func TestEvaluateHealthy_EnabledTriggers(t *testing.T) {
	r := newStreakReconciler(config.Config{EmergencyMaxConsecutiveFailures: 3})
	openSustained(r)

	r.evaluateHealthy(context.Background(), time.Now(), slog.New(slog.DiscardHandler))

	require.Equal(t, StateEmergencyProvisioning, r.deps.FSM.State())
}

func TestFailureStreak_HaltsAfterLimitAndRearmsOnClosed(t *testing.T) {
	r := newStreakReconciler(config.Config{EmergencyMaxConsecutiveFailures: 3})
	log := slog.New(slog.DiscardHandler)
	openSustained(r)

	r.recordProvisionFailure("health_timeout")
	r.recordProvisionFailure("health_timeout")
	require.False(t, r.triggerHalted.Load(), "abaixo do limite o gatilho segue armado")
	r.recordProvisionFailure("instance_terminal_state")
	require.True(t, r.triggerHalted.Load(), "3ª falha seguida trava o gatilho")

	r.evaluateHealthy(context.Background(), time.Now(), log)
	require.Equal(t, StateHealthy, r.deps.FSM.State(),
		"gatilho travado não pode provisionar com o breaker ainda OPEN")

	// local-llm volta: CLOSED observado destrava e zera a contagem.
	r.tracker.ApplyEvent(breakerEvent("closed"))
	r.tracker.closedSince.Store(time.Now().Add(-5 * time.Second).Unix())
	r.evaluateHealthy(context.Background(), time.Now(), log)
	require.False(t, r.triggerHalted.Load())
	require.Zero(t, r.consecutiveFailures.Load())

	// Nova queda sustentada volta a disparar normalmente.
	openSustained(r)
	r.evaluateHealthy(context.Background(), time.Now(), log)
	require.Equal(t, StateEmergencyProvisioning, r.deps.FSM.State())
}

func TestFailureStreak_ResetClearsHalt(t *testing.T) {
	r := newStreakReconciler(config.Config{EmergencyMaxConsecutiveFailures: 1})
	r.recordProvisionFailure("health_timeout")
	require.True(t, r.triggerHalted.Load())

	r.resetFailureStreak() // caminho de markHealthy / force_provision
	require.False(t, r.triggerHalted.Load())
	require.Zero(t, r.consecutiveFailures.Load())
}

func TestFailureStreak_ZeroLimitNeverHalts(t *testing.T) {
	r := newStreakReconciler(config.Config{EmergencyMaxConsecutiveFailures: 0})
	for i := 0; i < 50; i++ {
		r.recordProvisionFailure("health_timeout")
	}
	require.False(t, r.triggerHalted.Load())
}
