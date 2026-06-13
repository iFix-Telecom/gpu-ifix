/**
 * /signed-out — Logout landing card (UI-SPEC v2 §screen 15).
 *
 * Server component (no client hooks needed). Shown after Better Auth
 * signOut completes; provides reassurance that the session cookies were
 * cleared and offers a single primary CTA back to /login plus an
 * external link to the operator runbook.
 */
import { CircleCheck } from "lucide-react";
import Link from "next/link";
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

export default function SignedOutPage() {
  return (
    <AuthShell>
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Sessão encerrada</CardTitle>
          <CardDescription>
            Você saiu do painel de observabilidade Ifix. Faça login novamente
            para retomar a operação.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex flex-col gap-4">
            <Alert variant="default" role="status">
              <CircleCheck
                className="size-4 text-[color:var(--primary)]"
                aria-hidden
              />
              <AlertDescription>
                Logout concluído com sucesso. Cookies de sessão removidos.
              </AlertDescription>
            </Alert>
            <Button asChild>
              <Link href="/login">Entrar novamente</Link>
            </Button>
            <a
              href="https://github.com/IfixTelecom/ifix-ai-gateway/blob/main/gateway/docs/RUNBOOK-INCIDENTS.md"
              target="_blank"
              rel="noreferrer noopener"
              className="text-xs text-muted-foreground hover:text-foreground transition-colors text-center"
            >
              Abrir runbook de operações
            </a>
          </div>
        </CardContent>
      </Card>
    </AuthShell>
  );
}
