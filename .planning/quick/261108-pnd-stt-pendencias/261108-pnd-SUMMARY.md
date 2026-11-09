---
quick_id: 261108-pnd
status: complete
---

# 261108-pnd — SUMMARY

- e088917 `fix(gateway): sucesso em CLOSED zera falhas do breaker tier-1` (17u65kmj82g)
- 50f0388 `fix(gateway): duração real de mp3 e ogg/opus no billing STT` (17u65kmj82f)
- Testes novos: `closed_success_test.go`, `audio_duration_compressed_test.go` + fixtures `testdata/audio/`.
- Achado no caminho: primeira versão do parser aceitava "frame" perto do EOF sem sucessor (pegou no
  teste antigo de 128 kbps sintético) — corrigido: sem sucessor só se terminar exatamente no EOF.
- Efeito esperado no billing: `audio_seconds` de STT sobe ~16× para gravações voip (custo externo de
  Gemini/Groq/OpenAI passa a refletir o real). Painel de consumo vai mostrar salto a partir do deploy.
