# MinIO Setup Checklist — Phase 1 Prereq

**Estimated time:** 45-90 min (dominated by weight download + upload).
**Pre-requisite for:** `/gsd-execute-phase 1` plan 08 smoke.yml run.
**Operator:** Ifix infra engineer with MinIO admin access.

Credential handling convention: operator sets `MINIO_*` env vars before running commands below. Prefer a password manager + `read -s` to typing secrets inline — shell history leaks (T-01-09-02).

---

## Checklist

- [ ] **1. MinIO endpoint is reachable over public HTTPS**
  ```bash
  curl -fsS "${MINIO_ENDPOINT:?set first}/minio/health/live"
  ```
  Expected: HTTP 200 (empty body or `{"status":"OK"}`).

- [ ] **2. Bucket `ifix-ai-weights` exists with `private` access policy**
  ```bash
  mc alias set ifix "${MINIO_ENDPOINT}" "${MINIO_ACCESS_KEY}" "${MINIO_SECRET_KEY}"
  mc ls ifix | grep ifix-ai-weights || mc mb ifix/ifix-ai-weights
  mc anonymous get ifix/ifix-ai-weights 2>&1 | grep -q "Access permission for.*is \`private\`" \
    || mc anonymous set private ifix/ifix-ai-weights
  ```
  Expected: bucket listed; anonymous access = private.

- [ ] **3. Service account has s3:PutObject + s3:GetObject + s3:ListBucket on the bucket**
  ```bash
  echo "test" | mc pipe ifix/ifix-ai-weights/__setup-probe.txt
  mc cat ifix/ifix-ai-weights/__setup-probe.txt | grep -q test
  mc rm ifix/ifix-ai-weights/__setup-probe.txt
  ```
  Expected: "test" echoed; file deleted.

- [ ] **4. Download throughput ≥90 Mbps sustained (D-02)**
  ```bash
  # Generate a 1 GB test file, upload then download, measure:
  dd if=/dev/urandom of=/tmp/test-1gb bs=1M count=1024
  time mc cp /tmp/test-1gb ifix/ifix-ai-weights/__speed-probe.bin
  time mc cp ifix/ifix-ai-weights/__speed-probe.bin /tmp/test-1gb.dl
  mc rm ifix/ifix-ai-weights/__speed-probe.bin
  rm /tmp/test-1gb /tmp/test-1gb.dl
  ```
  Expected: download time ≤90s for 1 GB = ≥90 Mbps sustained.

- [ ] **5. HF can be reached from operator machine (weights source)**
  ```bash
  curl -fsS -o /dev/null -w "%{http_code}\n" \
    "https://huggingface.co/unsloth/Qwen3.5-27B-GGUF/resolve/main/Qwen3.5-27B-Q4_K_M.gguf" \
    --range 0-1023
  ```
  Expected: HTTP 206 (partial content). If 401, set HF_TOKEN.

- [ ] **6. Run the upload script (SHA-256 per D-05, versioned keys per D-06)**
  ```bash
  cd /home/pedro/projetos/pedro/gpu-ifix
  export MINIO_ENDPOINT MINIO_ACCESS_KEY MINIO_SECRET_KEY MINIO_BUCKET=ifix-ai-weights
  ./pod/scripts/upload-weights.sh --weights-version v1.0.0
  ```
  Expected: script exits 0; prints 3 `WEIGHTS_*_SHA256` values.
  The `v1.0.0` segment satisfies D-06 (weight rollback independent of image); the printed SHA-256 values satisfy D-05 (startup aborts on mismatch at pod boot).

- [ ] **7. Confirm 3 objects present in the bucket**
  ```bash
  mc ls --recursive ifix/ifix-ai-weights/ | awk '{print $NF}' | sort
  ```
  Expected (at minimum):
  ```
  bge-m3/v1.0.0/model.tar.gz
  bge-m3/v1.0.0/model.tar.gz.sha256
  qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf
  qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf.sha256
  whisper-large-v3/v1.0.0/model.tar.gz
  whisper-large-v3/v1.0.0/model.tar.gz.sha256
  ```

- [ ] **8. Populate GitHub Secrets** (Repo → Settings → Secrets and variables → Actions)
  Paste the values the upload script printed + MinIO config:
  ```
  VAST_AI_API_KEY        = <from vast.ai/account Keys tab — Export>
  MINIO_ENDPOINT         = https://minio.ifix.example.com
  MINIO_BUCKET           = ifix-ai-weights
  MINIO_ACCESS_KEY       = <service account key>
  MINIO_SECRET_KEY       = <service account secret>
  WEIGHTS_QWEN_SHA256    = <from step 6>
  WEIGHTS_WHISPER_SHA256 = <from step 6>
  WEIGHTS_BGE_M3_SHA256  = <from step 6>
  ```
  Verify:
  ```bash
  gh secret list | grep -E "^(VAST_AI_API_KEY|MINIO_|WEIGHTS_)" | wc -l
  ```
  Expected: 8 secrets listed.

- [ ] **9. build-pod.yml has already produced a candidate image**
  ```bash
  gh run list -w build-pod -L 1 --json status,conclusion,headSha
  ```
  Expected: at least one run with `conclusion: success` on `main` or `develop`.
  If empty, push a trivial change to trigger a build:
  ```bash
  git commit --allow-empty -m "chore: trigger build-pod.yml"
  git push origin develop
  ```

- [ ] **10. Trigger smoke.yml with defaults (D-19 gate evaluation, D-22 auto-teardown)**
  ```bash
  gh workflow run smoke.yml -f image_tag=develop
  sleep 10  # give GH time to enqueue
  gh run watch "$(gh run list -w smoke -L 1 --json databaseId -q '.[0].databaseId')"
  ```
  Expected: workflow completes; if all D-19 gates passed, exit 0.
  On failure, download smoke-report.json artifact and consult pod/README.md §Troubleshooting.

---

## After checklist

- Archive the green smoke-report.json to `.planning/phases/01-gpu-pod-image-smoke-test/baseline/` (pod/README.md §Baseline archival).
- Tag stable: `git tag v1.0.0 && git push origin v1.0.0` (D-23 promotion).
- Phase 1 COMPLETE. Proceed to `/gsd-plan-phase 2`.

## Credential rotation

Rotate MinIO service-account secret every 90 days (T-01-09-05 mitigation):

1. Create a new service account in MinIO console with the same bucket policy
2. Update GH Secrets (`MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`)
3. Trigger a fresh `smoke.yml` run to validate
4. Delete the old service account in MinIO

Weights do NOT need re-upload — they are credentials-agnostic objects.

## Failure recovery

| Step | Failure mode | Recovery |
|---|---|---|
| 1 | DNS/TLS | Infra team — fix MinIO ingress |
| 2-3 | Permission denied | Regenerate service-account; ensure bucket policy allows the account |
| 4 | <90 Mbps | D-02 SLA not met; escalate before running smoke (slow downloads push cold-start past 5 min gate) |
| 5 | HF rate-limit | Set `HF_TOKEN` and retry |
| 6 | Script crash | Check workdir disk (25 GB free?); re-run (idempotent) |
| 8 | Wrong secret name | Secret names MUST match smoke.yml exactly — see plan 08 frontmatter `user_setup` |
| 10 | Gate failure | See pod/README.md §Troubleshooting for the specific exit code |
