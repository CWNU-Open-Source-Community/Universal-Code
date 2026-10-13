import react from "@vitejs/plugin-react";
import { loadEnv } from "vite";
import { defineConfig } from "vitest/config";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, ".", "");
  const apiTarget = env.CYBERAGENT_API_TARGET || "http://127.0.0.1:8765";
  const targetURL = new URL(apiTarget);
  if (!["127.0.0.1", "localhost", "[::1]"].includes(targetURL.hostname) ||
    !["http:", "https:"].includes(targetURL.protocol)) {
    throw new Error("CYBERAGENT_API_TARGET must be an HTTP(S) loopback URL");
  }
  const proxy = {
    "/api": {
      target: apiTarget,
      changeOrigin: true,
      secure: false,
    },
  };

  return {
    plugins: [react()],
    server: {
      host: "127.0.0.1",
      port: 5173,
      strictPort: false,
      proxy,
    },
    preview: {
      host: "127.0.0.1",
      port: 4173,
      strictPort: false,
      proxy,
    },
    build: {
      assetsInlineLimit: 0,
      outDir: "dist",
      sourcemap: false,
    },
    test: {
      include: ["src/**/*.{test,spec}.{ts,tsx}"],
      environment: "jsdom",
      globals: true,
      setupFiles: "./src/test/setup.ts",
      restoreMocks: true,
    },
  };
});
