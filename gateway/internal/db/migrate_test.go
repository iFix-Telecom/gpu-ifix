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
