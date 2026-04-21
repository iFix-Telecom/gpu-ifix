//go:build integration

package integration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

// Triple-defense for sensitive+peak invalid state (D-C1):
//  1. gatewayctl tenant set-mode refuses pre-DB
//  2. DB CHECK chk_sensitive_no_peak refuses raw UPDATE
//  3. Gateway boot-time CheckSensitivePeakInvariant detects a CHECK bypass
//
// All three paths are exercised here. The gatewayctl and boot-time paths
// require either compiling the CLI binary or exercising the loader directly;
// we pick the direct-loader path for (3) so the test is hermetic.

// TestSensitivePeakRejectGatewayctl — Path 1: pre-DB rejection in the CLI.
func TestSensitivePeakRejectGatewayctl(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	_ = seedPhase4(t, ctx, pool)

	bin := buildGatewayctl(t)

	cmd := exec.CommandContext(ctx, bin,
		"tenant", "set-mode",
		"--tenant", "cobrancas",
		"--mode", "peak",
		"--window", "08-22",
	)
	cmd.Env = append(os.Environ(),
		"AI_GATEWAY_PG_DSN="+sharedPGDSN,
		"AI_GATEWAY_REDIS_ADDR="+sharedRedisAddr,
		"UPSTREAM_LLM_URL=http://dummy",
		"UPSTREAM_STT_URL=http://dummy",
		"UPSTREAM_EMBED_URL=http://dummy",
		"UPSTREAM_HEALTH_BRIDGE_URL=http://dummy",
	)
	out, err := cmd.CombinedOutput()
	outStr := string(out)
	if err == nil {
		t.Fatalf("expected non-zero exit, got success: %s", outStr)
	}
	// runTenantSetMode returns exit 2 for the LGPD rejection path.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() != 2 {
			t.Errorf("exit code: want 2 (LGPD policy), got %d", exitErr.ExitCode())
		}
	}
	if !strings.Contains(outStr, "cannot set peak mode for sensitive tenant") {
		t.Errorf("expected LGPD message, got: %s", outStr)
	}
}

// TestSensitivePeakRejectCheckConstraint — Path 2: CHECK constraint rejects
// raw UPDATE that bypasses gatewayctl's validation.
func TestSensitivePeakRejectCheckConstraint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	_ = seedPhase4(t, ctx, pool)

	// cobrancas has data_class='sensitive'; UPDATE to mode='peak' must be
	// rejected by chk_sensitive_no_peak.
	_, err := pool.Exec(ctx,
		`UPDATE ai_gateway.tenants SET mode = 'peak' WHERE slug = 'cobrancas'`)
	if err == nil {
		t.Fatal("expected CHECK constraint violation, got nil error")
	}
	if !strings.Contains(err.Error(), "chk_sensitive_no_peak") {
		t.Errorf("expected chk_sensitive_no_peak in error, got: %v", err)
	}
}

// TestSensitivePeakRejectBootTimeInvariant — Path 3: loader's
// CheckSensitivePeakInvariant detects a CHECK-bypass state (seeded by
// disabling triggers/constraints for the insert). Gateway main.go runs
// this at boot and os.Exit(1)s if violated.
func TestSensitivePeakRejectBootTimeInvariant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	_ = seedPhase4(t, ctx, pool)

	// Bypass CHECK by temporarily dropping it, UPDATE-ing, then re-adding.
	// session_replication_role='replica' only disables triggers, not
	// CHECK constraints, so we drop+reinstate. The loader query does a plain
	// COUNT; no trigger involvement.
	if _, err := pool.Exec(ctx,
		`ALTER TABLE ai_gateway.tenants DROP CONSTRAINT chk_sensitive_no_peak`); err != nil {
		t.Skipf("cannot drop CHECK in this env (role lacks DDL?): %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE ai_gateway.tenants SET mode = 'peak' WHERE slug = 'cobrancas'`); err != nil {
		t.Fatalf("bypass UPDATE: %v", err)
	}
	// Leave the CHECK off — the test only needs the transient violating row
	// for CheckSensitivePeakInvariant. Defer-restore so we don't leak state
	// into freshSchema on subsequent tests.
	defer func() {
		// Revert the violating row first so the CHECK re-add can succeed.
		_, _ = pool.Exec(context.Background(),
			`UPDATE ai_gateway.tenants SET mode = '24/7' WHERE slug = 'cobrancas'`)
		_, _ = pool.Exec(context.Background(),
			`ALTER TABLE ai_gateway.tenants
			    ADD CONSTRAINT chk_sensitive_no_peak
			        CHECK ((mode = '24/7') OR (data_class = 'normal'))`)
	}()

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	loader, err := tenants.NewLoader(ctx, pool, loc, discardLogger())
	if err != nil {
		t.Fatalf("new loader: %v", err)
	}
	err = loader.CheckSensitivePeakInvariant(ctx)
	if !errors.Is(err, tenants.ErrSensitivePeakInvariant) {
		t.Errorf("expected ErrSensitivePeakInvariant wrap, got %v", err)
	}
}

// buildGatewayctl compiles ./gateway/cmd/gatewayctl into a temp binary
// accessible via the returned absolute path. Mirrors gateway_e2e_test.go's
// gateway-binary build pattern.
func buildGatewayctl(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "gatewayctl")
	build := exec.Command(goBinaryPath(), "build", "-o", bin, "./gateway/cmd/gatewayctl")
	build.Dir = repoRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build gatewayctl: %v\n%s", err, out)
	}
	return bin
}
