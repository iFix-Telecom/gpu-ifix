import { resolve } from "node:path";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["./src/test-setup.ts"],
  },
  resolve: {
    alias: {
      "@": resolve(__dirname, "./src"),
      // `server-only` is a Next.js marker package (a dep of `next`, not hoisted
      // to the root node_modules) that throws when imported into a client
      // bundle. Under vitest there is no client/server split, so map it to
      // Next's own compiled no-op so `import "server-only"` in the server-only
      // helpers (gateway-admin.ts) resolves cleanly in tests.
      "server-only": resolve(
        __dirname,
        "./node_modules/next/dist/compiled/server-only/empty.js",
      ),
    },
  },
});
