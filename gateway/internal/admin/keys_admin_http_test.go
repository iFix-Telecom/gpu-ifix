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

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// fakeKeysQueries records calls so a rejected body can be asserted to reach NO
// InsertAPIKey. RevokeAPIKey always returns nil (models the idempotent UPDATE).
type fakeKeysQueries struct {
	tenant       gen.GetTenantBySlugRow
	listRows     []gen.ListActiveKeysByTenantWithMetaRow
	insertCalls  int
	revokeCalls  int
	getSlugCalls int
}

func (f *fakeKeysQueries) GetTenantBySlug(_ context.Context, _ string) (gen.GetTenantBySlugRow, error) {
	f.getSlugCalls++
	return f.tenant, nil
}

func (f *fakeKeysQueries) ListActiveKeysByTenantWithMeta(_ context.Context, _ uuid.UUID) ([]gen.ListActiveKeysByTenantWithMetaRow, error) {
	return f.listRows, nil
}

func (f *fakeKeysQueries) InsertAPIKey(_ context.Context, arg gen.InsertAPIKeyParams) (gen.InsertAPIKeyRow, error) {
	f.insertCalls++
	return gen.InsertAPIKeyRow{
		ID:        uuid.New(),
		TenantID:  arg.TenantID,
		KeyHash:   arg.KeyHash,
		KeyPrefix: arg.KeyPrefix,
		DataClass: arg.DataClass,
	}, nil
}

func (f *fakeKeysQueries) RevokeAPIKey(_ context.Context, _ uuid.UUID) error {
	f.revokeCalls++
	return nil
}

// withSlug/withID inject a chi route param into the request so chi.URLParam
// resolves under httptest.
func withSlug(r *http.Request, slug string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("slug", slug)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func withID(r *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestKeyCreate_ReturnsRawOnceNoHash(t *testing.T) {
	fake := &fakeKeysQueries{tenant: gen.GetTenantBySlugRow{ID: uuid.New(), Slug: "alpha"}}
	h := newKeysAdminHandlerWithQueries(fake, discardLog())
	rec := httptest.NewRecorder()
	req := withSlug(httptest.NewRequest(http.MethodPost, "/admin/tenants/alpha/keys",
		strings.NewReader(`{"data_class":"normal"}`)), "alpha")
	h.Create(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"key"`) {
		t.Errorf("response must serialize the raw key once; body=%s", body)
	}
	if strings.Contains(body, "key_hash") || strings.Contains(body, "key_lookup_hash") {
		t.Errorf("response leaked a hash column; body=%s", body)
	}
	var out createKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Key == "" || out.DataClass != "normal" {
		t.Errorf("unexpected create body: %+v", out)
	}
}

func TestKeyCreate_InvalidDataClass_400_NoInsert(t *testing.T) {
	fake := &fakeKeysQueries{tenant: gen.GetTenantBySlugRow{ID: uuid.New(), Slug: "alpha"}}
	h := newKeysAdminHandlerWithQueries(fake, discardLog())
	rec := httptest.NewRecorder()
	req := withSlug(httptest.NewRequest(http.MethodPost, "/admin/tenants/alpha/keys",
		strings.NewReader(`{"data_class":"invalido"}`)), "alpha")
	h.Create(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if fake.insertCalls != 0 {
		t.Errorf("insertCalls = %d, want 0 (whitelist before query)", fake.insertCalls)
	}
	var env errEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Code != "invalid_data_class" {
		t.Errorf("error.code = %q, want invalid_data_class", env.Error.Code)
	}
}

func TestKeyCreate_DefaultDataClassNormal(t *testing.T) {
	fake := &fakeKeysQueries{tenant: gen.GetTenantBySlugRow{ID: uuid.New(), Slug: "alpha"}}
	h := newKeysAdminHandlerWithQueries(fake, discardLog())
	rec := httptest.NewRecorder()
	// Empty body → data_class defaults to "normal".
	req := withSlug(httptest.NewRequest(http.MethodPost, "/admin/tenants/alpha/keys",
		strings.NewReader("")), "alpha")
	h.Create(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	var out createKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.DataClass != "normal" {
		t.Errorf("data_class = %q, want normal (default)", out.DataClass)
	}
}

func TestKeyRevoke_InvalidID_400(t *testing.T) {
	fake := &fakeKeysQueries{}
	h := newKeysAdminHandlerWithQueries(fake, discardLog())
	rec := httptest.NewRecorder()
	req := withID(httptest.NewRequest(http.MethodPost, "/admin/keys/not-a-uuid/revoke", nil), "not-a-uuid")
	h.Revoke(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if fake.revokeCalls != 0 {
		t.Errorf("revokeCalls = %d, want 0", fake.revokeCalls)
	}
}

func TestKeyRevoke_Idempotent(t *testing.T) {
	fake := &fakeKeysQueries{}
	h := newKeysAdminHandlerWithQueries(fake, discardLog())
	id := uuid.New().String()
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := withID(httptest.NewRequest(http.MethodPost, "/admin/keys/"+id+"/revoke", nil), id)
		h.Revoke(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d status = %d, want 200", i+1, rec.Code)
		}
	}
	if fake.revokeCalls != 2 {
		t.Errorf("revokeCalls = %d, want 2", fake.revokeCalls)
	}
}

func TestKeyList_NoHashInBody(t *testing.T) {
	fake := &fakeKeysQueries{
		tenant: gen.GetTenantBySlugRow{ID: uuid.New(), Slug: "alpha"},
		listRows: []gen.ListActiveKeysByTenantWithMetaRow{
			{ID: uuid.New(), TenantSlug: "alpha", KeyPrefix: "ifix_****abcd", Status: "active", DataClass: "normal"},
		},
	}
	h := newKeysAdminHandlerWithQueries(fake, discardLog())
	rec := httptest.NewRecorder()
	req := withSlug(httptest.NewRequest(http.MethodGet, "/admin/tenants/alpha/keys", nil), "alpha")
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "key_hash") || strings.Contains(body, "key_lookup_hash") {
		t.Errorf("list leaked a hash column; body=%s", body)
	}
	var out []keyListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].KeyPrefix != "ifix_****abcd" {
		t.Errorf("unexpected list body: %+v", out)
	}
}
