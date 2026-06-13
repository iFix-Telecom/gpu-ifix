"use client";

/**
 * /2fa/backup — Alphanumeric backup-code entry (UI-SPEC v2 §screen 14).
 *
 * Fallback for the TOTP challenge when the operator's authenticator
 * device is lost (D-12 + reviews consensus #6 / 11-09 RUNBOOK-2FA-RECOVERY
 * downstream).
 */
import { useRouter } from "next/navigation";
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

export default function BackupPage() {
  const router = useRouter();
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [verifying, setVerifying] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setVerifying(true);
    try {
      const res = await authClient.twoFactor.verifyBackupCode({ code });
      setVerifying(false);
      if (res.error) {
        setError(
          "Código de backup inválido ou já utilizado. Cada código funciona apenas uma vez.",
        );
        return;
      }
      router.push("/");
      router.refresh();
    } catch (_e) {
      setVerifying(false);
      setError(
        "Não foi possível verificar o código agora. Tente novamente em alguns segundos.",
      );
    }
  }

  return (
    <AuthShell>
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Código de backup</CardTitle>
          <CardDescription>
            Digite um dos 10 códigos de backup gerados durante o cadastro 2FA.
            Cada código funciona apenas uma vez.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <label htmlFor="backup-code" className="text-xs font-semibold">
                Código de backup
              </label>
              <Input
                id="backup-code"
                type="text"
                inputMode="text"
                autoComplete="off"
                placeholder="xxxx-xxxx"
                required
                className="font-mono tracking-widest"
                value={code}
                onChange={(e) => setCode(e.target.value)}
              />
            </div>
            {error && (
              <p className="text-xs text-destructive" role="alert">
                {error}
              </p>
            )}
            <Button type="submit" disabled={verifying || code.length === 0}>
              {verifying ? "Verificando…" : "Entrar com backup"}
            </Button>
            <button
              type="button"
              onClick={() => router.push("/2fa/challenge")}
              className="text-xs text-muted-foreground hover:text-foreground transition-colors text-center"
            >
              Voltar ao app autenticador
            </button>
          </form>
        </CardContent>
      </Card>
    </AuthShell>
  );
}
