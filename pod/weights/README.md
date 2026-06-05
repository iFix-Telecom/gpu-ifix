# Weights — MinIO Mirror

Os weights do pod (~19 GB pós Phase 11.1) NÃO vivem na imagem Docker (D-01). Eles são baixados na primeira boot de cada pod Vast.ai a partir de um bucket MinIO dedicado (D-02).

**Phase 11.1 (2026-06-04):** STT model removido (~3 GB economia + ~5 GB cold-start delta com cache). STT roda off-pod via tier-1 OpenAI (gateway fallback). Ver SEED-010 + phase 11.1 docs.

## Layout no bucket

Bucket: `ifix-ai-weights` (padrão — configurável via `MINIO_BUCKET`).

```
qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf         (~17 GB)
qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf.sha256  (sidecar — opcional)
bge-m3/v1.0.0/model.tar.gz                   (~2 GB)
bge-m3/v1.0.0/model.tar.gz.sha256
```

## Versionamento (D-06)

O segmento `v1.0.0` permite rollback de weights sem alterar a imagem Docker:

- Upload de nova versão: `./pod/scripts/upload-weights.sh --weights-version v1.1.0`
- Atualizar smoke.yml inputs (ou GH Secrets) para apontar para `v1.1.0`
- Se regressão detectada: apontar de volta para `v1.0.0` — sem re-build de imagem

## Checksums (D-05)

SHA-256 de cada weight é verificado por `pod/scripts/download-weights.sh` (chamado pelo `pod/onstart.sh` a cada boot de pod).

Os valores vivem em dois lugares:
1. **GitHub Secrets** (`WEIGHTS_QWEN_SHA256`, `WEIGHTS_BGE_M3_SHA256`) — fonte de verdade para smoke.yml
2. **Sidecar `*.sha256`** no bucket — operator-side backup para conferência manual

**Drift:** Se alguém subir um arquivo com o mesmo key path mas conteúdo diferente, o onstart.sh aborta com exit 3 (`download-weights.sh:3`). O pod nunca entra em rotação com weights tampered.

## Upload inicial

**Requisitos operador:**
- 25 GB de disco livre (download + tar + staging antes do upload)
- Conexão com upstream rápida (HF → sua máquina: tipicamente 5-20 Mbps; MinIO → você: ≥90 Mbps requerido por D-02)
- `mc`, `jq`, `curl`, `sha256sum`, `tar` instalados
- Credenciais MinIO com `s3:PutObject` no bucket

**Procedimento:**

```bash
cd /home/pedro/projetos/pedro/gpu-ifix

export MINIO_ENDPOINT=https://minio.ifix.example.com
export MINIO_ACCESS_KEY=xxx
export MINIO_SECRET_KEY=xxx
export MINIO_BUCKET=ifix-ai-weights
# HF_TOKEN é opcional — só se o seu IP estiver sendo rate-limited pelo HF
# export HF_TOKEN=hf_xxx

./pod/scripts/upload-weights.sh --weights-version v1.0.0

# Ao final, copie os 3 WEIGHTS_*_SHA256 impressos para GitHub Secrets.
```

Tempo estimado: 30-60 min na primeira execução (depende de HF → você e você → MinIO). Subsequentes são quase instantâneos se o workdir persistir (re-aproveita downloads em cache).

## Rotação de weights

Cenários:
1. **Patch de tokenizer / template upstream** — Unsloth re-publica GGUF → rodar upload-weights.sh com `--weights-version v1.0.1`, atualizar Secrets, rodar smoke.yml
2. **Downgrade** — problema detectado em produção → manter versão antiga no bucket; apontar smoke.yml de volta para ela
3. **Rotação de credenciais** — trocar MINIO_ACCESS_KEY nos Secrets; weights não precisam re-uploadar

## Troubleshooting

| Sintoma | Causa provável | Ação |
|---|---|---|
| `onstart.sh` exit 3 | SHA-256 mismatch no download | Recompute local; atualize `WEIGHTS_*_SHA256` em GH Secrets; re-rode smoke.yml |
| `mc cp` lento (<20 Mbps) | MinIO VPS saturada OU firewall QoS | Medir throughput direto via `mc cp` isolado; se consistentemente baixo, revisar D-02 SLA com time de infra |
| `mc cp` 403 | Bucket policy ou credenciais inválidas | Testar `mc ls ifix/${MINIO_BUCKET}`; confirmar service account tem read+write |
| `gguf` corrompido (llama-server falha a carregar) | Download interrompido sem detecção | SHA-256 deve ter capturado — se não, investigar mc retry policy |
