/**
 * Better Auth catch-all route handler — mounts the auth API under
 * /api/auth/* (sign-in, sign-out, session, …).
 *
 * Source: 07-RESEARCH.md verbatim example (line 489-492).
 */
import { toNextJsHandler } from "better-auth/next-js";
import { auth } from "@/lib/auth";

export const { POST, GET } = toNextJsHandler(auth);
