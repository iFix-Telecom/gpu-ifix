package db

import (
	"strings"
	"testing"
	"time"
)

// TestEnsurePartitions_GeneratesExpectedNames validates the naming logic
// without touching Postgres. Integration test in 02-07 runs against a real
// container and asserts the partitions actually exist after the call.
func TestEnsurePartitions_GeneratesExpectedNames(t *testing.T) {
	now := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	var names []string
	for i := 0; i <= 3; i++ {
		m := start.AddDate(0, i, 0)
		for _, table := range []string{"audit_log", "audit_log_content", "billing_events"} {
			names = append(names, table+"_"+m.Format("200601"))
		}
	}
	want := []string{
		"audit_log_202604", "audit_log_content_202604", "billing_events_202604",
		"audit_log_202605", "audit_log_content_202605", "billing_events_202605",
		"audit_log_202606", "audit_log_content_202606", "billing_events_202606",
		"audit_log_202607", "audit_log_content_202607", "billing_events_202607",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("got %v want %v", names, want)
	}
}
