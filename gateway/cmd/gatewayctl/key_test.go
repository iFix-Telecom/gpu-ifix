// Package main (key_test.go): real unit tests for `gatewayctl key list`
// (Phase 11 Plan 04 D-18.3). Reviews LOW #4 — zero t.Skip placeholders.
//
// Strategy: exercise runKeyListWith with a stub keyLister returning
// canned Rows. The stub bypasses loadAndPool so the test is hermetic
// (no Postgres pool, no Redis, no env). The render output is captured
// via a bytes.Buffer and asserted line-by-line for:
//
//   - Header columns: ID / TENANT / PREFIX / STATUS / DATA_CLASS / CREATED / LAST_USED
//   - Data row count (must match canned input)
//   - Absence of "key_hash" substring (threat T-11-OPS-02 — projection
//     of ListActiveKeysAllWithMeta deliberately excludes secret-bearing
//     columns; renderKeyList must not synthesize them either)
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

func discardKeyLog() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// stubKeyLister implements the keyLister interface for hermetic
// rendering tests. Per-method overrides allow each test to inject the
// exact behavior under test.
type stubKeyLister struct {
	all       []gen.ListActiveKeysAllWithMetaRow
	allErr    error
	byTenant  []gen.ListActiveKeysByTenantWithMetaRow
	byErr     error
	tenant    gen.GetTenantBySlugRow
	tenantErr error
}

func (s *stubKeyLister) ListActiveKeysAllWithMeta(_ context.Context) ([]gen.ListActiveKeysAllWithMetaRow, error) {
	return s.all, s.allErr
}

func (s *stubKeyLister) ListActiveKeysByTenantWithMeta(_ context.Context, _ uuid.UUID) ([]gen.ListActiveKeysByTenantWithMetaRow, error) {
	return s.byTenant, s.byErr
}

func (s *stubKeyLister) GetTenantBySlug(_ context.Context, _ string) (gen.GetTenantBySlugRow, error) {
	return s.tenant, s.tenantErr
}

// cannedRows returns 2 distinct active keys with known tenant slugs +
// key prefixes. CreatedAt fixed at 2026-01-01T00:00:00Z; LastUsedAt
// fixed at 2026-05-01T12:00:00Z for the first row, NULL for the second
// (so the rendered "-" placeholder is exercised).
func cannedRows() []gen.ListActiveKeysAllWithMetaRow {
	created, _ := time.Parse(time.RFC3339, "2026-01-01T00:00:00Z")
	usedAt, _ := time.Parse(time.RFC3339, "2026-05-01T12:00:00Z")
	id1 := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	id2 := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	tid1 := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	tid2 := uuid.MustParse("00000000-0000-0000-0000-0000000000a2")
	return []gen.ListActiveKeysAllWithMetaRow{
		{
			ID:         id1,
			TenantID:   tid1,
			TenantSlug: "uat10-test",
			KeyPrefix:  "ifix_sk_aaaa****1111",
			Status:     "active",
			DataClass:  "normal",
			CreatedAt:  created,
			LastUsedAt: pgtype.Timestamptz{Time: usedAt, Valid: true},
		},
		{
			ID:         id2,
			TenantID:   tid2,
			TenantSlug: "chat-ifix",
			KeyPrefix:  "ifix_sk_bbbb****2222",
			Status:     "active",
			DataClass:  "sensitive",
			CreatedAt:  created,
			LastUsedAt: pgtype.Timestamptz{Valid: false}, // never used → "-"
		},
	}
}

