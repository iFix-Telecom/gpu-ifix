/**
 * Better Auth browser client for the dashboard.
 *
 * Phase 11 (PRD-06): registers the `twoFactorClient()` plugin so the UI
 * (`app/2fa/enroll`, `app/2fa/challenge`, `app/2fa/backup`) can call
 * `authClient.twoFactor.enable(...)`, `authClient.twoFactor.verifyTotp(...)`,
 * and `authClient.twoFactor.verifyBackupCode(...)`. Server-side counterpart
 * is `twoFactor({ issuer: "Ifix AI Gateway" })` in `auth.ts`.
 */
import { twoFactorClient } from "better-auth/client/plugins";
import { createAuthClient } from "better-auth/react";

export const authClient = createAuthClient({
  plugins: [twoFactorClient()],
});

export const { signIn, signOut, signUp, useSession } = authClient;
