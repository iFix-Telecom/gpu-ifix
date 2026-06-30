package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/podconfig"
)

// fakeWriteQueries records every UpdatePodConfig* call so a test can assert
// the call-count (an out-of-bound value must reach NO query). last carries the
// name of the last query that fired.
type fakeWriteQueries struct {
	calls int
	last  string
}

func (f *fakeWriteQueries) hit(name string) error { f.calls++; f.last = name; return nil }

func (f *fakeWriteQueries) UpdatePodConfigFieldBlocklist(_ context.Context, _ []int64) error {
	return f.hit("UpdatePodConfigFieldBlocklist")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldAllowlist(_ context.Context, _ []int64) error {
	return f.hit("UpdatePodConfigFieldAllowlist")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldCapPrimary(_ context.Context, _ pgtype.Numeric) error {
	return f.hit("UpdatePodConfigFieldCapPrimary")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldCapFallback(_ context.Context, _ pgtype.Numeric) error {
	return f.hit("UpdatePodConfigFieldCapFallback")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldHostID(_ context.Context, _ int64) error {
	return f.hit("UpdatePodConfigFieldHostID")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldRejectPrivateIP(_ context.Context, _ bool) error {
	return f.hit("UpdatePodConfigFieldRejectPrivateIP")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldColdstartBudgetS(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigFieldColdstartBudgetS")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldPortBindBudgetS(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigFieldPortBindBudgetS")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldFailureCooldownS(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigFieldFailureCooldownS")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldMonthlyBudgetBRL(_ context.Context, _ pgtype.Numeric) error {
	return f.hit("UpdatePodConfigFieldMonthlyBudgetBRL")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldScheduleUpHour(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigFieldScheduleUpHour")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldScheduleDownHour(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigFieldScheduleDownHour")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldScheduleDays(_ context.Context, _ []string) error {
	return f.hit("UpdatePodConfigFieldScheduleDays")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldGraceRampDownS(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigFieldGraceRampDownS")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldProvisionLeadS(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigFieldProvisionLeadS")
}
func (f *fakeWriteQueries) UpdatePodConfigFieldScheduleDisabled(_ context.Context, _ bool) error {
	return f.hit("UpdatePodConfigFieldScheduleDisabled")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundCapPrimaryMin(_ context.Context, _ pgtype.Numeric) error {
	return f.hit("UpdatePodConfigBoundCapPrimaryMin")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundCapPrimaryMax(_ context.Context, _ pgtype.Numeric) error {
	return f.hit("UpdatePodConfigBoundCapPrimaryMax")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundCapFallbackMin(_ context.Context, _ pgtype.Numeric) error {
	return f.hit("UpdatePodConfigBoundCapFallbackMin")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundCapFallbackMax(_ context.Context, _ pgtype.Numeric) error {
	return f.hit("UpdatePodConfigBoundCapFallbackMax")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundColdstartBudgetSMin(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundColdstartBudgetSMin")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundColdstartBudgetSMax(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundColdstartBudgetSMax")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundPortBindBudgetSMin(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundPortBindBudgetSMin")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundPortBindBudgetSMax(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundPortBindBudgetSMax")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundFailureCooldownSMin(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundFailureCooldownSMin")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundFailureCooldownSMax(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundFailureCooldownSMax")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundMonthlyBudgetBRLMin(_ context.Context, _ pgtype.Numeric) error {
	return f.hit("UpdatePodConfigBoundMonthlyBudgetBRLMin")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundMonthlyBudgetBRLMax(_ context.Context, _ pgtype.Numeric) error {
	return f.hit("UpdatePodConfigBoundMonthlyBudgetBRLMax")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundScheduleUpHourMin(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundScheduleUpHourMin")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundScheduleUpHourMax(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundScheduleUpHourMax")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundScheduleDownHourMin(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundScheduleDownHourMin")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundScheduleDownHourMax(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundScheduleDownHourMax")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundGraceRampDownSMin(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundGraceRampDownSMin")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundGraceRampDownSMax(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundGraceRampDownSMax")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundProvisionLeadSMin(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundProvisionLeadSMin")
}
func (f *fakeWriteQueries) UpdatePodConfigBoundProvisionLeadSMax(_ context.Context, _ int32) error {
	return f.hit("UpdatePodConfigBoundProvisionLeadSMax")
}

// writeTestLoader builds a static loader with a known cfg + bounds so the
// validation reads a deterministic LIVE bound.
func writeTestLoader() *podconfig.Loader {
	cfg := podconfig.PodConfig{
		CapPrimary:       0.6,
		ScheduleUpHour:   9,
		ScheduleDownHour: 17,
	}
	bounds := podconfig.PodConfigBounds{
		CapPrimaryMin:       0.1,
		CapPrimaryMax:       2.2,
		ScheduleUpHourMin:   0,
		ScheduleUpHourMax:   23,
		ScheduleDownHourMin: 0,
		ScheduleDownHourMax: 23,
	}
	return podconfig.NewStaticLoaderForTest(cfg, podconfig.ScheduleRule{}, bounds, discardLog())
}

func doWriteRequest(t *testing.T, h *PrimaryConfigWriteHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/admin/primary/config", strings.NewReader(body))
	h.ServeHTTP(rec, req)
	return rec
}

func TestConfigWrite_ConfigField_OK(t *testing.T) {
	fake := &fakeWriteQueries{}
	h := newPrimaryConfigWriteHandlerWithQueries(fake, writeTestLoader(), discardLog())
	rec := doWriteRequest(t, h, `{"field":"blocklist","value":[1,2,3],"kind":"config"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if fake.calls != 1 || fake.last != "UpdatePodConfigFieldBlocklist" {
		t.Errorf("calls=%d last=%q, want 1 UpdatePodConfigFieldBlocklist", fake.calls, fake.last)
	}
}

func TestConfigWrite_Bound_OK(t *testing.T) {
	fake := &fakeWriteQueries{}
	h := newPrimaryConfigWriteHandlerWithQueries(fake, writeTestLoader(), discardLog())
	rec := doWriteRequest(t, h, `{"field":"cap_primary_max","value":1.8,"kind":"bound"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if fake.calls != 1 || fake.last != "UpdatePodConfigBoundCapPrimaryMax" {
		t.Errorf("calls=%d last=%q, want 1 UpdatePodConfigBoundCapPrimaryMax", fake.calls, fake.last)
	}
}

func TestConfigWrite_ConfigOutOfBound_Rejected(t *testing.T) {
	fake := &fakeWriteQueries{}
	h := newPrimaryConfigWriteHandlerWithQueries(fake, writeTestLoader(), discardLog())
	// cap_primary 5.0 exceeds the live CapPrimaryMax (2.2).
	rec := doWriteRequest(t, h, `{"field":"cap_primary","value":5.0,"kind":"config"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if fake.calls != 0 {
		t.Errorf("calls=%d, want 0 (no UPDATE on out-of-bound value)", fake.calls)
	}
	var env errEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Code != "validation_error" {
		t.Errorf("error.code = %q, want validation_error", env.Error.Code)
	}
}

func TestConfigWrite_BoundMinGteMax_Rejected(t *testing.T) {
	fake := &fakeWriteQueries{}
	h := newPrimaryConfigWriteHandlerWithQueries(fake, writeTestLoader(), discardLog())
	// cap_primary_min 3.0 >= live CapPrimaryMax (2.2) → invalid pair.
	rec := doWriteRequest(t, h, `{"field":"cap_primary_min","value":3.0,"kind":"bound"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if fake.calls != 0 {
		t.Errorf("calls=%d, want 0", fake.calls)
	}
}

func TestConfigWrite_DownHourEqualsUpHour_Rejected(t *testing.T) {
	fake := &fakeWriteQueries{}
	h := newPrimaryConfigWriteHandlerWithQueries(fake, writeTestLoader(), discardLog())
	// schedule_up_hour 17 == live ScheduleDownHour (17) → zero-length window.
	rec := doWriteRequest(t, h, `{"field":"schedule_up_hour","value":17,"kind":"config"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if fake.calls != 0 {
		t.Errorf("calls=%d, want 0", fake.calls)
	}
}

func TestConfigWrite_UnknownField_Rejected(t *testing.T) {
	fake := &fakeWriteQueries{}
	h := newPrimaryConfigWriteHandlerWithQueries(fake, writeTestLoader(), discardLog())
	rec := doWriteRequest(t, h, `{"field":"definitely_not_a_column","value":1,"kind":"config"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if fake.calls != 0 {
		t.Errorf("calls=%d, want 0", fake.calls)
	}
}
