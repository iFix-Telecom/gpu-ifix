import type { Metadata } from "next";
import { Inter, JetBrains_Mono, Space_Grotesk } from "next/font/google";
import "./globals.css";

/*
 * The three faces of the approved redesign, loaded through `next/font/google`.
 *
 * `next/font` downloads and SELF-HOSTS the files at build time — there is no
 * runtime request to fonts.googleapis.com and no extra npm package. Each font
 * exposes a CSS variable that globals.css maps onto a Tailwind family token
 * (`--font-sans` / `--font-display` / `--font-mono`).
 */

/** Body / UI text. */
const inter = Inter({
  subsets: ["latin"],
  weight: ["400", "500", "600"],
  variable: "--font-inter",
  display: "swap",
});

/** Headings + KPI display values. */
const spaceGrotesk = Space_Grotesk({
  subsets: ["latin"],
  weight: ["500", "600", "700"],
  variable: "--font-space-grotesk",
  display: "swap",
});

/** Every raw number, id and route — fixed advance width keeps columns aligned. */
const jetbrainsMono = JetBrains_Mono({
  subsets: ["latin"],
  weight: ["500", "600"],
  variable: "--font-jetbrains-mono",
  display: "swap",
});

export const metadata: Metadata = {
  title: "Gateway ifix-ai — Observabilidade",
  description:
    "Painel operacional do ifix-ai-gateway — latência, erro, custo e estado de failover por tenant.",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  // Dark-only operator monitoring screen (UI-SPEC §Theme mode) — the `dark`
  // class is pinned on <html> so the redesign `.dark` token set is active.
  return (
    <html
      lang="pt-BR"
      className={`dark ${inter.variable} ${spaceGrotesk.variable} ${jetbrainsMono.variable}`}
    >
      <body className="antialiased">{children}</body>
    </html>
  );
}
