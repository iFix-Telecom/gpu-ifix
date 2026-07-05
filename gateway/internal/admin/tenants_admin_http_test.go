package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// fakeTenantQueries records calls so a rejected body can be asserted to reach
// NO query. createErr, when set, is returned by CreateTenant (used to simulate
// the 23505 unique_violation).
type fakeTenantQueries struct {
	listRows   []gen.ListTenantsRow
	createErr  error
	listCalls  int
	createCall int
}

func (f *fakeTenantQueries) ListTenants(_ context.Context) ([]gen.ListTenantsRow, error) {
	f.listCalls++
	return f.listRows, nil
}

func (f *fakeTenantQueries) CreateTenant(_ context.Context, arg gen.CreateTenantParams) (gen.CreateTenantRow, error) {
	f.createCall++
	if f.createErr != nil {
		return gen.CreateTenantRow{}, f.createErr
	}
	return gen.CreateTenantRow{ID: uuid.New(), Slug: arg.Slug, Name: arg.Name}, nil
}

func TestTenantList_ReturnsAll(t *testing.T) {
	fake := &fakeTenantQueries{listRows: []gen.ListTenantsRow{
		{ID: uuid.New(), Slug: "alpha", Name: "Alpha"},
		{ID: uuid.New(), Slug: "beta", Name: "Beta"},
	}}
	h := newTenantAdminHandlerWithQueries(fake, discardLog())
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/admin/tenants", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var out []tenantResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 || out[0].Slug != "alpha" || out[1].Slug != "beta" {
		t.Errorf("unexpected list body: %+v", out)
	}
}

func TestTenantCreate_OK(t *testing.T) {
	fake := &fakeTenantQueries{}
	h := newTenantAdminHandlerWithQueries(fake, discardLog())
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/admin/tenants",
		strings.NewReader(`{"slug":"gamma","name":"Gamma"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	var out tenantResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID == "" || out.Slug != "gamma" {
		t.Errorf("unexpected create body: %+v", out)
	}
	if fake.createCall != 1 {
		t.Errorf("createCall = %d, want 1", fake.createCall)
	}
}

func TestTenantCreate_EmptySlug_400_NoQuery(t *testing.T) {
	fake := &fakeTenantQueries{}
	h := newTenantAdminHandlerWithQueries(fake, discardLog())
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/admin/tenants",
		strings.NewReader(`{"slug":"  ","name":"NoSlug"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if fake.createCall != 0 {
		t.Errorf("createCall = %d, want 0 (validation before query)", fake.createCall)
	}
}

func TestTenantCreate_DuplicateSlug_409(t *testing.T) {
	fake := &fakeTenantQueries{createErr: &pgconn.PgError{Code: "23505"}}
	h := newTenantAdminHandlerWithQueries(fake, discardLog())
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/admin/tenants",
		strings.NewReader(`{"slug":"dup","name":"Dup"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	var env errEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Code != "tenant_exists" {
		t.Errorf("error.code = %q, want tenant_exists", env.Error.Code)
	}
}
