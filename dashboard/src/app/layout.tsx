import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Gateway ifix-ai — Observabilidade",
  description:
    "Painel operacional do ifix-ai-gateway — latência, erro, custo e estado de failover por tenant.",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  // Dark-only operator monitoring screen (UI-SPEC §Theme mode) — the `dark`
  // class is pinned on <html> so the radix-nova `.dark` token set is active.
  return (
    <html lang="pt-BR" className="dark">
      <body className="antialiased">{children}</body>
    </html>
  );
}
