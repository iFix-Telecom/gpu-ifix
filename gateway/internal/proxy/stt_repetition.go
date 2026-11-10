// Package proxy (stt_repetition.go): detector de transcrição em laço.
//
// Quick 261110 (ClickUp 17u65kmj918): o gemini-stt às vezes "trava" num
// padrão e devolve 200 com o texto em laço ("é, é, é, é…", "o o o o…").
// Em 09/11 o n8n de gravações pegou 10 casos assim e retranscreveu chamando a
// OpenAI direto, fora do gateway (custo fora do painel). O detector abaixo é
// a mesma regra do nó "Transcrição" daquele fluxo, portada; no gateway ele
// vale para todos os clientes: a resposta em laço vira fallthrough para o
// próximo upstream STT (groq-whisper → openai-whisper), sem penalidade no
// breaker — laço é problema do áudio/modelo naquela requisição, não queda.
package proxy

import (
	"strings"
)

// sttRepetitivePunct é o conjunto removido pelo regex do n8n:
// /[.,!?;:'"()\-\*\[\]#]/g
const sttRepetitivePunct = ".,!?;:'\"()-*[]#"

// isRepetitiveTranscript reporta se o texto tem padrão de repetição
// degenerada. Textos com menos de 20 palavras nunca são marcados.
func isRepetitiveTranscript(text string) bool {
	clean := strings.Map(func(r rune) rune {
		if strings.ContainsRune(sttRepetitivePunct, r) {
			return -1
		}
		return r
	}, strings.ToLower(text))
	w := strings.Fields(clean)
	total := len(w)
	if total < 20 {
		return false
	}

	// 1. Uma palavra é mais da metade do texto.
	freq := make(map[string]int, total)
	for _, p := range w {
		freq[p]++
	}
	for _, c := range freq {
		if float64(c)/float64(total) > 0.5 {
			return true
		}
	}

	// 2. Mesma palavra 10 vezes seguidas.
	run := 1
	for i := 1; i < total; i++ {
		if w[i] == w[i-1] {
			run++
			if run >= 10 {
				return true
			}
		} else {
			run = 1
		}
	}

	// 3. N-grama (2..5) alinhado domina o texto (>70%).
	for n := 2; n <= 5; n++ {
		if total < n*10 {
			continue
		}
		for off := 0; off < n; off++ {
			cnt := map[string]int{}
			tot := 0
			for i := off; i <= total-n; i += n {
				cnt[strings.Join(w[i:i+n], " ")]++
				tot++
			}
			if tot < 10 {
				continue
			}
			for _, c := range cnt {
				if float64(c)/float64(tot) > 0.7 {
					return true
				}
			}
		}
	}

	// 4. Janela de até 100 palavras (passo de meia janela) onde um n-grama
	// (1..4) deslizante passa de 60%.
	win := total
	if win > 100 {
		win = 100
	}
	step := win / 2
	for n := 1; n <= 4; n++ {
		for start := 0; start <= total-win; start += step {
			j := w[start : start+win]
			tot := len(j) - n + 1
			if tot < 20 {
				continue
			}
			cnt := map[string]int{}
			for i := 0; i <= len(j)-n; i++ {
				cnt[strings.Join(j[i:i+n], " ")]++
			}
			for _, c := range cnt {
				if float64(c)/float64(tot) > 0.6 {
					return true
				}
			}
		}
	}
	return false
}

// sttRepetitiveTranscriptError é o sentinel de fallthrough para resposta 200
// em laço. errors.Is casa com errUpstreamRetryable (ErrorHandler suprime a
// escrita e o dispatcher segue a cascata); SkipBreaker evita penalizar o
// upstream.
type sttRepetitiveTranscriptError struct{}

func (sttRepetitiveTranscriptError) Error() string {
	return "proxy: stt transcript is repetitive (loop), fall through without breaker penalty"
}

func (sttRepetitiveTranscriptError) Is(target error) bool { return target == errUpstreamRetryable }

// SkipBreaker implements breakerSkipper (dispatcher.go).
func (sttRepetitiveTranscriptError) SkipBreaker() bool { return true }
