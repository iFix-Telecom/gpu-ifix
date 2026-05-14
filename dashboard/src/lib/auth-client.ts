/**
 * Better Auth browser client for the dashboard.
 *
 * No plugins — mirrors the server `auth` instance which enables only
 * `emailAndPassword`. The login page calls `authClient.signIn.email(...)`.
 */
import { createAuthClient } from "better-auth/react";

export const authClient = createAuthClient();

export const { signIn, signOut, signUp, useSession } = authClient;