// TestRunKeyList_NoTenantFilter_RendersAllRows: invoke runKeyListWith
// with no tenant filter; assert header + 2 data rows + no key_hash.
func TestRunKeyList_NoTenantFilter_RendersAllRows(t *testing.T) {
	stub := &stubKeyLister{all: cannedRows()}
	var buf bytes.Buffer
	got := runKeyListWith(context.Background(), stub, "", &buf, discardKeyLog())
	if got != 0 {
		t.Fatalf("exit code: want 0, got %d (output=%q)", got, buf.String())
	}
	out := buf.String()

	// Header columns (tabwriter pads with spaces; substring check is
	// safe because the column names are unique tokens in the output).
	for _, col := range []string{"ID", "TENANT", "PREFIX", "STATUS", "DATA_CLASS", "CREATED", "LAST_USED"} {
		if !strings.Contains(out, col) {
			t.Errorf("missing column %q in output:\n%s", col, out)
		}
	}

	// Data rows: split on newlines, drop header + trailing blank, expect
	// exactly 2 data lines.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("too few lines (need header + 2 data): %d\n%s", len(lines), out)
	}
	dataLines := lines[1:]
	dataCount := 0
	for _, l := range dataLines {
		if strings.TrimSpace(l) != "" {
			dataCount++
		}
	}
	if dataCount != 2 {
		t.Errorf("data row count: want 2, got %d:\n%s", dataCount, out)
	}

	// Threat T-11-OPS-02: never leak key_hash or raw key material.
	if strings.Contains(out, "key_hash") {
		t.Errorf("output contains forbidden substring \"key_hash\":\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "keyhash") {
		t.Errorf("output contains forbidden substring \"keyhash\":\n%s", out)
	}
	// Tenant slugs must be present (operator-readable surface).
	if !strings.Contains(out, "uat10-test") {
		t.Errorf("missing tenant slug 'uat10-test' in output:\n%s", out)
	}
	if !strings.Contains(out, "chat-ifix") {
		t.Errorf("missing tenant slug 'chat-ifix' in output:\n%s", out)
	}
	// Nullable LastUsedAt renders as "-" for the second row.
	if !strings.Contains(out, "\t-") && !strings.Contains(out, " -\n") && !strings.Contains(out, " -") {
		t.Errorf("expected '-' placeholder for null LastUsedAt:\n%s", out)
	}
}

// TestRunKeyList_TenantFilter_ResolvesSlugAndRenders: --tenant X is set;
// stub returns a resolved tenant UUID + by-tenant rows; output renders
// only the filtered set.
func TestRunKeyList_TenantFilter_ResolvesSlugAndRenders(t *testing.T) {
	created, _ := time.Parse(time.RFC3339, "2026-01-01T00:00:00Z")
	tid := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	stub := &stubKeyLister{
		tenant: gen.GetTenantBySlugRow{ID: tid, Slug: "uat10-test"},
		byTenant: []gen.ListActiveKeysByTenantWithMetaRow{{
			ID:         uuid.MustParse("00000000-0000-0000-0000-000000000010"),
			TenantID:   tid,
			TenantSlug: "uat10-test",
			KeyPrefix:  "ifix_sk_cccc****3333",
			Status:     "active",
			DataClass:  "normal",
			CreatedAt:  created,
			LastUsedAt: pgtype.Timestamptz{Valid: false},
		}},
	}
	var buf bytes.Buffer
	got := runKeyListWith(context.Background(), stub, "uat10-test", &buf, discardKeyLog())
	if got != 0 {
		t.Fatalf("exit code: want 0, got %d (output=%q)", got, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "ifix_sk_cccc****3333") {
		t.Errorf("missing expected key_prefix in tenant-filtered output:\n%s", out)
	}
	if strings.Contains(out, "ifix_sk_aaaa****1111") {
		t.Errorf("non-tenant row leaked into filtered output:\n%s", out)
	}
}

// TestRunKeyList_TenantNotFound_ExitsOne: --tenant X resolves to no row.
func TestRunKeyList_TenantNotFound_ExitsOne(t *testing.T) {
	stub := &stubKeyLister{tenantErr: pgx.ErrNoRows}
	var buf bytes.Buffer
	got := runKeyListWith(context.Background(), stub, "no-such-tenant", &buf, discardKeyLog())
	if got != 1 {
		t.Errorf("exit code: want 1 (tenant not found), got %d", got)
	}
}

// TestRunKeyList_DBError_ExitsOne: the all-list query errors out.
func TestRunKeyList_DBError_ExitsOne(t *testing.T) {
	stub := &stubKeyLister{allErr: errors.New("connection refused")}
	var buf bytes.Buffer
	got := runKeyListWith(context.Background(), stub, "", &buf, discardKeyLog())
	if got != 1 {
		t.Errorf("exit code: want 1 (db error), got %d", got)
	}
}

// TestRenderKeyList_EmptyRows_OnlyHeader: render an empty slice, expect
// only the header line, no key_hash leakage even when there are no rows.
func TestRenderKeyList_EmptyRows_OnlyHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := renderKeyList(&buf, nil); err != nil {
		t.Fatalf("renderKeyList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ID") || !strings.Contains(out, "LAST_USED") {
		t.Errorf("header missing on empty input:\n%s", out)
	}
	if strings.Contains(out, "key_hash") {
		t.Errorf("empty render leaked key_hash:\n%s", out)
	}
}
