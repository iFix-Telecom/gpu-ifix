"use client";

/**
 * Client island for the owner-only operator-management controls
 * (Phase 13, UM-10 / D-03 / D-04 / D-06 / D-07).
 *
 * The page itself stays an RSC (`page.tsx`) so the roster query + owner-gate
 * read run on the server; the interactive affordances live here behind the
 * cosmetic owner-gate (`page.tsx` renders these ONLY when `isOwner`). The
 * authoritative re-check is server-side in `@/lib/admin-actions`
 * (`requireOwner` on every action — a hidden-but-callable control is still
 * gated, T-13-authz).
 *
 * Surfaces:
 *   - <ProvisionOperatorButton/> → `dialog` (Nome + E-mail) → `inviteOperator`.
 *   - <OperatorRowActions/>      → `dropdown-menu` (··· / MoreHorizontal) with
 *     Resetar senha / Resetar 2FA / Remover operador, each behind an
 *     `alert-dialog` confirm (default-focus Cancelar, --destructive confirm).
 *
 * Copy is verbatim from 13-UI-SPEC §Copywriting. The ban/impersonation admin
 * endpoints are NEVER surfaced (D-01) — only invite/remove/reset-pw/reset-2FA.
 * Privacy (T-13-disclosure): only email is rendered — never TOTP/backup/hash/
 * temp-password/IP/raw UUID.
 */
import { KeyRound, MoreHorizontal, ShieldOff, Trash2 } from "lucide-react";
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
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import {
  inviteOperator,
  removeOperator,
  resetOperator2FA,
  resetOperatorPassword,
} from "@/lib/admin-actions";

/** Generic 14×14 inline pending spinner (matches login/page.tsx idiom). */
function Spinner() {
  return (
    <span
      aria-hidden
      className="inline-block size-3.5 animate-spin rounded-full border-2 border-current border-t-transparent"
    />
  );
}

const GENERIC_ERROR =
  "Não foi possível concluir a ação agora. Tente novamente em alguns segundos.";

/** Map a server-action throw to the UI-SPEC inline copy. */
function provisionErrorCopy(message: string): string {
  if (/allowlist|@ifixtelecom/i.test(message)) {
    return "Apenas e-mails @ifixtelecom.com.br são permitidos.";
  }
  if (/already|existe|exists|duplicate/i.test(message)) {
    return "Já existe um operador com este e-mail.";
  }
  return GENERIC_ERROR;
}

// ──────────────────────────────────────────────────────────────────────────
// + Provisionar operador → dialog (Nome + E-mail) → inviteOperator (D-04/D-05)
// ──────────────────────────────────────────────────────────────────────────

