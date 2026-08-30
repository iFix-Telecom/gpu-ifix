/**
 * /modelos — model aliases + upstreams control page (quick 260830-o2j).
 *
 * An RSC: reads the viewer role (`getViewerRole`) and the alias + upstream
 * lists server-side (`fetchModelAliasesServer` / `fetchUpstreamsServer` →
 * gateway admin API, key stays server-only) and hands them to the client
 * island. Owner: create/edit/delete alias rows (target + OpenRouter provider
 * prefs for openrouter-chat) and toggle upstream `enabled`. Operator:
 * read-only. Owner gate is COSMETIC — the server actions re-check requireOwner.
 */
import { getViewerRole } from "@/lib/viewer";
import {
  fetchModelAliasesServer,
  fetchUpstreamsServer,
} from "@/lib/gateway-server";
import type { ModelAliasRow, UpstreamRow } from "@/lib/gateway";
import { ModelosControls } from "./modelos-controls";

export const dynamic = "force-dynamic";

export default async function ModelosPage() {
  const viewerRole = await getViewerRole();
  const isOwner = viewerRole === "owner";

  let aliases: ModelAliasRow[] = [];
  let upstreams: UpstreamRow[] = [];
  let loadError: string | null = null;
  try {
    [aliases, upstreams] = await Promise.all([
      fetchModelAliasesServer(),
      fetchUpstreamsServer(),
    ]);
  } catch (e) {
    loadError =
      (e as Error)?.message ??
      "Não foi possível carregar modelos e upstreams.";
  }

  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-col gap-1">
        <h1 className="text-[28px] font-semibold leading-[1.2]">
          Modelos &amp; upstreams
        </h1>
        <p className="text-[12px] font-semibold text-muted-foreground">
          Aliases → upstreams/targets · roteamento de provedor OpenRouter por
          modelo · ligar/desligar upstreams · edição owner-only
        </p>
      </div>

      {loadError ? (
        <p className="text-[14px] text-destructive" role="alert">
          Não foi possível carregar: {loadError}
        </p>
      ) : (
        <ModelosControls
          isOwner={isOwner}
          initialAliases={aliases}
          initialUpstreams={upstreams}
        />
      )}
    </div>
  );
}
