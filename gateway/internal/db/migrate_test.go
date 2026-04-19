package db

import (
	"io/fs"
	"sort"
	"strings"
	"testing"
)

// TestEmbedFS_HasAllNineMigrations validates the embedded migrations FS
// (via the gatewaydb package shim) contains exactly the 9 migration files
// expected by downstream plans (6 from Phase 2 + 3 from Phase 3 plan 03-02:
// 0007 upstreams table, 0008 seed, 0009 NOTIFY trigger).
func TestEmbedFS_HasAllNineMigrations(t *testing.T) {
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
	if len(names) != 9 {
		t.Fatalf("expected 9 migrations embedded, got %d: %v", len(names), names)
	}
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
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("[%d] got %s want %s", i, names[i], w)
		}
	}
}
