package db

import (
	"io/fs"
	"sort"
	"strings"
	"testing"
)

// TestEmbedFS_HasAllSixMigrations validates the embedded migrations FS
// (via the gatewaydb package shim) contains exactly the 6 Phase-2
// migration files expected by downstream plans.
func TestEmbedFS_HasAllSixMigrations(t *testing.T) {
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
	if len(names) != 6 {
		t.Fatalf("expected 6 migrations embedded, got %d: %v", len(names), names)
	}
	want := []string{
		"0001_create_tenants.sql",
		"0002_create_api_keys.sql",
		"0003_create_audit_log_partitioned.sql",
		"0004_create_audit_log_content_partitioned.sql",
		"0005_create_model_aliases.sql",
		"0006_create_usage_counters_skeleton.sql",
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("[%d] got %s want %s", i, names[i], w)
		}
	}
}
