import type { NextConfig } from "next";

/**
 * The Go control plane is the only server that owns state. Next.js is a thin
 * client: every `/v1/*` call is proxied so the browser sees a single origin,
 * which keeps the session cookie first-party and removes CORS from the dev
 * loop entirely.
 */
const BACKEND = process.env.PIFORGE_BACKEND ?? "http://127.0.0.1:8080";

const nextConfig: NextConfig = {
  cacheComponents: true,
  partialPrefetching: true,
  reactCompiler: true,
  turbopack: {
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
  async rewrites() {
    return [
      { source: "/v1/:path*", destination: `${BACKEND}/v1/:path*` },
      { source: "/healthz", destination: `${BACKEND}/healthz` },
    ];
  },
};

export default nextConfig;
