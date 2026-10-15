package proxy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// Phase 23 Plan 07 (HAIKU-08) — Director do upstream TTS externo
// openrouter-tts (OpenRouter POST /api/v1/audio/speech, formato OpenAI).

const ttsTestModel = "elevenlabs/eleven-multilingual-v2"

func ttsTestResolver() *models.Resolver {
	return models.NewResolverForTesting(map[[2]string]string{
		{"tts-1", "openrouter-tts"}: ttsTestModel,
	})
}

func decodeTTSBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("body não é JSON válido: %v (%s)", err, b)
	}
	return m
}

func TestOpenRouterTTSDirector_RewritesModelVoiceAndForcesMP3(t *testing.T) {
	upstream, _ := url.Parse("https://openrouter.ai/api")
	d := BuildOpenRouterTTSDirector(upstream, "sk-or-test", ttsTestResolver(), "openrouter-tts",
		"default/model", "", map[string]string{"pm_alex": "voz-pt"}, discardLogger())

	body := []byte(`{"model":"tts-1","input":"Olá","voice":"pm_alex"}`)
	_, out := applyDirector(t, d, http.MethodPost, "/v1/audio/speech", "application/json", body, nil, nil)
	m := decodeTTSBody(t, out)
	if m["model"] != ttsTestModel {
		t.Errorf("model = %v, want %s", m["model"], ttsTestModel)
	}
	if m["voice"] != "voz-pt" {
		t.Errorf("voice = %v, want voz-pt", m["voice"])
	}
	if m["response_format"] != "mp3" {
		t.Errorf("response_format = %v, want mp3", m["response_format"])
	}
	if m["input"] != "Olá" {
		t.Errorf("input = %v, want Olá", m["input"])
	}
}

// O OpenRouter só aceita mp3|pcm (400 ZodError em prod, 2026-10-15, para o
// "wav" do claude-wpp). Formato fora desse conjunto vira mp3; a resposta sai
// com Content-Type audio/mpeg e o cliente vê o formato real pelo cabeçalho.
func TestOpenRouterTTSDirector_UnsupportedResponseFormatBecomesMP3(t *testing.T) {
	for _, f := range []string{"wav", "opus", "aac", "flac", "WAV"} {
		upstream, _ := url.Parse("https://openrouter.ai/api")
		d := BuildOpenRouterTTSDirector(upstream, "sk", ttsTestResolver(), "openrouter-tts", "", "", nil, discardLogger())
		body := []byte(`{"model":"tts-1","input":"x","voice":"pm_alex","response_format":"` + f + `"}`)
		_, out := applyDirector(t, d, http.MethodPost, "/v1/audio/speech", "application/json", body, nil, nil)
		if got := decodeTTSBody(t, out)["response_format"]; got != "mp3" {
			t.Errorf("response_format %q → %v, want mp3", f, got)
		}
	}
}

func TestOpenRouterTTSDirector_KeepsSupportedResponseFormat(t *testing.T) {
	for _, f := range []string{"mp3", "pcm"} {
		upstream, _ := url.Parse("https://openrouter.ai/api")
		d := BuildOpenRouterTTSDirector(upstream, "sk", ttsTestResolver(), "openrouter-tts", "", "", nil, discardLogger())
		body := []byte(`{"model":"tts-1","input":"x","response_format":"` + f + `"}`)
		_, out := applyDirector(t, d, http.MethodPost, "/v1/audio/speech", "application/json", body, nil, nil)
		if got := decodeTTSBody(t, out)["response_format"]; got != f {
			t.Errorf("response_format = %v, want %s", got, f)
		}
	}
}

func TestOpenRouterTTSDirector_EmptyResponseFormatBecomesMP3(t *testing.T) {
	upstream, _ := url.Parse("https://openrouter.ai/api")
	d := BuildOpenRouterTTSDirector(upstream, "sk", ttsTestResolver(), "openrouter-tts", "", "", nil, discardLogger())
	body := []byte(`{"model":"tts-1","input":"x","response_format":""}`)
	_, out := applyDirector(t, d, http.MethodPost, "/v1/audio/speech", "application/json", body, nil, nil)
	if got := decodeTTSBody(t, out)["response_format"]; got != "mp3" {
		t.Errorf("response_format = %v, want mp3", got)
	}
}

func TestOpenRouterTTSDirector_NoResolverRowUsesDefaultModel(t *testing.T) {
	upstream, _ := url.Parse("https://openrouter.ai/api")
	d := BuildOpenRouterTTSDirector(upstream, "sk", models.NewResolverForTesting(nil), "openrouter-tts",
		"default/tts-model", "", nil, discardLogger())
	body := []byte(`{"model":"tts-1","input":"x"}`)
	_, out := applyDirector(t, d, http.MethodPost, "/v1/audio/speech", "application/json", body, nil, nil)
	if got := decodeTTSBody(t, out)["model"]; got != "default/tts-model" {
		t.Errorf("model = %v, want default/tts-model", got)
	}
}

func TestOpenRouterTTSDirector_UnmappedVoiceUsesDefaultVoice(t *testing.T) {
	upstream, _ := url.Parse("https://openrouter.ai/api")
	d := BuildOpenRouterTTSDirector(upstream, "sk", ttsTestResolver(), "openrouter-tts", "", "voz-pt",
		map[string]string{"pm_alex": "outra"}, discardLogger())
	body := []byte(`{"model":"tts-1","input":"x","voice":"alloy"}`)
	_, out := applyDirector(t, d, http.MethodPost, "/v1/audio/speech", "application/json", body, nil, nil)
	if got := decodeTTSBody(t, out)["voice"]; got != "voz-pt" {
		t.Errorf("voice = %v, want voz-pt", got)
	}

	// Voz ausente também cai na default.
	_, out = applyDirector(t, d, http.MethodPost, "/v1/audio/speech", "application/json",
		[]byte(`{"model":"tts-1","input":"x"}`), nil, nil)
	if got := decodeTTSBody(t, out)["voice"]; got != "voz-pt" {
		t.Errorf("voice ausente = %v, want voz-pt", got)
	}
}

