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
import type { PodConfigResponse, PodConfigStructural } from "@/lib/gateway";

// The page reads the live session + the gateway on every request.
export const dynamic = "force-dynamic";

/** The structural read-only fields (D-01). `mono` = image/weights key/SHA. */
const STRUCTURAL_FIELDS: {
  label: string;
  value: (s: PodConfigStructural) => string;
  mono?: boolean;
}[] = [
  { label: "Fuso horário", value: (s) => s.timezone },
  { label: "GPU primária", value: (s) => s.gpu_name_primary },
  { label: "GPU fallback", value: (s) => s.gpu_name_fallback },
  { label: "Nº GPUs (primário)", value: (s) => String(s.num_gpus_primary) },
  { label: "Nº GPUs (fallback)", value: (s) => String(s.num_gpus_fallback) },
  { label: "Imagem do template", value: (s) => s.template_image, mono: true },
  { label: "Imagem Infinity", value: (s) => s.infinity_image, mono: true },
  { label: "Imagem DCGM", value: (s) => s.dcgm_image, mono: true },
  { label: "Imagem Speaches", value: (s) => s.speaches_image, mono: true },
  { label: "Chave dos pesos Qwen", value: (s) => s.qwen_weights_key, mono: true },
  { label: "SHA256 Qwen", value: (s) => s.qwen_weights_sha256, mono: true },
  { label: "Chave Whisper", value: (s) => s.whisper_weights_key, mono: true },
  { label: "SHA256 Whisper", value: (s) => s.whisper_weights_sha256, mono: true },
  { label: "Chave BGE-M3", value: (s) => s.bgem3_weights_key, mono: true },
  { label: "SHA256 BGE-M3", value: (s) => s.bgem3_weights_sha256, mono: true },
  { label: "Chave Chatterbox", value: (s) => s.chatterbox_weights_key, mono: true },
  { label: "SHA256 Chatterbox", value: (s) => s.chatterbox_weights_sha256, mono: true },
  {
    label: "Chave/SHA Jinja",
    value: (s) =>
      s.qwen_jinja_key
        ? `${s.qwen_jinja_key} · ${s.qwen_jinja_sha256 || "sem sha"}`
        : "embutido no GGUF",
    mono: true,
  },
  {
    label: "Llama args",
    value: (s) =>
      `${(s.llama_args ?? []).join(" ")}${s.llama_args_default ? " (padrão do código)" : ""}`,
    mono: true,
  },
];


/**
 * Surface D — structural read-only display (D-01/D-02). These fields are NOT
 * `pod_config` columns: the gateway serves them from its boot env in the
 * `structural` key of /admin/primary/config. Changed via redeploy/env, never
 * edited here; "—" only when the gateway does not send the key. Read-only for BOTH
 * roles — no edit affordance, no pod-relaunch control.
 */
function StructuralPanel({
  structural,
}: {
  structural?: PodConfigStructural;
}) {
  return (
    <Card className="bg-muted/30">
      <CardHeader>
        <CardTitle className="text-[20px] font-semibold">
          Configuração estrutural (somente leitura)
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3">
          {STRUCTURAL_FIELDS.map((f) => {
            const v = structural ? f.value(structural) : "";
            return (
              <div key={f.label} className="flex min-w-0 flex-col gap-0.5">
                <span className="text-[12px] font-semibold text-muted-foreground">
                  {f.label}
                </span>
                <span
                  className={
                    f.mono
                      ? "break-all font-mono text-[12px] text-muted-foreground"
                      : "text-[14px] tabular-nums text-muted-foreground"
                  }
                  title="Definido via redeploy/env"
                >
                  {v || "—"}
                </span>
              </div>
            );
          })}
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
      <StructuralPanel structural={data?.structural} />
    </div>
  );
}
