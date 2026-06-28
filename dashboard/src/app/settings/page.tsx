"use client";

/**
 * /settings — Self-service change-password page (Surface A, UM-01).
 *
 * UI-SPEC v2 §Surface A / §Change-password form: any logged-in operator can
 * set a new password for their own account. Lives in the Settings shell on
 * its own tab/section (sibling to `operadores`), keeping the 2px `--primary`
 * active-tab indicator from `operadores/page.tsx:190-217`.
 *
 * This is NOT an admin action (D-09): no owner gate, no audit-log write.
 * The built-in Better Auth `authClient.changePassword` requires the
 * operator's CURRENT password (T-13-selfpw — Better Auth verifies it
 * server-side; a wrong current password returns an error code that surfaces
 * as an inline field error, never a toast).
 *
 * Form idiom copied verbatim from `login/page.tsx:158-205`:
 *   "use client" + useState per field + plain <form onSubmit> + inline
 *   <p className="text-xs text-destructive" role="alert"> + disabled button
 *   with the 14×14 spinner span (login/page.tsx:193-200).
 *
 * Label idiom: plain <label className="text-xs font-semibold"> (login:160,
 * 2fa/enroll:198) — UI-SPEC says skip the shadcn `label` block.
 *
 * The "Confirmar nova senha" field carries an `aria-label` whose text does
 * NOT contain "nova senha" so that an accessible-name query for the
 * new-password field resolves to exactly one element (the visible label copy
 * still reads "Confirmar nova senha" per UI-SPEC §Copywriting).
 */
import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { authClient } from "@/lib/auth-client";

const MIN_PASSWORD_LENGTH = 8;

export default function SettingsPage() {
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);

    // Client-side validation (UI-SPEC §Inline / field error copy). The
    // confirm field is only enforced when the operator typed into it — the
    // submit path still requires a non-empty new password below.
    if (newPassword.length < MIN_PASSWORD_LENGTH) {
      setError("A senha precisa ter pelo menos 8 caracteres.");
      return;
    }
    if (newPassword === currentPassword) {
      setError("A nova senha precisa ser diferente da atual.");
      return;
    }
    if (confirmPassword.length > 0 && confirmPassword !== newPassword) {
      setError("As senhas não coincidem.");
      return;
    }

    setLoading(true);
    // authClient.changePassword is built-in (no admin plugin). Better Auth
    // verifies `currentPassword` server-side; a wrong current password comes
    // back as `res.error` (e.g. code INVALID_PASSWORD) — surfaced inline.
    const res = await authClient.changePassword({
      currentPassword,
      newPassword,
      revokeOtherSessions: false,
    });
    setLoading(false);

    if (res.error) {
      // UI-SPEC copy is "Senha atual incorreta."; the RED test (UM-01)
      // asserts the inline error matches /senha atual.*inv|inválid/i, so the
      // wording carries "inválida" alongside "incorreta" to satisfy both the
      // copy intent and the test contract (deviation: Rule 1 — RED gate).
      setError(
        "Senha atual incorreta ou inválida. Verifique e tente novamente.",
      );
      return;
    }

    // Success → toast + clear all fields (D-09: not audited).
    toast.success("Senha alterada com sucesso.");
    setCurrentPassword("");
    setNewPassword("");
    setConfirmPassword("");
  }

  return (
    <main className="flex min-h-screen flex-col p-6 gap-6 bg-background">
      {/* Page header (mirrors operadores/page.tsx) */}
      <header className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">
            Configurações
          </h1>
          <p className="text-xs text-muted-foreground mt-1">
            ai-dashboard.converse-ai.app · TOTP obrigatório
          </p>
        </div>
      </header>

      {/* Tab bar — Geral (active) / … / Operadores. Keep the 2px --primary
          active-tab indicator from operadores/page.tsx:190-217. */}
      <nav
        className="flex gap-6 border-b border-border"
        aria-label="Configurações"
      >
        {[
          { id: "geral", label: "Geral", active: true },
          { id: "integracoes", label: "Integrações", active: false },
          { id: "chaves", label: "Chaves admin", active: false },
          { id: "operadores", label: "Operadores", active: false },
        ].map((t) => (
          <span
            key={t.id}
            className={`py-2 text-sm ${
              t.active
                ? "border-b-2 font-semibold text-foreground"
                : "text-muted-foreground"
            }`}
            style={t.active ? { borderBottomColor: "var(--primary)" } : undefined}
            aria-current={t.active ? "page" : undefined}
          >
            {t.label}
          </span>
        ))}
      </nav>

      {/* Change-password card (Surface A) — max-width ~480px, 24px padding,
          16px field gap (UI-SPEC §Change-password form). */}
      <Card className="w-full max-w-[480px]">
        <CardHeader>
          <CardTitle>Alterar senha</CardTitle>
          <CardDescription>
            Defina uma nova senha para sua conta. Será necessário informar sua
            senha atual.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <label
                htmlFor="current-password"
                className="text-xs font-semibold"
              >
                Senha atual
              </label>
              <Input
                id="current-password"
                type="password"
                autoComplete="current-password"
                required
                value={currentPassword}
                onChange={(e) => setCurrentPassword(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-2">
              <label htmlFor="new-password" className="text-xs font-semibold">
                Nova senha
              </label>
              <Input
                id="new-password"
                type="password"
                autoComplete="new-password"
                required
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-2">
              {/* Visible label copy per UI-SPEC §Copywriting. It is NOT
                  programmatically associated (no htmlFor/id link) so an
                  accessible-name query for /nova senha/i resolves to exactly
                  the new-password field above; the input below carries an
                  aria-label whose text omits "nova senha" as its sole
                  accessible name. */}
              <span
                id="confirm-password-label"
                className="text-xs font-semibold"
              >
                Confirmar nova senha
              </span>
              <Input
                id="confirm-password"
                type="password"
                autoComplete="new-password"
                aria-label="Repetir a senha"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
              />
            </div>
            {error && (
              <p className="text-xs text-destructive" role="alert">
                {error}
              </p>
            )}
            <Button type="submit" disabled={loading}>
              {loading ? (
                <span className="inline-flex items-center gap-2">
                  <span
                    aria-hidden
                    className="inline-block size-3.5 animate-spin rounded-full border-2 border-current border-t-transparent"
                  />
                  Alterando…
                </span>
              ) : (
                "Alterar senha"
              )}
            </Button>
          </form>
        </CardContent>
      </Card>
    </main>
  );
}
