package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

func TestPrimaryControl_NilRedis503(t *testing.T) {
	h := NewPrimaryControlHandler(nil, "replica-a", discardLog())
	rec := httptest.NewRecorder()
	h.ForceUp(rec, httptest.NewRequest(http.MethodPost, "/admin/primary/force-up", strings.NewReader(`{"reason":"x"}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d want 503", rec.Code)
	}
}

func TestPrimaryControl_PublishesSameEventAsGatewayctl(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := rdb.Subscribe(ctx, redisx.PrimaryEventsChannel)
	if _, err := sub.Receive(ctx); err != nil { // subscription confirmation
		t.Fatal(err)
	}
	ch := sub.Channel()

	h := NewPrimaryControlHandler(rdb, "replica-a", discardLog())

	rec := httptest.NewRecorder()
	h.ForceUp(rec, httptest.NewRequest(http.MethodPost, "/admin/primary/force-up", strings.NewReader(`{"reason":"teste\nquebra"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["queued"] != true || resp["type"] != "force_up_request" {
		t.Errorf("resp = %v", resp)
	}

	select {
	case msg := <-ch:
		var ev redisx.PrimaryEvent
		if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type != "force_up_request" || ev.ReplicaID != "replica-a" {
			t.Errorf("event = %+v", ev)
		}
		if !strings.HasPrefix(ev.Reason, "dashboard:") || strings.Contains(ev.Reason, "\n") {
			t.Errorf("reason not sanitized/prefixed: %q", ev.Reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event published on gw:primary:events")
	}

	// force-down, empty body → default reason.
	rec = httptest.NewRecorder()
	h.ForceDown(rec, httptest.NewRequest(http.MethodPost, "/admin/primary/force-down", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("force-down status %d body %s", rec.Code, rec.Body.String())
	}
	select {
	case msg := <-ch:
		var ev redisx.PrimaryEvent
		_ = json.Unmarshal([]byte(msg.Payload), &ev)
		if ev.Type != "force_down_request" || ev.Reason != "dashboard:manual" {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no force-down event")
	}
}
