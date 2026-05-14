# syntax=docker/dockerfile:1.7
#
# dashboard/Dockerfile
# Multi-stage Next.js 15 standalone build for the ifix-ai-dashboard service.
#
# Mirrors the converseai-v4 apps/web Dockerfile pattern (Next.js standalone
# output → slim node:20-alpine runtime), simplified for a single non-monorepo
# npm app.
#
# Build context is the `dashboard/` directory itself (see build-dashboard.yml).

# =========================================================================
# Stage 1: deps — install node_modules from the lockfile only
# =========================================================================
FROM node:20-alpine AS deps
RUN apk add --no-cache libc6-compat
WORKDIR /app

# .npmrc carries legacy-peer-deps (better-auth's optional @sveltejs/kit peer
# collides with @vitejs/plugin-react's vite peer). Copy it so `npm ci`
# resolves the same way local dev does.
COPY package.json package-lock.json .npmrc ./
RUN npm ci

# =========================================================================
# Stage 2: builder — compile the Next.js standalone bundle
# =========================================================================
FROM node:20-alpine AS builder
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1

COPY --from=deps /app/node_modules ./node_modules
COPY . .

# next.config.ts sets `output: "standalone"` — produces .next/standalone with
# a minimal server.js + only the node_modules it actually needs.
RUN npm run build

# =========================================================================
# Stage 3: runtime — slim node:20-alpine, non-root, standalone server only
# =========================================================================
FROM node:20-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1
ENV PORT=3001
ENV HOSTNAME="0.0.0.0"

RUN addgroup --system --gid 1001 nodejs \
 && adduser --system --uid 1001 nextjs

# Standalone output: server.js + traced node_modules at the app root,
# static assets under .next/static, the public/ tree as-is.
COPY --from=builder /app/.next/standalone ./
COPY --from=builder /app/.next/static ./.next/static
COPY --from=builder /app/public ./public

USER nextjs
EXPOSE 3001

CMD ["node", "server.js"]
