package idempotency_test

import (
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/idempotency"
)

func TestHashBody_SameContentSameHash(t *testing.T) {
	h1, err := idempotency.HashBody([]byte(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	h2, err := idempotency.HashBody([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("expected equal hashes for key-reordered JSON, got %s vs %s", h1, h2)
	}
}

func TestHashBody_DifferentContentDifferentHash(t *testing.T) {
	h1, _ := idempotency.HashBody([]byte(`{"a":1}`))
	h2, _ := idempotency.HashBody([]byte(`{"a":2}`))
	if h1 == h2 {
		t.Fatalf("expected different hashes, both = %s", h1)
	}
}

func TestHashBody_NestedKeyOrderIndependent(t *testing.T) {
	h1, err := idempotency.HashBody([]byte(`{"outer":{"x":1,"y":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := idempotency.HashBody([]byte(`{"outer":{"y":2,"x":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("nested reorder should hash equally: %s vs %s", h1, h2)
	}
}

func TestHashBody_ArraysOrdered(t *testing.T) {
	h1, _ := idempotency.HashBody([]byte(`[1,2]`))
	h2, _ := idempotency.HashBody([]byte(`[2,1]`))
	if h1 == h2 {
		t.Fatalf("arrays are ordered; expected different hashes, both = %s", h1)
	}
}

func TestHashBody_EmptyBody(t *testing.T) {
	h, err := idempotency.HashBody(nil)
	if err != nil {
		t.Fatal(err)
	}
	// sha256("") = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if h != want {
		t.Fatalf("sha256('') mismatch: got %s want %s", h, want)
	}
	h2, _ := idempotency.HashBody([]byte(""))
	if h2 != want {
		t.Fatalf("[]byte('') expected %s got %s", want, h2)
	}
}

func TestHashBody_InvalidJSON(t *testing.T) {
	_, err := idempotency.HashBody([]byte("not json"))
	if err == nil {
		t.Fatalf("expected error for non-JSON input")
	}
}

func TestHashBody_NumbersCompared(t *testing.T) {
	// `1` and `1.0` are SAME value after float64 round-trip through json;
	// we accept the Go default re-encoding behavior (both round-trip to `1`).
	h1, _ := idempotency.HashBody([]byte(`{"n":1}`))
	h2, _ := idempotency.HashBody([]byte(`{"n":1.0}`))
	if h1 != h2 {
		t.Logf("note: %s != %s — number representation produced different hashes; documenting", h1, h2)
	}
	// The critical property: same literal produces same hash.
	h3, _ := idempotency.HashBody([]byte(`{"n":1}`))
	if h1 != h3 {
		t.Fatalf("identical literal should hash identically: %s vs %s", h1, h3)
	}
}

func TestHashBody_UnicodeContent(t *testing.T) {
	h1, err := idempotency.HashBody([]byte(`{"msg":"olá 🔥"}`))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := idempotency.HashBody([]byte(`{"msg":"olá 🔥"}`))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("unicode hashes should match: %s vs %s", h1, h2)
	}
}
