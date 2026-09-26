FROM oven/bun:1.3-slim
WORKDIR /app
COPY audio.ts server.ts ./
USER bun
CMD ["bun", "run", "server.ts"]
