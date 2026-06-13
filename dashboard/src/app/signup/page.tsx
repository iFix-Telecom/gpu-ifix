"use client";

/**
 * /signup — Self-register form gated by D-13 email allowlist (UI-SPEC v2
 * §screen 6).
 *
 * The Better Auth `databaseHooks.user.create.before` enforces the
 * allowlist server-side (see lib/auth.ts). Non-`@ifixtelecom.com.br`
 * domains throw a hook error which surfaces here as a destructive Alert
 * with red e-mail input border. Self-register for external users is
 * effectively closed (D-13).
 */
import { CircleAlert } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { AuthShell } from "@/components/auth/auth-shell";
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
import { signUp } from "@/lib/auth-client";

export default function SignupPage() {
  const router = useRouter();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [allowlistRejected, setAllowlistRejected] = useState(false);
  const [genericError, setGenericError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setAllowlistRejected(false);
    setGenericError(null);
    setLoading(true);
    try {
      const { error: signUpError } = await signUp.email({
        name,
        email,
        password,
      });
      setLoading(false);
      if (signUpError) {
        // The allowlist hook surfaces as a Better Auth error containing
        // "allowlist" or "ifixtelecom" or the generic "failed to create user"
        // depending on version. Treat any of those as a rejection.
        const msg = (signUpError.message ?? "").toLowerCase();
        if (
          msg.includes("allowlist") ||
          msg.includes("ifixtelecom") ||
          msg.includes("failed to create user")
        ) {
          setAllowlistRejected(true);
          return;
        }
        setGenericError(
          "Não foi possível criar a conta agora. Tente novamente em alguns segundos.",
        );
        return;
      }
      // On success (autoSignIn=false), redirect to /login.
      router.push("/login");
    } catch (_e) {
      setLoading(false);
      setGenericError(
        "Não foi possível criar a conta agora. Tente novamente em alguns segundos.",
      );
    }
  }

  return (
    <AuthShell>
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Solicitar acesso</CardTitle>
          <CardDescription>
            Cadastro restrito à equipe de operações Ifix.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {allowlistRejected && (
            <Alert variant="destructive" className="mb-4" role="alert">
              <CircleAlert className="size-4" aria-hidden />
              <AlertDescription>
                <span className="font-semibold">
                  Cadastro restrito a contas{" "}
                  <span className="font-mono">@ifixtelecom.com.br</span>.
                </span>{" "}
                Solicite acesso à equipe de operações para criar uma conta
                interna.
              </AlertDescription>
            </Alert>
          )}
          {genericError && (
            <Alert variant="destructive" className="mb-4" role="alert">
              <AlertDescription>{genericError}</AlertDescription>
            </Alert>
          )}
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <label htmlFor="name" className="text-xs font-semibold">
                Nome
              </label>
              <Input
                id="name"
                type="text"
                autoComplete="name"
                required
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-2">
              <label htmlFor="email" className="text-xs font-semibold">
                E-mail
              </label>
              <Input
                id="email"
                type="email"
                autoComplete="email"
                required
                value={email}
                onChange={(e) => {
                  setEmail(e.target.value);
                  if (allowlistRejected) setAllowlistRejected(false);
                }}
                style={
                  allowlistRejected
                    ? { borderColor: "var(--destructive)" }
                    : undefined
                }
              />
            </div>
            <div className="flex flex-col gap-2">
              <label htmlFor="password" className="text-xs font-semibold">
                Senha
              </label>
              <Input
                id="password"
                type="password"
                autoComplete="new-password"
                required
                minLength={8}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            <Button type="submit" disabled={loading}>
              {loading ? "Criando conta…" : "Criar conta"}
            </Button>
            <Link
              href="/login"
              className="text-xs text-muted-foreground hover:text-foreground transition-colors text-center"
            >
              Já tenho uma conta — entrar
            </Link>
          </form>
        </CardContent>
      </Card>
    </AuthShell>
  );
}
