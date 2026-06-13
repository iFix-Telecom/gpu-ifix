"use client";

/**
 * /2fa/enroll — TOTP enrollment 3-step state machine (D-12).
 *
 * UI-SPEC v2 §screens 8/9/10:
 *   Step 1 ("qr")     — show 192×192 QR + manual TOTP secret pill +
 *                       algorithm/period metadata. Primary CTA
 *                       "Já escaneei, continuar".
 *   Step 2 ("verify") — 6-digit OTP input + 30s countdown. Primary CTA
 *                       "Confirmar código".
 *   Step 3 ("backup") — 10 backup codes in 2-column grid + warning +
 *                       "Copiar tudo" + "Salvar e continuar".
 *
 * The TOTP secret + backup codes come from `authClient.twoFactor.enable()`
 * which returns `{ totpURI, secret, backupCodes }`. The QR PNG is rendered
 * client-side via `QRCode.toDataURL(totpURI)` (qrcode npm package).
 *
 * Acceptance criteria (Task 11-02-04 done):
 *   - `<img>` whose src begins with `data:image/png;base64,` (QR present)
 *   - `authClient.twoFactor` is called (enable + verifyTotp)
 *   - Wrapped in `<AuthShell>`
 */
import { ArrowRight, ShieldCheck } from "lucide-react";
import { useRouter } from "next/navigation";
import QRCode from "qrcode";
import { useEffect, useState } from "react";
import { toast } from "sonner";
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
import { Input } from "@/components/ui/input";
import { authClient } from "@/lib/auth-client";

type EnrollStep = "qr" | "verify" | "backup";

