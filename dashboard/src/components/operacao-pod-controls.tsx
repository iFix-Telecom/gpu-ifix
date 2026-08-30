"use client";

/**
 * OperacaoPodControls — owner-only "Controle do pod" card (quick 260830-o2j).
 *
 * Ligar (force-up) / Desligar (force-down) publish the SAME Redis events
 * `gatewayctl primary force-up|force-down` publish; the reconciler leader
 * applies them on its next tick, so the toast says "pedido enfileirado", not
 * "pod ligado". Both go through an alert-dialog carrying the concrete impact
 * (Vast cost while up / in-flight requests fall to tier-1) and an optional
 * reason that lands in the lifecycle trigger_reason. Operators see the card
 * disabled with an explanatory note — the server action re-checks
 * requireOwner regardless (UI gate is cosmetic).
 */

import { Power, PowerOff } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { primaryControl } from "@/lib/admin-actions";

type Action = "force-up" | "force-down";

const COPY: Record<
  Action,
  { title: string; impact: string; cta: string; ok: string }
> = {
  "force-up": {
    title: "Ligar o pod primário agora?",
    impact:
      "Publica force_up_request: o gateway provisiona um pod na Vast fora do schedule (custo por hora até o próximo desligamento agendado ou manual). Sem efeito se o pod já estiver ativo/provisionando.",
    cta: "Ligar pod",
    ok: "Pedido de ligar enfileirado — o reconciler aplica no próximo tick.",
  },
  "force-down": {
    title: "Desligar o pod primário agora?",
    impact:
      "Publica force_down_request: o pod entra em drain e é destruído; requests em voo caem para o tier-1 externo (tenants sensitive recebem 503 até o pod voltar). O schedule pode religá-lo na próxima janela.",
    cta: "Desligar pod",
    ok: "Pedido de desligar enfileirado — o reconciler aplica no próximo tick.",
  },
};

export function OperacaoPodControls({
  isOwner,
  fsmState,
}: {
  isOwner: boolean;
  fsmState?: string;
}) {
  const [target, setTarget] = useState<Action | null>(null);
  const [reason, setReason] = useState("");
  const [pending, setPending] = useState(false);

  async function confirm() {
    if (!target) return;
    setPending(true);
    try {
      await primaryControl({ action: target, reason });
      toast.success(COPY[target].ok);
      setTarget(null);
      setReason("");
    } catch (err) {
      toast.error(
        (err as Error)?.message ??
          "Não foi possível enviar o pedido. Tente novamente.",
      );
    } finally {
      setPending(false);
    }
  }

  return (
    <Card data-testid="pod-controls">
      <CardHeader>
        <CardTitle className="text-[20px] font-semibold">Controle do pod</CardTitle>
        <CardDescription>
          Força ligar/desligar o pod primário fora do schedule (mesmo contrato do{" "}
          <code>gatewayctl primary force-up|force-down</code>).
          {fsmState ? (
            <>
              {" "}
              Estado atual: <span className="font-mono">{fsmState}</span>.
            </>
          ) : null}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap items-center gap-3">
        <Button
          type="button"
          disabled={!isOwner}
          onClick={() => setTarget("force-up")}
        >
          <Power className="size-4" />
          Ligar pod
        </Button>
        <Button
          type="button"
          variant="destructive"
          disabled={!isOwner}
          onClick={() => setTarget("force-down")}
        >
          <PowerOff className="size-4" />
          Desligar pod
        </Button>
        {!isOwner && (
          <span className="text-[12px] text-muted-foreground">
            Somente owner pode ligar/desligar o pod.
          </span>
        )}
      </CardContent>

      <AlertDialog open={target !== null} onOpenChange={(o) => !o && !pending && setTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{target ? COPY[target].title : ""}</AlertDialogTitle>
            <AlertDialogDescription>{target ? COPY[target].impact : ""}</AlertDialogDescription>
          </AlertDialogHeader>
          <label className="flex flex-col gap-1">
            <span className="text-[12px] font-semibold text-muted-foreground">
              Motivo (opcional, fica no histórico do lifecycle)
            </span>
            <Input
              value={reason}
              maxLength={200}
              placeholder="ex.: demanda extra do suporte"
              onChange={(e) => setReason(e.target.value)}
            />
          </label>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={pending}>Cancelar</AlertDialogCancel>
            <AlertDialogAction
              disabled={pending}
              onClick={(e) => {
                e.preventDefault();
                void confirm();
              }}
              className={target === "force-down" ? "bg-destructive text-destructive-foreground hover:bg-destructive/90" : undefined}
            >
              {pending ? "Enviando…" : target ? COPY[target].cta : ""}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  );
}
