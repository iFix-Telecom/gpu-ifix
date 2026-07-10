// Package vast — unit tests for the REST client. All tests use
// httptest.Server fixtures (no real Vast.ai traffic) so the suite is
// deterministic and zero-cost in CI.
//
// Threat coverage (T-6-01): TestClientNeverLogsAPIKey scans the client.go
// source file for any pattern that would forward the API key into a
// logger or Sentry call. The runtime.Caller pattern (W12 fix 2026-05-13)
// resolves the source path relative to THIS test file's location, so the
// test passes regardless of the working directory `go test` is invoked
// from (CI runs it from the repo root, dev sometimes from the package).
package vast

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// helper: spawn an httptest.Server with a single dispatcher handler.
func newTestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

// newTestTLSServer spawns an httptest TLS server (https://) — required by
// the OnstartLog tests because FetchLogs rejects any non-https result_url
// (SSRF guard, finding #8). Callers must set `c.httpClient = s.Client()` so
// the client trusts the server's self-signed cert.
func newTestTLSServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewTLSServer(h)
	t.Cleanup(s.Close)
	return s
}

// TestOnstartLog_Available — PUT returns result_url, the presigned GET
// returns non-empty text → Status=Available, Text set. Also asserts the
// auth header IS on the PUT and ABSENT on the result_url GET (item 6).
func TestOnstartLog_Available(t *testing.T) {
	const apiKey = "k"
	var putAuth, getAuth string
	srv := newTestTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/instances/request_logs/123/":
			putAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"success":true,"result_url":"REPLACED/logs/blob"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/logs/blob":
			getAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte("[download-weights] progress key=qwen bytes=123\n"))
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	})
	// The result_url must point back at this same TLS server (same trusted
	// cert). Rewrite the placeholder now that srv.URL is known.
	rewriter := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/instances/request_logs/123/":
			putAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"success":true,"result_url":"` + srv.URL + `/logs/blob"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/logs/blob":
			getAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte("[download-weights] progress key=qwen bytes=123\n"))
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}
	srv.Config.Handler = http.HandlerFunc(rewriter)

	c := NewClientWithBaseURL(apiKey, srv.URL)
	c.httpClient = srv.Client()

	res, err := c.OnstartLog(context.Background(), 123)
	require.NoError(t, err, "all expected statuses return err==nil")
	require.Equal(t, OnstartLogAvailable, res.Status)
	require.Contains(t, res.Text, "bytes=123")
	require.Equal(t, "Bearer "+apiKey, putAuth, "auth header must be on the PUT")
	require.Empty(t, getAuth, "presigned result_url GET must carry NO auth header")
}

// TestOnstartLog_NotReady — PUT returns no result_url yet (async result
// still cooking) → Status=NotReady, non-fatal, Text=="".
func TestOnstartLog_NotReady(t *testing.T) {
	srv := newTestTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	c.httpClient = srv.Client()

	res, err := c.OnstartLog(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, OnstartLogNotReady, res.Status)
	require.Empty(t, res.Text)
}

