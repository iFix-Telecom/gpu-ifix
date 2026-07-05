/**
 * /tenants/gerenciar — owner-aware tenant-management page (Phase 18, TEN-UI-10/11).
 *
 * An RSC: it reads the acting viewer's role server-side (`getViewerRole`) and the
 * tenant list server-side (`fetchTenantsServer` → the GET-only `/api/gateway/tenants`
 * proxy, so the admin key stays server-only). It passes `isOwner` + the tenant list
 * into the controls island — the island lists tenants, and (owner-only) creates a
 * tenant, generates an API key showing the raw ONCE, and revokes keys with an
 * impact confirm. Keys are fetched on demand when a tenant row is expanded (NOT here).
 *
 * Owner gate is COSMETIC — the server actions (Plan 18-03) re-check requireOwner on
 * every mutation. Rota NOVA — a `/tenants` existente é a página de MÉTRICAS.
 */
import { getViewerRole } from "@/lib/viewer";
import { fetchTenantsServer } from "@/lib/gateway-server";
import { TenantControls } from "./tenant-controls";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import type { TenantRow } from "@/lib/gateway";

// The page reads the live session + the gateway on every request.
export const dynamic = "force-dynamic";

export default async function TenantsGerenciarPage() {
  // Owner-gate: COSMETIC — the server actions re-check requireOwner.
  const viewerRole = await getViewerRole();
  const isOwner = viewerRole === "owner";

  let tenants: TenantRow[] = [];
  let loadError: string | null = null;
  try {
    tenants = await fetchTenantsServer();
  } catch (e) {
    loadError =
      (e as Error)?.message ?? "Não foi possível carregar a lista de tenants.";
  }

  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-col gap-1">
        <h1 className="text-[28px] font-semibold leading-[1.2]">
          Tenants (gestão)
        </h1>
        <p className="text-[12px] font-semibold text-muted-foreground">
          Criar tenant · gerar/revogar API key · classe de dados por-key ·
          edição owner-only
        </p>
      </div>

      {loadError ? (
        <Card>
          <CardHeader>
            <CardTitle className="text-[20px] font-semibold">
              Tenants
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-[14px] text-destructive" role="alert">
              Não foi possível carregar a lista de tenants: {loadError}
            </p>
          </CardContent>
        </Card>
      ) : (
        <TenantControls isOwner={isOwner} initialTenants={tenants} />
      )}
    </div>
  );
}