func TestOpenRouterTTSDirector_UnmappedVoiceWithoutDefaultKept(t *testing.T) {
	upstream, _ := url.Parse("https://openrouter.ai/api")
	d := BuildOpenRouterTTSDirector(upstream, "sk", ttsTestResolver(), "openrouter-tts", "", "", nil, discardLogger())
	body := []byte(`{"model":"tts-1","input":"x","voice":"alloy"}`)
	_, out := applyDirector(t, d, http.MethodPost, "/v1/audio/speech", "application/json", body, nil, nil)
	if got := decodeTTSBody(t, out)["voice"]; got != "alloy" {
		t.Errorf("voice = %v, want alloy", got)
	}
}

func TestOpenRouterTTSDirector_PreservesOtherFields(t *testing.T) {
	upstream, _ := url.Parse("https://openrouter.ai/api")
	d := BuildOpenRouterTTSDirector(upstream, "sk", ttsTestResolver(), "openrouter-tts", "", "", nil, discardLogger())
	body := []byte(`{"model":"tts-1","input":"x","speed":1.25,"instructions":"fale devagar"}`)
	_, out := applyDirector(t, d, http.MethodPost, "/v1/audio/speech", "application/json", body, nil, nil)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["speed"]) != "1.25" {
		t.Errorf("speed = %s, want 1.25 byte a byte", raw["speed"])
	}
	if string(raw["instructions"]) != `"fale devagar"` {
		t.Errorf("instructions = %s", raw["instructions"])
	}
}

func TestOpenRouterTTSDirector_AuthHeaders(t *testing.T) {
	upstream, _ := url.Parse("https://openrouter.ai/api")
	d := BuildOpenRouterTTSDirector(upstream, "sk-or-xyz", ttsTestResolver(), "openrouter-tts", "", "", nil, discardLogger())
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer ifix_sk_x")
	hdr.Set("X-API-Key", "ifix_sk_x")
	req, _ := applyDirector(t, d, http.MethodPost, "/v1/audio/speech", "application/json",
		[]byte(`{"model":"tts-1","input":"x"}`), hdr, nil)
	if got := req.Header.Get("Authorization"); got != "Bearer sk-or-xyz" {
		t.Errorf("Authorization = %q, want Bearer sk-or-xyz", got)
	}
	if got := req.Header.Get("X-API-Key"); got != "" {
		t.Errorf("X-API-Key vazou: %q", got)
	}
}

func TestOpenRouterTTSDirector_PathJoin(t *testing.T) {
	upstream, _ := url.Parse("https://openrouter.ai/api")
	d := BuildOpenRouterTTSDirector(upstream, "sk", ttsTestResolver(), "openrouter-tts", "", "", nil, discardLogger())
	req, _ := applyDirector(t, d, http.MethodPost, "/v1/audio/speech", "application/json",
		[]byte(`{"model":"tts-1","input":"x"}`), nil, nil)
	if got := req.URL.String(); got != "https://openrouter.ai/api/v1/audio/speech" {
		t.Errorf("URL = %q, want https://openrouter.ai/api/v1/audio/speech", got)
	}
	if req.Host != "openrouter.ai" {
		t.Errorf("Host = %q", req.Host)
	}
}

func TestOpenRouterTTSDirector_InvalidJSONUntouched(t *testing.T) {
	upstream, _ := url.Parse("https://openrouter.ai/api")
	d := BuildOpenRouterTTSDirector(upstream, "sk", ttsTestResolver(), "openrouter-tts", "m", "v", nil, discardLogger())
	body := []byte(`{"model":"tts-1",`)
	_, out := applyDirector(t, d, http.MethodPost, "/v1/audio/speech", "application/json", body, nil, nil)
	if string(out) != string(body) {
		t.Errorf("body alterado: %q", out)
	}
}

func TestOpenRouterTTSDirector_ContentLength(t *testing.T) {
	upstream, _ := url.Parse("https://openrouter.ai/api")
	d := BuildOpenRouterTTSDirector(upstream, "sk", ttsTestResolver(), "openrouter-tts", "", "voz-pt", nil, discardLogger())
	req, out := applyDirector(t, d, http.MethodPost, "/v1/audio/speech", "application/json",
		[]byte(`{"model":"tts-1","input":"Olá"}`), nil, nil)
	if req.ContentLength != int64(len(out)) {
		t.Errorf("ContentLength = %d, want %d", req.ContentLength, len(out))
	}
	if got := req.Header.Get("Content-Length"); got != strconv.Itoa(len(out)) {
		t.Errorf("header Content-Length = %q, want %d", got, len(out))
	}
}

func TestParseTTSVoiceMap(t *testing.T) {
	got := ParseTTSVoiceMap("pm_alex=a, alloy = b,ruim")
	want := map[string]string{"pm_alex": "a", "alloy": "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseTTSVoiceMap = %v, want %v", got, want)
	}
	if m := ParseTTSVoiceMap(""); len(m) != 0 {
		t.Errorf("vazio = %v, want map vazio", m)
	}
	if m := ParseTTSVoiceMap("=x,y=,,a=b"); !reflect.DeepEqual(m, map[string]string{"a": "b"}) {
		t.Errorf("malformadas = %v, want {a:b}", m)
	}
}
