FROM oven/bun:1.3-slim
WORKDIR /app
COPY package.json bun.lock ./
RUN bun install --production --frozen-lockfile
COPY server.ts ./
USER bun
EXPOSE 8099
CMD ["bun", "run", "server.ts"]
