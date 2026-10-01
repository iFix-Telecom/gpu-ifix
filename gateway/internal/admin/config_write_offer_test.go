package admin

import (
	"net/http"
	"testing"
)

// TestConfigWrite_OfferPolicyFields — quick-261001-qdd. offer_mode is a closed
// enum, bid_margin is [1.00,5.00], max_preemptions_per_day is [0,20]. Invalid
// values are 400 with zero UPDATE calls (T-qdd-01).
func TestConfigWrite_OfferPolicyFields(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode int
		wantLast string
	}{
		{"offer_mode_bid", `{"field":"offer_mode","value":"bid","kind":"config"}`, http.StatusOK, "UpdatePodConfigFieldOfferMode"},
		{"offer_mode_ondemand", `{"field":"offer_mode","value":"ondemand","kind":"config"}`, http.StatusOK, "UpdatePodConfigFieldOfferMode"},
		{"offer_mode_invalid", `{"field":"offer_mode","value":"spot","kind":"config"}`, http.StatusBadRequest, ""},
		{"offer_mode_not_string", `{"field":"offer_mode","value":1,"kind":"config"}`, http.StatusBadRequest, ""},
		{"bid_margin_ok", `{"field":"bid_margin","value":1.25,"kind":"config"}`, http.StatusOK, "UpdatePodConfigFieldBidMargin"},
		{"bid_margin_low", `{"field":"bid_margin","value":0.9,"kind":"config"}`, http.StatusBadRequest, ""},
		{"bid_margin_high", `{"field":"bid_margin","value":5.5,"kind":"config"}`, http.StatusBadRequest, ""},
		{"max_preempt_zero", `{"field":"max_preemptions_per_day","value":0,"kind":"config"}`, http.StatusOK, "UpdatePodConfigFieldMaxPreemptionsPerDay"},
		{"max_preempt_ok", `{"field":"max_preemptions_per_day","value":3,"kind":"config"}`, http.StatusOK, "UpdatePodConfigFieldMaxPreemptionsPerDay"},
		{"max_preempt_neg", `{"field":"max_preemptions_per_day","value":-1,"kind":"config"}`, http.StatusBadRequest, ""},
		{"max_preempt_high", `{"field":"max_preemptions_per_day","value":21,"kind":"config"}`, http.StatusBadRequest, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeWriteQueries{}
			h := newPrimaryConfigWriteHandlerWithQueries(fake, writeTestLoader(), discardLog())
			rec := doWriteRequest(t, h, c.body)
			if rec.Code != c.wantCode {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, c.wantCode, rec.Body.String())
			}
			if c.wantLast == "" {
				if fake.calls != 0 {
					t.Errorf("calls=%d, want 0", fake.calls)
				}
				return
			}
			if fake.calls != 1 || fake.last != c.wantLast {
				t.Errorf("calls=%d last=%q, want 1 %s", fake.calls, fake.last, c.wantLast)
			}
		})
	}
}