// TestOnstartLog_FetchError_GET5xx — PUT ok, but the result_url GET returns
// 5xx → Status=FetchError (folded, non-fatal to the caller).
func TestOnstartLog_FetchError_GET5xx(t *testing.T) {
	var srv *httptest.Server
	srv = newTestTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			_, _ = w.Write([]byte(`{"result_url":"` + srv.URL + `/logs/blob"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := NewClientWithBaseURL("k", srv.URL)
	c.httpClient = srv.Client()

	res, err := c.OnstartLog(context.Background(), 1)
	require.NoError(t, err, "FetchError must NOT surface as a fatal error")
	require.Equal(t, OnstartLogFetchError, res.Status)
}

// TestOnstartLog_FetchError_PUTNon200 — PUT itself returns non-200 →
// Status=FetchError (folded, non-fatal).
func TestOnstartLog_FetchError_PUTNon200(t *testing.T) {
	srv := newTestTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"msg":"logs API down"}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	c.httpClient = srv.Client()

	res, err := c.OnstartLog(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, OnstartLogFetchError, res.Status)
}

// TestOnstartLog_Empty — GET returns 200 with whitespace-only body →
// Status=Empty (UNKNOWN, non-fatal).
func TestOnstartLog_Empty(t *testing.T) {
	var srv *httptest.Server
	srv = newTestTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			_, _ = w.Write([]byte(`{"result_url":"` + srv.URL + `/logs/blob"}`))
			return
		}
		_, _ = w.Write([]byte("   \n\t  "))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	c.httpClient = srv.Client()

	res, err := c.OnstartLog(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, OnstartLogEmpty, res.Status)
}

// TestOnstartLog_NonHTTPSResultURL_FetchError — SSRF guard (finding #8): a
// result_url with a non-https scheme is rejected BEFORE the GET → FetchError,
// and the GET handler is never reached.
func TestOnstartLog_NonHTTPSResultURL_FetchError(t *testing.T) {
	getCalled := false
	srv := newTestTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			// Point at an http:// (non-https) internal-looking address.
			_, _ = w.Write([]byte(`{"result_url":"http://169.254.169.254/latest/meta-data"}`))
			return
		}
		getCalled = true
		_, _ = w.Write([]byte("should never be fetched"))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	c.httpClient = srv.Client()

	res, err := c.OnstartLog(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, OnstartLogFetchError, res.Status, "non-https result_url must map to FetchError")
	require.False(t, getCalled, "SSRF guard must reject before making the GET")
}

// TestNewClient_HTTPTimeoutIs30s asserts the package-level constant per
// CONTEXT.md D-A1.
func TestNewClient_HTTPTimeoutIs30s(t *testing.T) {
	c := NewClient("test-key")
	require.Equal(t, 30*time.Second, c.HTTPTimeout(),
		"D-A1: vast.Client.httpClient.Timeout MUST be 30s (package-level, NOT env-tunable)")
}

// TestNewClient_DefaultBaseURL asserts the canonical host. Catches a
// regression if someone resets it back to vast.ai (legacy).
func TestNewClient_DefaultBaseURL(t *testing.T) {
	c := NewClient("test-key")
	require.Equal(t, "https://console.vast.ai/api/v0", c.baseURL,
		"DefaultBaseURL must point at console.vast.ai (legacy vast.ai/api/v0 returns 308)")
}

// TestClientAuthHeader — every request carries Authorization: Bearer ${apiKey}.
// httptest.Server inspects the header on the wire to prove the key reaches
// the server (and only there).
func TestClientAuthHeader(t *testing.T) {
	const apiKey = "test-api-key-12345"
	var observed string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		observed = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"id":1,"email":"x@y"}`))
	})
	c := NewClientWithBaseURL(apiKey, srv.URL)
	require.NoError(t, c.Ping(context.Background()))
	require.Equal(t, "Bearer "+apiKey, observed,
		"Authorization header must be `Bearer ${apiKey}` exactly")
}

// TestClientNeverLogsAPIKey — T-6-01 mitigation. Greps client.go for any
// reference to apiKey alongside log/sentry/errors/fmt.Sprintf calls.
//
// W12 fix (2026-05-13): use runtime.Caller(0) to resolve client.go
// relative to this test file. Catches both `go test ./gateway/internal/emerg/vast/`
// (cwd = package) and `go test ./...` (cwd = repo root).
func TestClientNeverLogsAPIKey(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller(0) must succeed")
	clientPath := filepath.Join(filepath.Dir(thisFile), "client.go")
	data, err := os.ReadFile(clientPath)
	require.NoError(t, err, "client.go must be readable from %s", clientPath)
	src := string(data)

	// The API key MUST NOT flow into any of these sinks.
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`log\.[A-Z][a-z]+\([^)]*apiKey`),
		regexp.MustCompile(`slog\.[A-Z][a-z]+\([^)]*apiKey`),
		regexp.MustCompile(`sentry\.[A-Za-z]+\([^)]*apiKey`),
		regexp.MustCompile(`fmt\.[A-Za-z]+\([^)]*c\.apiKey`),
		regexp.MustCompile(`errors\.New\([^)]*apiKey`),
		// Match *any* assignment of c.apiKey into a string/error
		// other than via setAuthHeader's `"Bearer "+c.apiKey` literal.
		regexp.MustCompile(`Sprintf\([^)]*apiKey`),
	}
	for _, re := range forbidden {
		matches := re.FindAllString(src, -1)
		require.Empty(t, matches,
			"T-6-01 violation: pattern %q matched in client.go: %v", re.String(), matches)
	}

	// Positive assertion: the key MUST appear in setAuthHeader (proves
	// the test's pattern would catch a real leak — i.e. the test isn't
	// trivially passing because apiKey is unused).
	require.Regexp(t, regexp.MustCompile(`"Bearer "\+c\.apiKey`), src,
		"client.go must reference apiKey in setAuthHeader's Bearer prefix; if not, the T-6-01 grep is a tautology")
}

