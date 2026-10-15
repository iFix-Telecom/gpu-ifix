// Package proxy (chat_retryable_status.go): cascata por status HTTP para
// upstreams LLM tier-1 NÃO-finais (Phase 23-04, HAIKU-03).
//
// Antes desta peça só falha de conexão (errDialFailedFallthrough) e
// over-context cascateavam em chat: um 429/504 "rate-limited upstream" do
// provedor chegava cru ao cliente mesmo havendo próximo candidato saudável
// (Pitfall 7 / F6 — 504 chegou ao chat-ifix). Este interceptor transforma os
// status transitórios do provedor no sentinel errUpstreamRetryable, e o
// ErrorHandler sentinel-aware + dispatcher fazem o resto (suprimir escrita,
// contar breaker, avançar ao próximo candidato).
package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// chatErrorPeekMax limita quanto do corpo de erro do upstream é lido para ser
// devolvido ao cliente caso a cadeia inteira falhe (T-23-11). Envelopes de
// erro têm centenas de bytes; um upstream hostil não consegue fazer o gateway
// bufferizar megabytes.
const chatErrorPeekMax = 8 << 10 // 8 KiB

// chatRetryableStatuses são os status que indicam indisponibilidade
// transitória/quota do PROVEDOR (não do request) e portanto justificam tentar
// o próximo candidato:
//
//	402 créditos esgotados · 429 rate limit · 500/502/503/504 falha do provedor
//	524 timeout de origem (Cloudflare/OpenRouter) · 529 overloaded (Anthropic)
//
// 400 (e demais 4xx de validação) fica FORA de propósito (T-23-14): um request
// inválido seria inválido nos três saltos — cascatear só triplicaria custo e
// latência para o mesmo 400.
var chatRetryableStatuses = map[int]bool{
	402: true,
	429: true,
	500: true,
	502: true,
	503: true,
	504: true,
	524: true,
	529: true,
}

// chatRetryableStatusInterceptor implementa ChatRetryableStatusInterceptor.
type chatRetryableStatusInterceptor struct{}

// ChatRetryableStatusInterceptor devolve o interceptor de cascata por status
// para upstreams de chat tier-1 NÃO-finais. Deve ser composto PRIMEIRO na
// ComposeInterceptors (antes de billing/audit), para que o status retryable
// vire fallthrough antes de qualquer contabilização (wiring no 23-14).
//
// O candidato FINAL da cadeia não deve compô-lo: sem próximo salto, o sentinel
// só trocaria o erro real do upstream pelo envelope genérico de exaustão. (Com
// o lastStatus/lastBody deste plano o dano seria menor, mas o caminho direto
// continua sendo o mais fiel.)
//
// Diferença intencional em relação ao chatOverContextInterceptor: NÃO consulta
// streamingFlagFrom. Status != 2xx chega antes de qualquer byte de SSE (o
// provedor só abre o stream com 200), então o fallthrough é pré-byte também com
// stream:true — D-07 continua respeitado (Padrão 3 do research).
//
// Contrato de forma do corpo: o lastBody é repassado como veio do upstream.
// Upstreams que não falam OpenAI nativamente devem traduzir o erro para o
// envelope OpenAI ANTES deste interceptor (para o ①, o 23-12 compõe a
// tradução antes dele). Este interceptor não normaliza.
func ChatRetryableStatusInterceptor() ProxyResponseInterceptor {
	return chatRetryableStatusInterceptor{}
}

// Intercept aplica os gates do mais barato ao mais caro e só toca o corpo
// quando a resposta vai de fato cascatear.
func (chatRetryableStatusInterceptor) Intercept(resp *http.Response) error {
	if resp == nil || resp.Request == nil {
		return nil
	}
	if !chatRetryableStatuses[resp.StatusCode] {
		return nil
	}
	// Ninguém re-roteia (proxy standalone / caminho sem dispatchTo): o
	// ErrorHandler transformaria o sentinel em 502 — pior que o erro honesto.
	if dispatchResultFrom(resp.Request.Context()) == nil {
		return nil
	}
	var peek []byte
	if resp.Body != nil {
		var err error
		peek, err = io.ReadAll(io.LimitReader(resp.Body, chatErrorPeekMax))
		orig := resp.Body
		resp.Body = restoredBody{Reader: io.MultiReader(bytes.NewReader(peek), orig), orig: orig}
		if err != nil {
			// Corpo ilegível: ainda cascateia (o status já basta), sem corpo.
			peek = nil
		}
	}
	return &chatUpstreamStatusError{
		status:      resp.StatusCode,
		body:        peek,
		contentType: resp.Header.Get("Content-Type"),
	}
}

// chatUpstreamStatusError é o erro de fallthrough emitido para um status
// retryable de chat. errors.Is casa com errUpstreamRetryable (cascata +
// supressão de escrita no ErrorHandler) e o ErrorHandler copia
// status/corpo/content-type para o dispatchResult, de onde o dispatcher
// devolve o ÚLTIMO erro real quando a cadeia se esgota.
type chatUpstreamStatusError struct {
	status      int
	body        []byte
	contentType string
}

func (e *chatUpstreamStatusError) Error() string {
	return fmt.Sprintf("%s (chat upstream status %d)", errUpstreamRetryable.Error(), e.status)
}

// Is faz errors.Is(err, errUpstreamRetryable) == true.
func (e *chatUpstreamStatusError) Is(target error) bool { return target == errUpstreamRetryable }

// Status é o status HTTP devolvido pelo upstream.
func (e *chatUpstreamStatusError) Status() int { return e.status }

// Body é o prefixo (≤ chatErrorPeekMax) do corpo de erro do upstream.
func (e *chatUpstreamStatusError) Body() []byte { return e.body }

// ContentType é o Content-Type da resposta de erro do upstream.
func (e *chatUpstreamStatusError) ContentType() string { return e.contentType }
