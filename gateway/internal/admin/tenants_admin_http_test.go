package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
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
	prefsCalls []gen.UpdateTenantProviderPrefsParams
}

func (f *fakeTenantQueries) ListTenants(_ context.Context) ([]gen.ListTenantsRow, error) {
	f.listCalls++
	return f.listRows, nil
}

func (f *fakeTenantQueries) UpdateTenantProviderPrefs(_ context.Context, arg gen.UpdateTenantProviderPrefsParams) (int64, error) {
	f.prefsCalls = append(f.prefsCalls, arg)
	if arg.Slug == "ghost" {
		return 0, nil
	}
	return 1, nil
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

// withTenantSlug injects the chi {slug} param.
func withTenantSlug(r *http.Request, slug string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("slug", slug)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// quick 260830-o2j — per-tenant provider_prefs write path.
func TestTenantSetProviderPrefs(t *testing.T) {
	fake := &fakeTenantQueries{}
	ref := &countingRefresher{}
	h := newTenantAdminHandlerWithQueries(fake, discardLog())
	h.refresher = ref

	// valid → canonical stored + refresh
	rec := httptest.NewRecorder()
	h.SetProviderPrefs(rec, withTenantSlug(httptest.NewRequest(http.MethodPut, "/admin/tenants/alpha/provider-prefs",
		strings.NewReader(`{"provider_prefs":{"only":["novita"],"data_collection":"deny","zdr":true}}`)), "alpha"))
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if len(fake.prefsCalls) != 1 || fake.prefsCalls[0].Slug != "alpha" || !strings.Contains(string(fake.prefsCalls[0].ProviderPrefs), `"zdr":true`) {
		t.Errorf("prefs calls = %+v", fake.prefsCalls)
	}
	if ref.calls != 1 {
		t.Errorf("refresh calls = %d", ref.calls)
	}

	// null → clears (NULL)
	rec = httptest.NewRecorder()
	h.SetProviderPrefs(rec, withTenantSlug(httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"provider_prefs":null}`)), "alpha"))
	if rec.Code != 200 || fake.prefsCalls[1].ProviderPrefs != nil {
		t.Errorf("clear: status %d prefs %s", rec.Code, fake.prefsCalls[1].ProviderPrefs)
	}

	// invalid → 400, no write
	rec = httptest.NewRecorder()
	h.SetProviderPrefs(rec, withTenantSlug(httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"provider_prefs":{"quantizations":["q4"]}}`)), "alpha"))
	if rec.Code != 400 || len(fake.prefsCalls) != 2 {
		t.Errorf("invalid: status %d calls %d", rec.Code, len(fake.prefsCalls))
	}

	// unknown slug → 404
	rec = httptest.NewRecorder()
	h.SetProviderPrefs(rec, withTenantSlug(httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"provider_prefs":{"zdr":true}}`)), "ghost"))
	if rec.Code != 404 {
		t.Errorf("ghost: status %d", rec.Code)
	}
}
