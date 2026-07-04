---
quick_id: 260704-rkc
slug: stt-gemini-mime-sniff
type: quick
date: 2026-07-04
autonomous: true
files_modified:
  - gateway/internal/proxy/gemini_stt_director.go
  - gateway/internal/proxy/gemini_stt_director_test.go
---

<objective>
Corrigir o STT do gateway quando o cliente envia áudio SEM Content-Type de áudio válido (chega como `application/octet-stream`). Hoje `extractAudioFromMultipart` repassa esse MIME verbatim ao Gemini, que responde HTTP 502 `INVALID_ARGUMENT: Unsupported MIME type: application/octet-stream` → transcrição quebra (ex.: Maestro/WhatsApp). O Gemini transcreve opus/wav/mp3 normalmente quando um `audio/*` correto é enviado — então sniffar o tipo real pelos magic bytes resolve por completo (sem transcode, sem cascade).
</objective>

<root_cause>
`gateway/internal/proxy/gemini_stt_director.go`, função `extractAudioFromMultipart` (~linha 302-306):
```go
mimeType := part.Header.Get("Content-Type")
if mimeType == "" {          // só cobre AUSENTE
    mimeType = "audio/wav"
}
return audio, mimeType, nil  // octet-stream/genérico passa direto → gemini 502
```
Reproduzido ao vivo: opus/ogg com `type=audio/ogg` → 200; mesmo arquivo sem type (octet-stream) → 502.
</root_cause>

<tasks>

<task type="auto">
  <name>Task 1: Sniff de MIME de áudio pelos magic bytes</name>
  <files>gateway/internal/proxy/gemini_stt_director.go</files>
  <action>
    Adicionar helper `sniffAudioMIME(audio []byte) string` que detecta o tipo pelos magic bytes:
    - `OggS` (0x4F 67 67 53) → `audio/ogg`
    - `RIFF`....`WAVE` (bytes 0-3 = "RIFF", bytes 8-11 = "WAVE") → `audio/wav`
    - `ID3` (0x49 44 33) OU frame-sync MPEG (0xFF seguido de 0xE0-mask: 0xFB/0xF3/0xF2/0xFA/0xF9…) → `audio/mpeg`
    - `fLaC` (0x66 4C 61 43) → `audio/flac`
    - offset 4-7 = `ftyp` (M4A/mp4/isom) → `audio/mp4`
    - EBML 0x1A 45 DF A3 → `audio/webm`
    - senão → `audio/wav` (fallback seguro)
    Guardar contra buffers curtos (checar len antes de indexar).

    Em `extractAudioFromMultipart`, substituir o bloco de MIME por: confiar no declarado APENAS se começar com `audio/`; caso contrário (vazio, `application/octet-stream`, `application/*`, ou qualquer não-`audio/`), usar `sniffAudioMIME(audio)`.
    ```go
    declared := part.Header.Get("Content-Type")
    var mimeType string
    if strings.HasPrefix(declared, "audio/") {
        mimeType = declared
    } else {
        mimeType = sniffAudioMIME(audio)
    }
    return audio, mimeType, nil
    ```
    `strings` já está importado no arquivo.
  </action>
  <verify>
    <automated>cd gateway && gofmt -l internal/proxy/gemini_stt_director.go (vazio) && go build ./...</automated>
  </verify>
  <done>Helper de sniff adicionado; extractAudioFromMultipart usa sniff quando o MIME declarado não é audio/*; confia em audio/* concreto.</done>
</task>

<task type="auto">
  <name>Task 2: Teste unitário do sniff + extractAudioFromMultipart</name>
  <files>gateway/internal/proxy/gemini_stt_director_test.go</files>
  <action>
    Adicionar testes (estender o arquivo existente, não quebrar os testes atuais):
    - `sniffAudioMIME`: OggS→audio/ogg; RIFF+WAVE→audio/wav; ID3 e 0xFFFB→audio/mpeg; fLaC→audio/flac; ftyp→audio/mp4; bytes desconhecidos→audio/wav; slice vazio/curto→audio/wav (sem panic).
    - `extractAudioFromMultipart`: montar multipart com part "file" e:
      (a) Content-Type ausente + bytes OggS → retorna audio/ogg (antes retornava audio/wav);
      (b) Content-Type `application/octet-stream` + bytes RIFF/WAVE → retorna audio/wav (não octet-stream);
      (c) Content-Type `audio/mpeg` concreto → passthrough audio/mpeg (confia no declarado).
    Usar `mime/multipart` p/ montar o body, igual aos testes existentes.
  </action>
  <verify>
    <automated>cd gateway && go test ./internal/proxy/ -run 'Gemini|SniffAudio|ExtractAudio|MultipartMime' -count=1 (PASS) && go test ./internal/proxy/ -count=1 (PASS — nenhum teste existente quebrado)</automated>
  </verify>
  <done>Testes cobrindo octet-stream→sniff, ausente→sniff, audio/* concreto→passthrough, todos verdes; suíte proxy sem regressão.</done>
</task>

</tasks>

<verification>
- `go build ./...` verde no módulo gateway.
- `go test ./internal/proxy/ -count=1` verde (novos testes + zero regressão).
- `gofmt -l` vazio nos 2 arquivos.
</verification>

<success_criteria>
STT do gateway envia MIME de áudio correto ao Gemini para uploads sem Content-Type (octet-stream) ou genérico, detectando o formato real pelos magic bytes; áudio audio/* declarado corretamente segue intacto. Cobertura por teste unitário. SEM build/push/deploy (passo de ops separado).
</success_criteria>

<output>
Criar `.planning/quick/260704-rkc-stt-gemini-mime-sniff/260704-rkc-SUMMARY.md` ao concluir, com o diff-resumo e a saída dos testes.
</output>