export function ProvisionOperatorButton() {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  function reset() {
    setName("");
    setEmail("");
    setError(null);
    setPending(false);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setPending(true);
    try {
      // actor omitted → server reads the live session and re-checks owner.
      await inviteOperator({ name, email });
      toast.success(`Convite enviado para ${email}.`);
      setOpen(false);
      reset();
    } catch (err) {
      setError(provisionErrorCopy((err as Error)?.message ?? ""));
      setPending(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <Button
        type="button"
        onClick={() => setOpen(true)}
        className="rounded-md border bg-[color:var(--primary)] text-[color:var(--primary-foreground,white)] text-xs font-semibold"
        style={{ padding: "5px 10px", borderRadius: 5 }}
      >
        + Provisionar operador
      </Button>
      <DialogContent
        className="gap-4"
        style={{ maxWidth: 384, padding: 24 }}
      >
        <DialogHeader>
          <DialogTitle>Provisionar operador</DialogTitle>
          <DialogDescription>
            O operador receberá um e-mail com um link para definir a própria
            senha. Acesso restrito a contas @ifixtelecom.com.br.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <label htmlFor="provision-name" className="text-xs font-semibold">
              Nome
            </label>
            <Input
              id="provision-name"
              type="text"
              autoComplete="name"
              value={name}
              disabled={pending}
              onChange={(ev) => setName(ev.target.value)}
            />
          </div>
          <div className="flex flex-col gap-2">
            <label htmlFor="provision-email" className="text-xs font-semibold">
              E-mail
            </label>
            <Input
              id="provision-email"
              type="email"
              autoComplete="off"
              required
              value={email}
              disabled={pending}
              onChange={(ev) => setEmail(ev.target.value)}
            />
          </div>
          {error && (
            <p className="text-xs text-destructive" role="alert">
              {error}
            </p>
          )}
          <DialogFooter className="gap-2">
            <DialogClose asChild>
              <Button type="button" variant="ghost" disabled={pending}>
                Cancelar
              </Button>
            </DialogClose>
            <Button type="submit" disabled={pending}>
              {pending ? (
                <span className="inline-flex items-center gap-2">
                  <Spinner />
                  Enviando…
                </span>
              ) : (
                "Enviar convite"
              )}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ──────────────────────────────────────────────────────────────────────────
// ··· row menu → dropdown-menu → per-action alert-dialog confirms
// (D-06 reset-password, D-07 reset-2FA, remove). Ban/impersonation NOT wired.
// ──────────────────────────────────────────────────────────────────────────

type ConfirmKind = "reset-password" | "reset-2fa" | "remove" | null;

export function OperatorRowActions({
  name,
  email,
  userId,
}: {
  name: string;
  email: string;
  userId: string;
}) {
  const [confirm, setConfirm] = useState<ConfirmKind>(null);
  const [pending, setPending] = useState(false);

  async function run(kind: Exclude<ConfirmKind, null>) {
    setPending(true);
    try {
      if (kind === "reset-password") {
        await resetOperatorPassword({ email, targetId: userId });
        toast.success(`E-mail de redefinição enviado para ${email}.`);
      } else if (kind === "reset-2fa") {
        await resetOperator2FA({ targetId: userId, targetEmail: email });
        toast.success(`2FA de ${email} resetado. Sessões encerradas.`);
      } else {
        await removeOperator({ targetId: userId, targetEmail: email });
        toast.success(`Operador ${email} removido.`);
      }
      setConfirm(null);
    } catch {
      toast.error(GENERIC_ERROR);
    } finally {
      setPending(false);
    }
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            className="text-muted-foreground hover:text-foreground"
            aria-label={`Ações para ${name}`}
          >
            <MoreHorizontal className="size-4" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => setConfirm("reset-password")}>
            <KeyRound className="size-4" />
            Resetar senha
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setConfirm("reset-2fa")}>
            <ShieldOff className="size-4" />
            Resetar 2FA
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="destructive"
            onSelect={() => setConfirm("remove")}
          >
            <Trash2 className="size-4" />
            Remover operador
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      {/* Resetar senha — revokes sessions (destructive-tone confirm). */}
      <ConfirmDialog
        open={confirm === "reset-password"}
        onOpenChange={(o) => !o && setConfirm(null)}
        title="Resetar senha?"
        description={`Envia um e-mail de redefinição para ${email} e encerra as sessões ativas dele. O operador define a própria senha nova pelo link.`}
        confirmLabel="Enviar reset de senha"
        pending={pending}
        onConfirm={() => run("reset-password")}
      />

      {/* Resetar 2FA — CR-01-safe: re-enroll on next login. */}
      <ConfirmDialog
        open={confirm === "reset-2fa"}
        onOpenChange={(o) => !o && setConfirm(null)}
        title="Resetar 2FA?"
        description={`Desativa o 2FA de ${email} e encerra as sessões dele. No próximo login ele será obrigado a configurar um novo autenticador.`}
        confirmLabel="Resetar 2FA"
        pending={pending}
        onConfirm={() => run("reset-2fa")}
      />

      {/* Remover operador — irreversible. */}
      <ConfirmDialog
        open={confirm === "remove"}
        onOpenChange={(o) => !o && setConfirm(null)}
        title="Remover operador?"
        description={`Remove ${email} e encerra todas as sessões dele imediatamente. Esta ação não pode ser desfeita.`}
        confirmLabel="Remover operador"
        pending={pending}
        onConfirm={() => run("remove")}
      />
    </>
  );
}

/**
 * Shared destructive `alert-dialog` confirm. Radix AlertDialog default-focuses
 * the Cancel control; the confirm button carries `variant="destructive"`
 * (revokes sessions). Pending = confirm disabled + spinner.
 */
function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  pending,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmLabel: string;
  pending: boolean;
  onConfirm: () => void;
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent style={{ maxWidth: 384, padding: 24 }}>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter className="gap-2">
          <AlertDialogCancel disabled={pending}>Cancelar</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={pending}
            onClick={(e) => {
              // Keep the dialog mounted through the async op (it closes on
              // success via setConfirm(null)); prevent Radix auto-close so the
              // pending spinner is visible.
              e.preventDefault();
              onConfirm();
            }}
          >
            {pending ? (
              <span className="inline-flex items-center gap-2">
                <Spinner />
                {confirmLabel}
              </span>
            ) : (
              confirmLabel
            )}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
