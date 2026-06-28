"use client";

/**
 * /reset-password/[token] — set-password landing for the invite/reset link.
 *
 * UI-SPEC v2 §Surface / RESEARCH §Primary use case: an owner provisions an
 * operator → Better Auth `requestPasswordReset` sends a Brevo email whose
 * link is `${BETTER_AUTH_URL}/reset-password/${token}` (RESEARCH Pitfall 6 —
 * `BETTER_AUTH_URL` MUST be the public origin `https://ai-dashboard.converse-ai.app`,
 * confirmed in Plan 02 Task 3 A4). The operator clicks the link, lands here,
 * and sets their own password via the built-in `authClient.resetPassword`.
 *
 * Unauthenticated surface — wrapped in `<AuthShell>` + `<Card max-w-sm>` to
 * mirror `2fa/enroll/page.tsx:174-175`. The token is read from the route
 * param via `useParams()` and is NEVER rendered (privacy rule — the page
 * shows only password inputs; no token, no credential).
 *
 * Form idiom (inline error + pending spinner) copied from
 * `login/page.tsx:158-205`. On success → `router.push("/login")`; the
 * middleware then routes an invited operator to `/2fa/enroll` on first login.
 *
 * Threat notes:
 *   - T-13-token: the token is consumed by Better Auth's single-use
 *     verification table; replay fails server-side.
 *   - T-13-disclosure: token never shown; only new-password + confirm inputs.
 */
import { useParams, useRouter } from "next/navigation";
import { useState } from "react";
import { AuthShell } from "@/components/auth/auth-shell";
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

export default function ResetPasswordPage() {
  const router = useRouter();
  const params = useParams<{ token: string }>();
  // `useParams` may return `string | string[]`; the [token] segment is a
  // single dynamic value. Never render this — privacy rule (T-13-disclosure).
  const rawToken = params?.token;
  const token = Array.isArray(rawToken) ? (rawToken[0] ?? "") : (rawToken ?? "");

  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);

    if (newPassword.length < MIN_PASSWORD_LENGTH) {
      setError("A senha precisa ter pelo menos 8 caracteres.");
      return;
    }
    if (confirmPassword !== newPassword) {
      setError("As senhas não coincidem.");
      return;
    }
    if (!token) {
      setError(
        "Link de redefinição inválido ou expirado. Solicite um novo convite.",
      );
      return;
    }

    setLoading(true);
    const res = await authClient.resetPassword({ newPassword, token });
    setLoading(false);

    if (res.error) {
      // Generic failure copy (UI-SPEC §Inline error — "Any server/network
      // failure"). Never echo the token or a server detail.
      setError(
        "Não foi possível concluir a ação agora. O link pode ter expirado. Solicite um novo convite.",
      );
      return;
    }

    // First login → middleware routes an invited operator to /2fa/enroll.
    router.push("/login");
  }

  return (
    <AuthShell>
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Definir nova senha</CardTitle>
          <CardDescription>
            Escolha a senha que você usará para entrar no painel. Após salvar,
            faça login normalmente.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
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
              <label
                htmlFor="confirm-password"
                className="text-xs font-semibold"
              >
                Confirmar nova senha
              </label>
              <Input
                id="confirm-password"
                type="password"
                autoComplete="new-password"
                required
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
                  Salvando…
                </span>
              ) : (
                "Salvar senha"
              )}
            </Button>
          </form>
        </CardContent>
      </Card>
    </AuthShell>
  );
}
