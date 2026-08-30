/**
 * /operacao — RSC shell (quick 260830-o2j). Reads the viewer role server-side
 * (`getViewerRole`, cosmetic gate — server actions re-check requireOwner) and
 * renders the live client island with `isOwner` so the pod force-up/down card
 * can show. Everything else (10s React Query poll, panels) lives in
 * `operacao-client.tsx`, unchanged.
 */
import { getViewerRole } from "@/lib/viewer";
import { OperacaoClient } from "./operacao-client";

export const dynamic = "force-dynamic";

export default async function OperacaoPage() {
  const viewerRole = await getViewerRole();
  return <OperacaoClient isOwner={viewerRole === "owner"} />;
}
