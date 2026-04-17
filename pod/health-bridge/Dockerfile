# syntax=docker/dockerfile:1.7
#
# pod/health-bridge/Dockerfile
# Distroless static Go binary for the in-pod health-bridge service.
#
# Refs:
#   - .planning/phases/01-gpu-pod-image-smoke-test/01-04-PLAN.md Task 3
#   - .planning/phases/01-gpu-pod-image-smoke-test/01-PATTERNS.md
#       §pod/health-bridge/Dockerfile
#   - pod/docker-compose.yml health-bridge service (plan 01-03)
#       healthcheck: ["CMD", "/health-bridge", "--self-check"]

# =========================================================================
# Stage 1: builder — Go toolchain + source
# =========================================================================
FROM golang:1.23-alpine AS builder
WORKDIR /build

# Install git/ca-certs (needed for `go mod download` if deps are remote)
RUN apk add --no-cache ca-certificates git

# Dependency layer cache: copy go.mod/go.sum first.
# go.sum is optional in Phase 1 (stdlib-only; pkg/openai has no transitive deps).
COPY go.mod go.sum* ./
RUN go mod download || true

# Source — copy ONLY what we need for this binary (shared pkg/openai +
# the health-bridge source tree).
COPY pkg/openai ./pkg/openai
COPY pod/health-bridge ./pod/health-bridge

# Static build — distroless runtime has no libc.
# -trimpath strips build-machine paths from stack traces.
# -ldflags="-s -w" removes the symbol/DWARF tables for a ~30% smaller binary.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /health-bridge \
    ./pod/health-bridge

# =========================================================================
# Stage 2: runtime — distroless static (no shell, no libc, ~2 MB base)
# =========================================================================
FROM gcr.io/distroless/static-debian12 AS runtime

# Timezone data: copy zoneinfo from the builder so TZ=America/Sao_Paulo
# resolves at runtime (Ifix convention).
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
ENV TZ=America/Sao_Paulo

COPY --from=builder /health-bridge /health-bridge

EXPOSE 9100
STOPSIGNAL SIGTERM
ENTRYPOINT ["/health-bridge"]
