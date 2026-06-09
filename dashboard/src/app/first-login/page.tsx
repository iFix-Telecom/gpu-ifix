"use client";

/**
 * /first-login — Welcome gate post-password-verify (UI-SPEC v2 §screen 7).
 *
 * Shown when the middleware detects `user.twoFactorEnabled === false`
 * (i.e. the operator just signed up and hasn't enrolled yet). The CTA
 * routes to /2fa/enroll. The middleware already redirects to /2fa/enroll
 * directly today; this page is the one-time welcome gate documented in
 * UI-SPEC v2 §screen 7 — operators may visit it directly to read the
 * 3-bullet explainer before the enrollment form.
 */
import { ArrowRight, CircleAlert, ShieldCheck } from "lucide-react";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { AuthShell } from "@/components/auth/auth-shell";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { useSession } from "@/lib/auth-client";

export default function FirstLoginPage() {
  const router = useRouter();
  const session = useSession();
  const [name, setName] = useState<string>("");

  useEffect(() => {
    const u = (session?.data as { user?: { name?: string } } | undefined)?.user;
    setName(u?.name ?? "");
  }, [session]);

  return (
    <AuthShell>
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <ShieldCheck
              className="size-[18px] text-[color:var(--primary)]"
              strokeWidth={2.2}
            />
            {name ? `Bem-vindo, ${name}` : "Bem-vindo"}
          </CardTitle>
          <CardDescription>
            Este é seu primeiro login. Antes de acessar o painel, é necessário
            ativar a verificação em duas etapas.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex flex-col gap-4">
            <div
              className="rounded-md border"
              style={{
                padding: "12px 16px",
                background:
                  "color-mix(in oklch, var(--status-warning, oklch(0.769 0.188 70.08)) 12%, var(--card))",
                borderColor:
                  "color-mix(in oklch, var(--status-warning, oklch(0.769 0.188 70.08)) 45%, transparent)",
              }}
              role="alert"
            >
              <p className="flex items-start gap-2 text-xs">
                <CircleAlert
                  className="mt-0.5 size-4 text-[color:var(--status-warning,oklch(0.769_0.188_70.08))]"
                  aria-hidden
                />
                <span>
                  <span className="font-semibold">
                    2FA obrigatório para esta conta.
                  </span>{" "}
                  Todas as contas{" "}
                  <span className="font-mono">@ifixtelecom.com.br</span> são
                  obrigadas a configurar TOTP no primeiro acesso.
                </span>
              </p>
            </div>
            <ul className="list-disc pl-5 text-xs text-muted-foreground flex flex-col gap-1">
              <li>Use Google Authenticator, 1Password ou Authy.</li>
              <li>
                Você receberá 10 códigos de backup para usar caso perca o
                dispositivo.
              </li>
              <li>
                A configuração leva ~1 minuto e pode ser feita uma única vez.
              </li>
            </ul>
            <Button type="button" onClick={() => router.push("/2fa/enroll")}>
              Configurar 2FA agora
              <ArrowRight className="ml-2 size-[14px]" strokeWidth={2.2} />
            </Button>
          </div>
        </CardContent>
      </Card>
    </AuthShell>
  );
}
