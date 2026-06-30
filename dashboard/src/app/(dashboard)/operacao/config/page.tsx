/**
 * /operacao/config — owner-aware pod-config control page (Phase 17).
 *
 * An RSC: it reads the acting viewer's role server-side (`getViewerRole`, D-07)
 * and the CURRENT pod config server-side (`fetchPodConfigServer` → the GET-only
 * `/api/gateway/primary/config` proxy, so the admin key stays server-only). It
 * passes `isOwner` + the live `config` (16 hot fields) + `bounds` (the numeric
 * min/max pairs) into the controls island — the island's ONLY data source, so
 * the editor renders live DB values, never stale boot env.
 *
 * Four surfaces (UI-SPEC §net-new screens), in diagnostic-first order:
 *   C  Provisionamento ao vivo  — live FSM + event trail (10s poll, client).
 *   A  Config HOT editor        — 16 fields, owner-edit / operator read-only.
 *   B  Limites de validação     — bounds table, owner-edit / operator read-only.
 *   D  Configuração estrutural  — 19 structural fields, READ-ONLY for both.
 *
 * Owner gate is COSMETIC — the server actions (Plan 17-05) re-check requireOwner
 * on every edit. NO pod-relaunch control, NO structural-edit form (D-01/D-02).
 */
import { getViewerRole } from "@/lib/viewer";
import { fetchPodConfigServer } from "@/lib/gateway-server";
import { PodConfigControls } from "@/components/pod-config-controls";
import { PodConfigLivePanel } from "@/components/pod-config-live-panel";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import type { PodConfigResponse } from "@/lib/gateway";

// The page reads the live session + the gateway on every request.
export const dynamic = "force-dynamic";

/** The 19 structural read-only fields (D-01). `mono` = weights key/SHA. */
const STRUCTURAL_FIELDS: { label: string; mono?: boolean }[] = [
  { label: "Fuso horário" },
  { label: "GPU primária" },
  { label: "GPU fallback" },
  { label: "Nº GPUs (primário)" },
  { label: "Nº GPUs (fallback)" },
  { label: "Imagem do template" },
  { label: "Imagem Infinity" },
  { label: "Imagem DCGM" },
  { label: "Imagem Speaches" },
  { label: "Chave dos pesos Qwen", mono: true },
  { label: "SHA256 Qwen", mono: true },
  { label: "Chave Whisper", mono: true },
  { label: "SHA256 Whisper", mono: true },
  { label: "Chave BGE-M3", mono: true },
  { label: "SHA256 BGE-M3", mono: true },
  { label: "Chave Chatterbox", mono: true },
  { label: "SHA256 Chatterbox", mono: true },
  { label: "Chave/SHA Jinja", mono: true },
  { label: "Llama args", mono: true },
];

/**
 * Surface D — structural read-only display (D-01/D-02). These fields are NOT
 * `pod_config` columns (the gateway read endpoint does not expose them by
 * construction — config_read.go), so they carry no live value in the dashboard;
 * they are changed via redeploy/env, never edited here. Read-only for BOTH
 * roles — no edit affordance, no pod-relaunch control.
 */
function StructuralPanel() {
  return (
    <Card className="bg-muted/30">
      <CardHeader>
        <CardTitle className="text-[20px] font-semibold">
          Configuração estrutural (somente leitura)
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3">
          {STRUCTURAL_FIELDS.map((f) => (
            <div key={f.label} className="flex flex-col gap-0.5">
              <span className="text-[12px] font-semibold text-muted-foreground">
                {f.label}
              </span>
              <span
                className={
                  f.mono
                    ? "truncate font-mono text-[12px] text-muted-foreground"
                    : "text-[14px] tabular-nums text-muted-foreground"
                }
                title="Definido via redeploy/env"
              >
                —
              </span>
            </div>
          ))}
        </div>
        <p className="text-[12px] text-muted-foreground">
          Alterado via redeploy/env — não editável aqui.
        </p>
      </CardContent>
    </Card>
  );
}

export default async function ConfigDoPodPage() {
  // Owner-gate (D-07): COSMETIC — the server actions re-check requireOwner.
  const viewerRole = await getViewerRole();
  const isOwner = viewerRole === "owner";

  let data: PodConfigResponse | null = null;
  let loadError: string | null = null;
  try {
    data = await fetchPodConfigServer();
  } catch (e) {
    loadError =
      (e as Error)?.message ??
      "Não foi possível carregar a configuração do pod.";
  }

  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-col gap-1">
        <h1 className="text-[28px] font-semibold leading-[1.2]">
          Config do pod
        </h1>
        <p className="text-[12px] font-semibold text-muted-foreground">
          Config HOT do pod primário · edição owner-only · vale a partir do
          próximo provisionamento
        </p>
      </div>

      {/* Surface C — live provisioning panel (top, diagnostic-first). */}
      <PodConfigLivePanel />

      {/* Surfaces A + B — config editor + bounds (owner-edit / operator RO). */}
      {loadError ? (
        <p className="text-[14px] text-destructive" role="alert">
          Não foi possível carregar a configuração do pod: {loadError}
        </p>
      ) : data ? (
        <PodConfigControls
          isOwner={isOwner}
          config={data.config}
          bounds={data.bounds}
        />
      ) : null}

      {/* Surface D — structural read-only. */}
      <StructuralPanel />
    </div>
  );
}
