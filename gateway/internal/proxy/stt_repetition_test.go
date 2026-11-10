package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Quick 261110: amostras reais (n8n, 09/11) + falsos positivos óbvios.
func TestIsRepetitiveTranscript(t *testing.T) {
	loopE := "a gente, a gente, certo, é, é de, " + strings.Repeat("é, ", 120)
	loopO := "Olá, Rodrigo. Sim. Opa, Rodrigo, tudo bem? Sou eu aqui da equipe do Marcelo Duarte. Alô? Alô? Opa, tá me ouvindo bem? O " + strings.Repeat("o ", 150)
	loopBigram := "bom dia " + strings.Repeat("muito obrigado ", 40)
	normal := "Olá, se você me diga seu nome e o motivo da ligação, poderei ver se essa pessoa está disponível. " +
		"Permaneça na linha. Oi. Boa tarde, Laís, tudo bem? Boa tarde. Laís, eu acho que eu já conversei com você " +
		"tantas vezes na minha vida que eu tô até com vergonha de ligar de novo, mas é sobre aquela proposta do " +
		"plano de telefonia que a gente mandou semana passada, você conseguiu olhar com o pessoal do financeiro?"
	alo := "Alô? Alô? Alô? Alô? Oi, tudo bem? Pode falar sim, estou te ouvindo agora, a ligação estava ruim mas melhorou bastante."
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"vazio", "", false},
		{"curto repetido (<20 palavras)", strings.Repeat("alô ", 15), false},
		{"laço é", loopE, true},
		{"laço o", loopO, true},
		{"laço bigrama", loopBigram, true},
		{"conversa normal", normal, false},
		{"alô repetido no começo", alo, false},
	}
	for _, c := range cases {
		if got := isRepetitiveTranscript(c.text); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestGeminiSTT_RepetitiveTranscriptFallsThroughWithoutBreaker(t *testing.T) {
	gURL, _ := url.Parse("https://gemini.example/v1")
	_, modifyResponse := BuildGeminiSTTDirector(gURL, "k", newDirectorTestResolver(nil),
		"gemini-stt", slog.New(slog.NewTextHandler(io.Discard, nil)))

	body, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"parts": []any{map[string]any{"text": "certo, " + strings.Repeat("é, ", 80)}}},
	}}})
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(string(body)))}
	err := modifyResponse(resp)
	if !errors.Is(err, errUpstreamRetryable) {
		t.Fatalf("err=%v; want errUpstreamRetryable", err)
	}
	if countsAsBreakerFailure(err) {
		t.Fatal("laço não pode contar como falha no breaker")
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d; want 502 (consumidor standalone)", resp.StatusCode)
	}

	// Texto normal segue intacto.
	ok, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"parts": []any{map[string]any{"text": "Oi, boa tarde, tudo bem?"}}},
	}}})
	resp2 := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(string(ok)))}
	if err := modifyResponse(resp2); err != nil {
		t.Fatalf("texto normal: err=%v", err)
	}
	out, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(out), "boa tarde") {
		t.Fatalf("texto normal alterado: %s", out)
	}
}
