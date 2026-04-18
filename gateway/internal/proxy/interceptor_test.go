package proxy

import (
	"errors"
	"net/http"
	"testing"
)

type recordingInterceptor struct {
	calls   *int
	returns error
}

func (r recordingInterceptor) Intercept(resp *http.Response) error {
	_ = resp
	*r.calls++
	return r.returns
}

func TestComposeInterceptors_Empty(t *testing.T) {
	fn := ComposeInterceptors()
	resp := &http.Response{StatusCode: 200, Header: http.Header{}}
	if err := fn(resp); err != nil {
		t.Fatalf("empty compose must return nil, got %v", err)
	}
}

func TestComposeInterceptors_OrderPreserved(t *testing.T) {
	a, b := 0, 0
	fn := ComposeInterceptors(
		recordingInterceptor{calls: &a},
		recordingInterceptor{calls: &b},
	)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}}
	if err := fn(resp); err != nil {
		t.Fatal(err)
	}
	if a != 1 || b != 1 {
		t.Fatalf("expected both called once; a=%d b=%d", a, b)
	}
}

func TestComposeInterceptors_StopsOnFirstError(t *testing.T) {
	a, b := 0, 0
	bad := errors.New("boom")
	fn := ComposeInterceptors(
		recordingInterceptor{calls: &a, returns: bad},
		recordingInterceptor{calls: &b},
	)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}}
	if err := fn(resp); err == nil || !errors.Is(err, bad) {
		t.Fatalf("expected wrapped bad; got %v", err)
	}
	if a != 1 || b != 0 {
		t.Fatalf("expected a=1 b=0; got a=%d b=%d", a, b)
	}
}