// TestSearchOffers_HappyPath — server returns 1 offer; client parses + returns it.
func TestSearchOffers_HappyPath(t *testing.T) {
	var capturedQ string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/bundles", r.URL.Path)
		capturedQ = r.URL.Query().Get("q")
		_, _ = w.Write([]byte(`{"offers":[{"id":35956479,"dph_total":0.336,"gpu_name":"RTX 4090","reliability":0.99,"host_id":120840,"machine_id":29286,"inet_down":5453.6,"cuda_max_good":12.6,"rentable":true,"num_gpus":1}]}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	offers, err := c.SearchOffers(context.Background(), DefaultSearchFilter(0.40, 0, "RTX 4090", 1))
	require.NoError(t, err)
	require.Len(t, offers, 1)
	require.Equal(t, int64(35956479), offers[0].ID)
	require.InDelta(t, 0.336, offers[0].DphTotal, 0.0001)
	require.Equal(t, int64(120840), offers[0].HostID)

	// Filter shape sanity: must be valid JSON containing the canonical D-A2 fields.
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(capturedQ), &parsed))
	require.Contains(t, parsed, "gpu_name")
	require.Contains(t, parsed, "reliability")
	require.Contains(t, parsed, "dph_total")
}

// TestSearchOffers_PrimaryHostExcluded — when primaryHostID > 0, filter
// must include host_id: {neq: primaryHostID}.
func TestSearchOffers_PrimaryHostExcluded(t *testing.T) {
	f := DefaultSearchFilter(0.40, 999, "RTX 4090", 1)
	hostFilter, ok := f["host_id"].(map[string]any)
	require.True(t, ok, "host_id filter present when primaryHostID > 0")
	require.Equal(t, int64(999), hostFilter["neq"])
}

// TestSearchOffers_PrimaryHostUnknown — primaryHostID == 0 omits the filter.
func TestSearchOffers_PrimaryHostUnknown(t *testing.T) {
	f := DefaultSearchFilter(0.40, 0, "RTX 4090", 1)
	_, ok := f["host_id"]
	require.False(t, ok, "host_id filter MUST be absent when primaryHostID == 0 (D-A2)")
}

// TestCreateInstance_HappyPath — 200 + {success:true, new_contract:N}.
func TestCreateInstance_HappyPath(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPut, r.Method)
		require.Equal(t, "/asks/12345/", r.URL.Path)
		_, _ = w.Write([]byte(`{"success":true,"new_contract":99999}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	inst, err := c.CreateInstance(context.Background(), 12345, CreateRequest{
		ClientID: "me",
		Image:    "ghcr.io/x/y:latest",
		Disk:     50,
	})
	require.NoError(t, err)
	require.Equal(t, int64(99999), inst.ID)
}

// TestCreateInstance_404_OfferGone — sentinel error mapping per D-A3.
func TestCreateInstance_404_OfferGone(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"error":"no_such_ask","msg":"already taken"}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	_, err := c.CreateInstance(context.Background(), 999, CreateRequest{ClientID: "me"})
	require.ErrorIs(t, err, ErrOfferGone, "404 + no_such_ask must map to ErrOfferGone")
}

// TestCreateInstance_410_OfferGone — 410 + "no longer available" message
// also maps to ErrOfferGone (per parseErrorBody fallback).
func TestCreateInstance_410_OfferGone(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"msg":"This offer is no longer available"}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	_, err := c.CreateInstance(context.Background(), 999, CreateRequest{ClientID: "me"})
	require.ErrorIs(t, err, ErrOfferGone, "410 + 'no longer available' must map to ErrOfferGone")
}

// TestCreateInstance_HTTP200_SuccessFalse — defensive: HTTP 200 + success=false
// must NOT be treated as success (returns *VastError, never silently passes).
func TestCreateInstance_HTTP200_SuccessFalse(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"new_contract":0}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	_, err := c.CreateInstance(context.Background(), 1, CreateRequest{ClientID: "me"})
	require.Error(t, err)
	var ve *VastError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "create_failed", ve.Code)
}

// TestParseErrorBody_429_RateLimited.
func TestParseErrorBody_429_RateLimited(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	err := c.Ping(context.Background())
	require.ErrorIs(t, err, ErrRateLimited)
}

// TestParseErrorBody_401_Unauthorized.
func TestParseErrorBody_401_Unauthorized(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_key"}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	err := c.Ping(context.Background())
	require.ErrorIs(t, err, ErrUnauthorized)
}

// TestParseErrorBody_403_Unauthorized — 403 also maps to ErrUnauthorized
// (per D-A5 fail-loud).
func TestParseErrorBody_403_Unauthorized(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	c := NewClientWithBaseURL("k", srv.URL)
	err := c.Ping(context.Background())
	require.ErrorIs(t, err, ErrUnauthorized)
}

// TestParseErrorBody_503_VastError — 5xx returns *VastError (not a sentinel)
// so the lifecycle can decide retry vs abort based on Status.
func TestParseErrorBody_503_VastError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"msg":"Vast under maintenance"}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	err := c.Ping(context.Background())
	require.Error(t, err)
	var ve *VastError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, http.StatusServiceUnavailable, ve.Status)
	require.Equal(t, "server_error", ve.Code)
	require.Contains(t, ve.Msg, "maintenance")
}

