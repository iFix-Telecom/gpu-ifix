"use client";

/**
 * Login page — email/password sign-in for the ~4 Ifix operators.
 *
 * Phase 11 extensions (UI-SPEC v2 §screens 3/4/5):
 *   - Pending state — spinner inside "Entrando…" disabled button (D-12)
 *   - Rate-limited state — Alert variant="destructive" + countdown (D-14)
 *   - Session-expired state — Alert variant="default" + Clock (D-15)
 *
 * The base form layout + copy from Phase 07 ("Gateway ifix-ai" title,
 * "E-mail ou senha inválidos." error) is preserved verbatim per UI-SPEC
 * §Inheritance Notes — Phase 11 only ADDS the Alerts above the form and
 * the spinner inside the button.
 *
 * Query-param contract (middleware → login):
 *   - `?session_expired=1` — set by middleware.ts when no session cookie
 *   - `?rate_limited=<retry-after-seconds>` — surfaced by future
 *     /api/auth/sign-in/email 429 handling; today the param is optional.
 */
import { Clock } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { signIn } from "@/lib/auth-client";

// Next.js 15.5 prerender requires useSearchParams callers to be wrapped in
// Suspense so the static export can defer the query-string read to runtime.
// Without this the `next build` Collecting-page-data step errors with
// "useSearchParams() should be wrapped in a suspense boundary at page /login"
// (CI: gh run 26568406942). The Suspense fallback renders an empty Card so
// hydration matches the eventual filled form layout.
export default function LoginPage() {
  return (
    <Suspense fallback={<LoginFallback />}>
      <LoginPageInner />
    </Suspense>
  );
}

function LoginFallback() {
  return (
    <main className="flex min-h-screen items-center justify-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Gateway ifix-ai</CardTitle>
          <CardDescription>
            Painel de observabilidade — acesso restrito à equipe de operações.
          </CardDescription>
        </CardHeader>
        <CardContent />
      </Card>
    </main>
  );
}

function LoginPageInner() {
  const router = useRouter();
  const params = useSearchParams();
  const sessionExpired = params.get("session_expired") === "1";
  const rateLimitedParam = params.get("rate_limited");
  const rateLimited = rateLimitedParam !== null && rateLimitedParam !== "0";
  const retryAfterSeconds = rateLimited
    ? Number.parseInt(rateLimitedParam ?? "0", 10) || 0
    : 0;

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);

    // better-auth twoFactorClient signal: with 2FA enabled the backend returns
    // 200 `{ twoFactorRedirect: true }` and only a temporary `two_factor` cookie
    // (no real session). The `onSuccess` callback exposes the narrowed union
    // (`context.data.twoFactorRedirect`); routing to the 2FA challenge here
    // avoids the push to "/", which the middleware would otherwise bounce to
    // /login?session_expired=1 since no session cookie exists yet
    // (QUICK-2FA-REDIRECT).
    let twoFactorRequired = false;
    const { error: signInError } = await signIn.email(
      { email, password },
      {
        onSuccess(context) {
          if (context.data?.twoFactorRedirect) {
            twoFactorRequired = true;
          }
        },
      },
    );

    setLoading(false);

    if (signInError) {
      setError(
        "E-mail ou senha inválidos. Verifique as credenciais e tente novamente.",
      );
      return;
    }

    if (twoFactorRequired) {
      router.push("/2fa/challenge");
      return;
    }

    router.push("/");
    router.refresh();
  }

  // Render the rate-limit Alert with a tabular-numerals countdown line —
  // the value is reported by the upstream 429 (Retry-After).
  const rateLimitCopy = retryAfterSeconds > 0
    ? `Muitas tentativas. Aguarde ${retryAfterSeconds}s antes de tentar novamente. Limite: 5 tentativas a cada 15 min por IP.`
    : "Muitas tentativas. Aguarde antes de tentar novamente. Limite: 5 tentativas a cada 15 min por IP.";

  return (
    <main className="flex min-h-screen items-center justify-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Gateway ifix-ai</CardTitle>
          <CardDescription>
            Painel de observabilidade — acesso restrito à equipe de operações.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {rateLimited && (
            <Alert variant="destructive" className="mb-4" role="alert">
              <AlertDescription>
                <span className="font-semibold">Muitas tentativas.</span>{" "}
                <span className="tabular-nums">{rateLimitCopy}</span>
              </AlertDescription>
            </Alert>
          )}
          {sessionExpired && !rateLimited && (
            <Alert variant="default" className="mb-4" role="status">
              <Clock className="size-4 text-muted-foreground" aria-hidden />
              <AlertDescription>
                <span className="font-semibold">
                  Sessão encerrada por inatividade.
                </span>{" "}
                Faça login novamente. Sessões expiram após 30 min sem atividade.
              </AlertDescription>
            </Alert>
          )}
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <label htmlFor="email" className="text-xs font-semibold">
                E-mail
              </label>
              <Input
                id="email"
                type="email"
                autoComplete="email"
                required
                disabled={rateLimited}
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-2">
              <label htmlFor="password" className="text-xs font-semibold">
                Senha
              </label>
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                required
                disabled={rateLimited}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            {error && (
              <p className="text-xs text-destructive" role="alert">
                {error}
              </p>
            )}
            <Button type="submit" disabled={loading || rateLimited}>
              {loading ? (
                <span className="inline-flex items-center gap-2">
                  <span
                    aria-hidden
                    className="inline-block size-3.5 animate-spin rounded-full border-2 border-current border-t-transparent"
                  />
                  Entrando…
                </span>
              ) : (
                "Entrar"
              )}
            </Button>
          </form>
        </CardContent>
      </Card>
    </main>
  );
}
