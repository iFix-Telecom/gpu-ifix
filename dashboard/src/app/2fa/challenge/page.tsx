"use client";

/**
 * /2fa/challenge — TOTP login challenge (D-12).
 *
 * UI-SPEC v2 §screens 11/12/13:
 *   - default — OTP slots + countdown + "Usar código de backup" ghost link
 *   - invalid — red OTP slot border + inline CircleAlert error
 *   - success — 800ms transient primary-tinted slots + "Verificado" disabled
 *
 * On success, router.push("/") + router.refresh() — the middleware will
 * pick up the new session.twoFactorVerified=true claim via cookie cache.
 */
import { ShieldCheck } from "lucide-react";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { AuthShell } from "@/components/auth/auth-shell";
import { OtpRow } from "@/components/auth/otp-row";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { authClient } from "@/lib/auth-client";

export default function ChallengePage() {
  const router = useRouter();
  const [code, setCode] = useState("");
  const [otpState, setOtpState] = useState<"default" | "invalid" | "success">(
    "default",
  );
  const [error, setError] = useState<string | null>(null);
  const [verifying, setVerifying] = useState(false);
  const [countdown, setCountdown] = useState(30);

  useEffect(() => {
    const id = setInterval(
      () => setCountdown((c) => (c <= 1 ? 30 : c - 1)),
      1000,
    );
    return () => clearInterval(id);
  }, []);

  async function handleVerify(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setVerifying(true);
    try {
      const res = await authClient.twoFactor.verifyTotp({ code });
      setVerifying(false);
      if (res.error) {
        setOtpState("invalid");
        setError(
          "Código incorreto. Confirme o código atual no seu app autenticador e tente novamente.",
        );
        return;
      }
      setOtpState("success");
      setTimeout(() => {
        router.push("/");
        router.refresh();
      }, 800);
    } catch (_e) {
      setVerifying(false);
      setOtpState("invalid");
      setError(
        "Não foi possível confirmar o código agora. Tente novamente em alguns segundos.",
      );
    }
  }

  return (
    <AuthShell>
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <ShieldCheck
              className="size-[18px] text-[color:var(--primary)]"
              strokeWidth={2.2}
            />
            Verificação em duas etapas
          </CardTitle>
          <CardDescription>
            Digite o código de 6 dígitos do seu app autenticador.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleVerify} className="flex flex-col gap-4">
            <OtpRow
              value={code}
              onChange={(v) => {
                setCode(v);
                if (otpState === "invalid") setOtpState("default");
              }}
              state={otpState}
              autoFocus
              disabled={verifying || otpState === "success"}
            />
            <p className="text-xs text-center text-muted-foreground">
              novo código em <span className="tabular-nums">{countdown}s</span>
            </p>
            {error && (
              <p className="text-xs text-destructive text-center" role="alert">
                {error}
              </p>
            )}
            <Button
              type="submit"
              disabled={
                verifying || code.length !== 6 || otpState === "success"
              }
            >
              {verifying
                ? "Verificando…"
                : otpState === "success"
                  ? "Verificado"
                  : "Confirmar código"}
            </Button>
            <button
              type="button"
              onClick={() => router.push("/2fa/backup")}
              className="text-xs text-muted-foreground hover:text-foreground transition-colors text-center"
            >
              Usar código de backup
            </button>
          </form>
        </CardContent>
      </Card>
    </AuthShell>
  );
}