// TestPing_HappyPath — 200 OK returns nil.
func TestPing_HappyPath(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/users/current", r.URL.Path)
		_, _ = w.Write([]byte(`{"id":1,"email":"test@ifix"}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	require.NoError(t, c.Ping(context.Background()))
}

// TestGetInstance_RunningHasPorts — happy path. Captures the spike
// finding: ports field populated when actual_status==running.
func TestGetInstance_RunningHasPorts(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/instances/12345/", r.URL.Path)
		_, _ = w.Write([]byte(`{"instances":{"id":12345,"actual_status":"running","public_ipaddr":"1.2.3.4","ports":{"9100/tcp":[{"HostIp":"0.0.0.0","HostPort":"40713"}]}}}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	inst, err := c.GetInstance(context.Background(), 12345)
	require.NoError(t, err)
	require.Equal(t, "running", inst.ActualStatus)
	require.Equal(t, "1.2.3.4", inst.PublicIPAddr)
	bindings, ok := inst.Ports["9100/tcp"]
	require.True(t, ok, "Ports map must contain '9100/tcp' key for the running instance")
	require.Len(t, bindings, 1)
	require.Equal(t, "40713", bindings[0].HostPort)
}

// TestGetInstance_LoadingNoPorts — W6 invariant: pre-running, ports is empty.
func TestGetInstance_LoadingNoPorts(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"instances":{"id":12345,"actual_status":"loading","public_ipaddr":"1.2.3.4","ports":{}}}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	inst, err := c.GetInstance(context.Background(), 12345)
	require.NoError(t, err)
	require.Equal(t, "loading", inst.ActualStatus)
	require.Empty(t, inst.Ports, "ports map must be empty pre-running (W6)")
	require.True(t, inst.IsActive(), "loading is non-terminal (IsActive)")
	require.False(t, inst.IsTerminal())
}

// TestGetInstance_TerminalState — exited/unknown/offline.
func TestGetInstance_TerminalState(t *testing.T) {
	for _, status := range []string{"exited", "unknown", "offline"} {
		t.Run(status, func(t *testing.T) {
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				body := `{"instances":{"id":12345,"actual_status":"` + status + `"}}`
				_, _ = w.Write([]byte(body))
			})
			c := NewClientWithBaseURL("k", srv.URL)
			inst, err := c.GetInstance(context.Background(), 12345)
			require.NoError(t, err)
			require.True(t, inst.IsTerminal(), "%s must be terminal", status)
			require.False(t, inst.IsActive())
		})
	}
}

// TestGetInstance_Destroyed — Vast convention: HTTP 200 + {"instances": null}.
func TestGetInstance_DestroyedReturnsNotFound(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"instances":null}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	_, err := c.GetInstance(context.Background(), 12345)
	require.ErrorIs(t, err, ErrInstanceNotFound,
		"`{instances: null}` must map to ErrInstanceNotFound (verified during spike)")
}

// TestGetInstance_404_NotFound — explicit 404 + no_such_instance.
func TestGetInstance_404_NotFound(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no_such_instance"}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	_, err := c.GetInstance(context.Background(), 12345)
	require.ErrorIs(t, err, ErrInstanceNotFound)
}

// TestDestroyInstance_HappyPath — 200 OK returns nil.
func TestDestroyInstance_HappyPath(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		require.Equal(t, "/instances/12345/", r.URL.Path)
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	require.NoError(t, c.DestroyInstance(context.Background(), 12345))
}

// TestDestroyInstance_404_Idempotent — 404 + no_such_instance is success
// (the instance is gone — that's what the caller wanted). Per RESEARCH
// lines 717-719.
func TestDestroyInstance_404_Idempotent(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no_such_instance","msg":"already destroyed"}`))
	})
	c := NewClientWithBaseURL("k", srv.URL)
	err := c.DestroyInstance(context.Background(), 12345)
	require.NoError(t, err, "DELETE returning 404+no_such_instance must be treated as idempotent success")
}

// TestDestroyInstance_500_Surfaces — 5xx surfaces as *VastError so caller
// can decide retry. Distinguishes accidental success from a real error.
func TestDestroyInstance_500_Surfaces(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := NewClientWithBaseURL("k", srv.URL)
	err := c.DestroyInstance(context.Background(), 12345)
	require.Error(t, err)
	var ve *VastError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, http.StatusInternalServerError, ve.Status)
}

// TestVastError_NeverIncludesAPIKey — VastError.Error() formatting must
// not include any apiKey-derived data, even if the caller wraps with %w.
func TestVastError_NeverIncludesAPIKey(t *testing.T) {
	e := &VastError{Status: 500, Code: "server_error", Msg: "broken"}
	got := e.Error()
	require.False(t, strings.Contains(got, "Bearer"),
		"VastError.Error() must never contain 'Bearer' (might be confused with auth header on log scrape)")
	require.False(t, strings.Contains(got, "Authorization"),
		"VastError.Error() must never contain 'Authorization'")
}
