/**
 * Dashboard home — placeholder skeleton.
 *
 * Plan 07-07 delivers the app skeleton only; the real Overview / Tenants /
 * Incident History views are built in 07-08 against the contracts this plan
 * fixes (auth boundary, the gateway-fetch wrappers, the design tokens).
 */
export default function HomePage() {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center gap-4 p-12">
      <h1 className="text-xl font-semibold">Gateway ifix-ai</h1>
      <p className="text-sm text-muted-foreground">
        Painel de observabilidade — em construção (07-08).
      </p>
    </main>
  );
}