export default function EnrollPage() {
  const router = useRouter();
  const [step, setStep] = useState<EnrollStep>("qr");

  // Step 1 state
  const [password, setPassword] = useState("");
  const [enrolling, setEnrolling] = useState(false);
  const [enrollError, setEnrollError] = useState<string | null>(null);
  const [totpURI, setTotpURI] = useState<string>("");
  const [secret, setSecret] = useState<string>("");
  const [qrDataUrl, setQrDataUrl] = useState<string>("");
  const [backupCodes, setBackupCodes] = useState<string[]>([]);

  // Step 2 state
  const [code, setCode] = useState("");
  const [otpState, setOtpState] = useState<"default" | "invalid" | "success">(
    "default",
  );
  const [verifyError, setVerifyError] = useState<string | null>(null);
  const [verifying, setVerifying] = useState(false);
  const [countdown, setCountdown] = useState(30);

  // 30s TOTP window countdown (UI-SPEC §screen 9 / §Countdown copy).
  useEffect(() => {
    if (step !== "verify") return;
    setCountdown(30);
    const id = setInterval(() => {
      setCountdown((c) => (c <= 1 ? 30 : c - 1));
    }, 1000);
    return () => clearInterval(id);
  }, [step]);

  // Render the QR PNG once we have a totpURI.
  useEffect(() => {
    if (!totpURI) return;
    let cancelled = false;
    (async () => {
      try {
        const dataUrl = await QRCode.toDataURL(totpURI, {
          errorCorrectionLevel: "M",
          margin: 1,
          width: 192,
        });
        if (!cancelled) setQrDataUrl(dataUrl);
      } catch (_e) {
        if (!cancelled) {
          setEnrollError(
            "Não foi possível gerar o QR code agora. Tente novamente em alguns segundos.",
          );
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [totpURI]);

  async function handleEnable(e: React.FormEvent) {
    e.preventDefault();
    setEnrollError(null);
    setEnrolling(true);
    try {
      const res = await authClient.twoFactor.enable({ password });
      setEnrolling(false);
      if (res.error) {
        setEnrollError(
          "Não foi possível iniciar o cadastro 2FA. Confirme sua senha e tente novamente.",
        );
        return;
      }
      const data = res.data as
        | { totpURI?: string; secret?: string; backupCodes?: string[] }
        | undefined;
      setTotpURI(data?.totpURI ?? "");
      setSecret(data?.secret ?? "");
      setBackupCodes(data?.backupCodes ?? []);
    } catch (_e) {
      setEnrolling(false);
      setEnrollError(
        "Não foi possível iniciar o cadastro 2FA. Tente novamente em alguns segundos.",
      );
    }
  }

  async function handleVerify(e: React.FormEvent) {
    e.preventDefault();
    setVerifyError(null);
    setVerifying(true);
    try {
      const res = await authClient.twoFactor.verifyTotp({ code });
      setVerifying(false);
      if (res.error) {
        setOtpState("invalid");
        setVerifyError(
          "Código incorreto. Confirme o código atual no seu app autenticador e tente novamente.",
        );
        return;
      }
      setOtpState("success");
      // 800ms transient success state per UI-SPEC §screen 13.
      setTimeout(() => setStep("backup"), 800);
    } catch (_e) {
      setVerifying(false);
      setOtpState("invalid");
      setVerifyError(
        "Não foi possível confirmar o código agora. Tente novamente em alguns segundos.",
      );
    }
  }

  function copySecret() {
    if (!secret) return;
    navigator.clipboard.writeText(secret).then(
      () => toast.success("Segredo copiado."),
      () => toast.error("Não foi possível copiar."),
    );
  }

  function copyAllBackup() {
    if (backupCodes.length === 0) return;
    navigator.clipboard.writeText(backupCodes.join("\n")).then(
      () =>
        toast.success(
          `${backupCodes.length} códigos copiados para a área de transferência.`,
        ),
      () => toast.error("Não foi possível copiar."),
    );
  }

  return (
    <AuthShell>
      <Card className="w-full max-w-sm">
        {step === "qr" && (
          <>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <ShieldCheck
                  className="size-[18px] text-[color:var(--primary)]"
                  strokeWidth={2.2}
                />
                Configurar 2FA
              </CardTitle>
              <CardDescription>
                Escaneie o QR code abaixo com seu app autenticador (Google
                Authenticator, 1Password, Authy).
              </CardDescription>
            </CardHeader>
            <CardContent>
              {!totpURI ? (
                <form onSubmit={handleEnable} className="flex flex-col gap-4">
                  <p className="text-xs text-muted-foreground">
                    Confirme sua senha para iniciar o cadastro 2FA.
                  </p>
                  <div className="flex flex-col gap-2">
                    <label htmlFor="confirm-password" className="text-xs font-semibold">
                      Senha
                    </label>
                    <Input
                      id="confirm-password"
                      type="password"
                      autoComplete="current-password"
                      required
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                    />
                  </div>
                  {enrollError && (
                    <p className="text-xs text-destructive" role="alert">
                      {enrollError}
                    </p>
                  )}
                  <Button type="submit" disabled={enrolling}>
                    {enrolling ? "Gerando QR code…" : "Gerar QR code"}
                  </Button>
                </form>
              ) : (
                <div className="flex flex-col gap-4">
                  <div
                    className="self-center rounded-[10px] bg-white p-4"
                    style={{ width: 192 + 16 * 2, height: 192 + 16 * 2 }}
                  >
                    {qrDataUrl ? (
                      <img
                        src={qrDataUrl}
                        alt="QR code do segredo TOTP"
                        width={192}
                        height={192}
                      />
                    ) : (
                      <div className="size-[192px] animate-pulse bg-muted" />
                    )}
                  </div>
                  <div
                    className="self-center inline-flex items-center gap-2 rounded-md border font-mono text-xs font-semibold tabular-nums"
                    style={{
                      padding: "6px 10px",
                      letterSpacing: "0.08em",
                      borderColor: "var(--border-strong, var(--border))",
                    }}
                  >
                    <span>{secret}</span>
                    <button
                      type="button"
                      onClick={copySecret}
                      className="text-muted-foreground hover:text-foreground"
                      aria-label="Copiar segredo"
                    >
                      ⎘
                    </button>
                  </div>
                  <p className="text-[11px] text-muted-foreground text-center">
                    emissor: <span className="font-mono">Ifix AI Gateway</span> ·
                    algoritmo: SHA1 · 30s
                  </p>
                  <Button type="button" onClick={() => setStep("verify")}>
                    Já escaneei, continuar
                  </Button>
                </div>
              )}
            </CardContent>
          </>
        )}

        {step === "verify" && (
          <>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <ShieldCheck
                  className="size-[18px] text-[color:var(--primary)]"
                  strokeWidth={2.2}
                />
                Confirmar código
              </CardTitle>
              <CardDescription>
                Digite o código de 6 dígitos exibido no seu app autenticador
                para concluir o cadastro.
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
                  novo código em{" "}
                  <span className="tabular-nums">{countdown}s</span>
                </p>
                {verifyError && (
                  <p className="text-xs text-destructive text-center" role="alert">
                    {verifyError}
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
              </form>
            </CardContent>
          </>
        )}

        {step === "backup" && (
          <>
            <CardHeader>
              <CardTitle>Códigos de backup</CardTitle>
              <CardDescription>
                Guarde estes códigos em local seguro. Cada código funciona uma
                única vez para entrar caso você perca acesso ao app autenticador.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <div className="flex flex-col gap-4">
                <div
                  className="rounded-md border"
                  style={{
                    padding: "10px 12px",
                    background:
                      "color-mix(in oklch, var(--status-warning, oklch(0.769 0.188 70.08)) 12%, var(--card))",
                    borderColor:
                      "color-mix(in oklch, var(--status-warning, oklch(0.769 0.188 70.08)) 45%, transparent)",
                  }}
                  role="alert"
                >
                  <p className="text-xs">
                    Os códigos só são exibidos uma vez. Copie e salve antes de
                    continuar.
                  </p>
                </div>
                <ul
                  className="grid grid-cols-2"
                  style={{ gap: "6px" }}
                >
                  {backupCodes.map((c, i) => (
                    // biome-ignore lint/suspicious/noArrayIndexKey: fixed-size 10
                    <li
                      key={i}
                      className="flex items-center rounded-[5px] border bg-[color:var(--row-hover,var(--card))]"
                      style={{ padding: "5px 8px" }}
                    >
                      <span className="text-[11px] text-muted-foreground w-5 text-right tabular-nums">
                        {String(i + 1).padStart(2, "0")}
                      </span>
                      <span className="ml-2 font-mono text-xs font-semibold tabular-nums">
                        {c}
                      </span>
                    </li>
                  ))}
                </ul>
                <div className="flex gap-2">
                  <Button
                    type="button"
                    variant="secondary"
                    className="flex-1"
                    onClick={copyAllBackup}
                  >
                    Copiar tudo
                  </Button>
                  <Button
                    type="button"
                    className="flex-1"
                    onClick={() => {
                      router.push("/");
                      router.refresh();
                    }}
                  >
                    Salvar e continuar
                    <ArrowRight className="ml-2 size-[14px]" strokeWidth={2.2} />
                  </Button>
                </div>
              </div>
            </CardContent>
          </>
        )}
      </Card>
    </AuthShell>
  );
}
