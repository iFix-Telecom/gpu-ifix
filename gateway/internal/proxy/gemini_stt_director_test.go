package proxy

// Phase 11.2 Plan 01 — Wave 0 RED stubs for gemini-stt director (D-B4).
//
// These tests pin the contract for `BuildGeminiSTTDirector` (Plan 06).
// They are all skipped until Plan 06 wires the director — see PATTERNS.md
// lines 104-114 (test pattern map) for expected assertion shapes.
//
// Analog: gateway/internal/proxy/openai_whisper_director_test.go.
// Critical pitfall references: RESEARCH §Pitfall 3 (x-goog-api-key strip).

import "testing"

// TestBuildGeminiSTTDirector_SetsXGoogApiKeyHeader — D-B4 / Pitfall 3.
// After base director strips Authorization, gemini director MUST set
// `x-goog-api-key: <bearer>` header verbatim (NOT X-API-Key — that gets
// stripped). See PATTERNS.md Shared Patterns §Authentication / Header Hygiene.
func TestBuildGeminiSTTDirector_SetsXGoogApiKeyHeader(t *testing.T) {
	t.Skip("OWNER: Plan 06 — implements BuildGeminiSTTDirector; unskip + assert capturedReq.Header.Get(\"x-goog-api-key\") == bearer")
	// Expected:
	//   require.NotEmpty(t, capturedReq.Header.Get("x-goog-api-key"))
	//   require.Equal(t, "<bearer>", capturedReq.Header.Get("x-goog-api-key"))
	// Reference: PATTERNS.md line 110-112, openai_whisper_director.go:66 analog.
}

// TestBuildGeminiSTTDirector_StripsAuthorizationHeader — D-B4 / Pitfall 3.
// `Authorization: Bearer ...` MUST be removed before forwarding so Google
// AI Studio doesn't reject. BuildDirector base strips it; assertion pins
// that gemini director does NOT re-introduce it after the swap.
func TestBuildGeminiSTTDirector_StripsAuthorizationHeader(t *testing.T) {
	t.Skip("OWNER: Plan 06 — implements BuildGeminiSTTDirector; unskip + assert capturedReq.Header.Get(\"Authorization\") == \"\"")
	// Expected:
	//   require.Empty(t, capturedReq.Header.Get("Authorization"))
	// Reference: PATTERNS.md line 467-472 (header swap pattern).
}

// TestBuildGeminiSTTDirector_MultipartToJSON_AudioBytesPreserved — D-B4 / Pattern 2.
// Director MUST translate OpenAI multipart (file part) → Gemini inline JSON
// (`contents[0].parts[].inline_data.data` base64). Round-trip MUST yield
// the EXACT original audio bytes.
func TestBuildGeminiSTTDirector_MultipartToJSON_AudioBytesPreserved(t *testing.T) {
	t.Skip("OWNER: Plan 06 — implements multipart→JSON adapter; unskip + decode base64 from forwarded JSON body, assert byte-identical to original WAV")
	// Expected:
	//   decoded, _ := base64.StdEncoding.DecodeString(payload.Contents[0].Parts[0].InlineData.Data)
	//   require.Equal(t, originalWAVBytes, decoded)
	// Reference: PATTERNS.md line 86-93 (multipart parsing), RESEARCH §Pattern 2.
}

// TestBuildGeminiSTTDirector_ResolvesModelViaEnvOverride — D-B7.
// Forwarded URL path MUST contain the model resolved via
// `UPSTREAM_STT_FALLBACK_1_MODEL` env override (default
// `gemini-2.5-flash-lite`). Resolver wiring uses
// `resolver.Resolve("whisper", "gemini-stt")` per upstreamEnvVarMap.
func TestBuildGeminiSTTDirector_ResolvesModelViaEnvOverride(t *testing.T) {
	t.Skip("OWNER: Plan 06 — wires resolver into director; unskip + assert capturedReq.URL.Path contains env-resolved model slug")
	// Expected:
	//   t.Setenv("UPSTREAM_STT_FALLBACK_1_MODEL", "gemini-2.5-flash")
	//   require.Contains(t, capturedReq.URL.Path, "gemini-2.5-flash")
	// Reference: PATTERNS.md line 112, resolver.go:56-60.
}

// TestBuildGeminiSTTDirector_FlattenResponse — D-B4.
// ModifyResponse MUST flatten Gemini envelope
// `{candidates:[{content:{parts:[{text:"..."}]}}]}` into OpenAI shape
// `{"text":"..."}` so downstream consumers see the same response shape
// as openai-whisper.
func TestBuildGeminiSTTDirector_FlattenResponse(t *testing.T) {
	t.Skip("OWNER: Plan 06 — implements ModifyResponse flatten; unskip + assert response body parses as {\"text\":\"...\"}")
	// Expected:
	//   var out struct{ Text string `json:"text"` }
	//   json.NewDecoder(resp.Body).Decode(&out)
	//   require.Equal(t, "transcribed words", out.Text)
	// Reference: PATTERNS.md line 80, RESEARCH §Pattern 2 lines 277-381.
}

// TestBuildGeminiSTTDirector_TranslatesGeminiErrorEnvelope — D-B4.
// When Gemini returns its native error envelope
// (`{error:{code,message,status}}`), director MUST translate to OpenAI
// envelope `{error:{message,type,code}}` w/ HTTP 502 so caller-side
// retry logic stays uniform across STT upstreams.
func TestBuildGeminiSTTDirector_TranslatesGeminiErrorEnvelope(t *testing.T) {
	t.Skip("OWNER: Plan 06 — implements Gemini→OpenAI error translation; unskip + assert response status 502 + OpenAI error envelope shape")
	// Expected:
	//   require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	//   require.Contains(t, body, `"type":"upstream_error"`)
	// Reference: RESEARCH §Pattern 2, PATTERNS.md Shared §Breaker IsSuccessful.
}
