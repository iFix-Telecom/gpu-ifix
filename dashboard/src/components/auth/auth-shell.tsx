"use client";

/**
 * AuthShell — centered-card wrapper for every auth-route page.
 *
 * UI-SPEC v2 §Visual Hierarchy: every auth screen is a single-task focus
 * surface — one LoginCard centered in the viewport, with a 40×40 Zap-icon
 * header above and a centered footer line below (Ifix · AI Gateway ·
 * ai-dashboard.converse-ai.app).
 *
 * Layout tokens (UI-SPEC v2 §Spacing Scale):
 *   - outer: flex min-h-screen flex-col items-center justify-center
 *   - gap-4 (16px) between logo + card + footer
 *   - p-6 (24px) viewport padding
 *   - 40×40 logo, 10px borderRadius, primary-tinted bg, 1px primary border@40%
 *
 * Used by: app/login (existing), app/signup, app/first-login, app/2fa/*,
 * app/signed-out.
 */
import { Zap } from "lucide-react";
import type { ReactElement, ReactNode } from "react";

export interface AuthShellProps {
  children: ReactNode;
}

export function AuthShell({ children }: AuthShellProps): ReactElement {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center gap-4 p-6 bg-background">
      {/* 40×40 Zap logo (UI-SPEC §Layout Constraints) — primary-tinted */}
      <div
        aria-hidden
        className="flex h-10 w-10 items-center justify-center rounded-[10px] border"
        style={{
          background: "color-mix(in oklch, var(--primary) 22%, var(--card))",
          borderColor: "color-mix(in oklch, var(--primary) 40%, transparent)",
        }}
      >
        <Zap
          className="size-5 text-[color:var(--primary)]"
          strokeWidth={2.4}
        />
      </div>

      {children}

      <p className="mt-4 text-xs text-muted-foreground">
        Ifix · AI Gateway · ai-dashboard.converse-ai.app
      </p>
    </main>
  );
}
