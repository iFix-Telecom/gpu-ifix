# poc-voz-realtime

Webhook da PoC SEED-009 (OpenAI Realtime via SIP). Plano e gates em
`.planning/quick/260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md`.

- `POST /webhook` — `realtime.call.incoming` assinado (fail-closed: 503 sem `OPENAI_WEBHOOK_SECRET`).
- `GET /health`
- Só aceita chamada cujo From user = `POC_CALLER_TOKEN` (setado como `from_user` no endpoint PJSIP `openai-realtime`); resto → 603.
- Travas: 1 chamada simultânea (`POC_MAX_CONCURRENT`), hangup em `POC_MAX_CALL_SECONDS` (720).
- Log NDJSON por chamada em `data/calls/<call_id>.ndjson` (`turn_latency`, transcrições, `usage`).

Deploy (worker-vm): rsync p/ `/opt/poc-voz-realtime` + `docker compose up -d --build`.
Secrets em `/opt/poc-voz-realtime/secrets.env` (root 600): `OPENAI_API_KEY`, `OPENAI_PROJECT_ID`,
`OPENAI_WEBHOOK_SECRET`, `POC_CALLER_TOKEN`. Fonte da chave: `ops-claude:/etc/onboard/secrets/openai-poc-voz.env`.

Remover: `docker compose down --rmi local && rm -rf /opt/poc-voz-realtime`.
