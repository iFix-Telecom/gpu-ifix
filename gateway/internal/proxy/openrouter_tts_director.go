// Package proxy (openrouter_tts_director.go): Director do upstream TTS externo
// openrouter-tts — Phase 23 Plan 07 (HAIKU-08).
//
// O OpenRouter expõe POST /api/v1/audio/speech no formato OpenAI. O upstream é
// configurado com URL base terminando em /api (ex.: https://openrouter.ai/api);
// BuildDirector junta o path de entrada /v1/audio/speech →
// /api/v1/audio/speech.
//
// Reescritas do body (map[string]json.RawMessage — demais campos como input,
// speed e instructions passam byte a byte):
//
//   - model: resolver.Resolve(alias, upstreamName) quando há linha em
//     model_aliases para (alias, upstreamName); senão defaultModel.
//   - voice: voiceMap[voz do cliente] quando mapeada; senão, se defaultVoice
//     != "", defaultVoice (cobre voz ausente/vazia e vozes de outros engines,
//     ex. pm_alex do Kokoro); senão a voz do cliente segue intacta.
//   - response_format: "mp3" só quando ausente/vazio (Pitfall 13 — o
//     OpenRouter devolve PCM por padrão). Valor explícito do cliente é
//     respeitado.
//
// Cascata por status/timeout NÃO é responsabilidade deste Director — o 23-14
// compõe ChatRetryableStatusInterceptor no ModifyResponse do proxy TTS.
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// BuildOpenRouterTTSDirector retorna o Director do upstream openrouter-tts.
// Envolve BuildDirector (remove Authorization/X-API-Key do cliente, junta
// path, X-Request-ID do gateway) e injeta `Authorization: Bearer
// <authBearer>`. log é usado só para R12 (upstream + error_class); o body
// nunca é logado (texto do tenant).
func BuildOpenRouterTTSDirector(
	upstream *url.URL,
	authBearer string,
	resolver *models.Resolver,
	upstreamName string,
	defaultModel string,
	defaultVoice string,
	voiceMap map[string]string,
	log *slog.Logger,
) func(*http.Request) {
	base := BuildDirector(upstream)
	return func(r *http.Request) {
		base(r)
		if authBearer != "" {
			r.Header.Set("Authorization", "Bearer "+authBearer)
		}
		if r.Body == nil {
			return
		}
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil || len(body) == 0 {
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			return
		}
		patched, perr := openrouterTTSRewrite(body, resolver, upstreamName, defaultModel, defaultVoice, voiceMap)
		if perr != nil {
			if log != nil {
				log.Debug("openrouter_tts_director rewrite skipped",
					"upstream", upstreamName,
					"reason", "parse_failed",
					"error_class", fmt.Sprintf("%T", perr),
				)
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(patched))
		r.ContentLength = int64(len(patched))
		r.Header.Set("Content-Length", strconv.Itoa(len(patched)))
	}
}

// openrouterTTSRewrite aplica as regras de model/voice/response_format.
func openrouterTTSRewrite(
	body []byte,
	resolver *models.Resolver,
	upstreamName, defaultModel, defaultVoice string,
	voiceMap map[string]string,
) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}

	// model — Resolver.Resolve devolve o próprio alias (passthrough) quando
	// não há linha para (alias, upstreamName); nesse caso usamos defaultModel.
	var alias string
	if raw, ok := m["model"]; ok {
		_ = json.Unmarshal(raw, &alias)
	}
	target := ""
	if resolver != nil {
		if t := resolver.Resolve(alias, upstreamName); t != "" && t != alias {
			target = t
		}
	}
	if target == "" {
		if defaultModel != "" {
			target = defaultModel
		} else {
			target = alias
		}
	}
	if target != "" {
		b, err := json.Marshal(target)
		if err != nil {
			return nil, err
		}
		m["model"] = b
	}

	// voice
	var voice string
	if raw, ok := m["voice"]; ok {
		_ = json.Unmarshal(raw, &voice)
	}
	newVoice := ""
	if mapped, ok := voiceMap[voice]; ok && voice != "" && mapped != "" {
		newVoice = mapped
	} else if defaultVoice != "" {
		newVoice = defaultVoice
	}
	if newVoice != "" {
		b, err := json.Marshal(newVoice)
		if err != nil {
			return nil, err
		}
		m["voice"] = b
	}

	// response_format — força mp3 só quando ausente/vazio (Pitfall 13).
	var format string
	if raw, ok := m["response_format"]; ok {
		_ = json.Unmarshal(raw, &format)
	}
	if strings.TrimSpace(format) == "" {
		m["response_format"] = json.RawMessage(`"mp3"`)
	}

	return json.Marshal(m)
}

// ParseTTSVoiceMap interpreta `pm_alex=<voz>,alloy=<voz>` (espaços em volta
// de chaves/valores são removidos). Entradas sem "=" ou com chave/valor vazio
// são ignoradas. Nunca devolve nil.
func ParseTTSVoiceMap(raw string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	return out
}
