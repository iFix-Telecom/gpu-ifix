import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Standalone output: the Dockerfile copies .next/standalone for a slim
  // runtime image (mirrors the converseai-v4 web Dockerfile pattern).
  output: "standalone",
};

export default nextConfig;
