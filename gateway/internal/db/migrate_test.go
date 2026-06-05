package db

import (
	"io/fs"
	"sort"
	"strings"
	"testing"
)

// TestEmbedFS_HasAllMigrations validates the embedded migrations FS
// (via the gatewaydb package shim) contains every migration file expected
// by downstream plans, in numeric order:
//   - Phase 1/2: 0001-0006 (tenants, api_keys, audit_log, model_aliases, usage_counters skeleton)
//   - Phase 3: 0007-0009 (upstreams + seed + NOTIFY trigger)
//   - Phase 4: 0010-0015 (billing_events, usage_counters evolve, prices+fx, tenants schedule/quota, admin_keys, seed)
//   - Phase 5: 0016-0018 (tenants shedding limits, upstreams shed thresholds, audit_log shed values docs-only)
//   - Phase 6: 0019 (emergency_lifecycles audit table)
//   - Phase 7: 0020 (audit_log.event_kind additive nullable column),
//     0021 (audit_log idx on (ts, tenant_id, route) — CR-02),
//     0022 (audit_log.reason additive nullable column — CR-03)
//   - Phase 6.6: 0023 (primary_lifecycles audit table; sequence number computed
//     at execution time per reviews consensus action #10 — 2026-05-17 was hardcoded
//     before, causing churn risk when concurrent migrations land)
//   - Phase 06.7: 0024 (upstreams tts role) + 0025 (voices catalog)
//   - Phase 06.9: 0026 (evolve model_aliases per-upstream — composite PK,
//     tier-1 seed, R3 Down guard, R11 column comment)
//
// When adding migrations, append the filename to the want slice; the test
// fails if a migration is missing, out of order, or unexpected.
func TestEmbedFS_HasAllMigrations(t *testing.T) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	want := []string{
		"0001_create_tenants.sql",
		"0002_create_api_keys.sql",
		"0003_create_audit_log_partitioned.sql",
		"0004_create_audit_log_content_partitioned.sql",
		"0005_create_model_aliases.sql",
		"0006_create_usage_counters_skeleton.sql",
		"0007_create_upstreams.sql",
		"0008_seed_upstreams.sql",
		"0009_upstreams_notify_trigger.sql",
		"0010_create_billing_events.sql",
		"0011_evolve_usage_counters.sql",
		"0012_create_prices_and_fx.sql",
		"0013_evolve_tenants_schedule_quota.sql",
		"0014_create_admin_keys.sql",
		"0015_seed_prices_and_quotas.sql",
		"0016_evolve_tenants_shedding_limits.sql",
		"0017_evolve_upstreams_shed_thresholds.sql",
		"0018_audit_log_shed_values.sql",
		"0019_emergency_lifecycles.sql",
		"0020_audit_log_event_kind.sql",
		"0021_audit_log_ts_index.sql",
		"0022_audit_log_reason.sql",
		"0023_primary_lifecycles.sql",
		"0024_upstreams_tts_role.sql",
		"0025_create_voices.sql",
		"0026_evolve_model_aliases_per_upstream.sql",
		"0027_openrouter_target_deepseek_v4_flash.sql",
		"0028_remove_local_stt_upstream.sql",
	}
	if len(names) != len(want) {
		t.Fatalf("expected %d migrations embedded, got %d: %v", len(want), len(names), names)
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("[%d] got %s want %s", i, names[i], w)
		}
	}
}
