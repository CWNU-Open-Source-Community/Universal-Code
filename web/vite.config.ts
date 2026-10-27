import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { loadEnv, normalizePath, type Plugin } from "vite";
import { defineConfig } from "vitest/config";

// npm overrides cannot replace Monaco's embedded copy. Redirect its sanitizer import
// in both Vite and the separate dependency prebundler to the installed DOMPurify.
const monacoSanitizer = normalizePath(fileURLToPath(import.meta.resolve("monaco-editor/base/browser/domSanitize.js")));
const monacoDOMPurify: Plugin = {
  name: "monaco-dompurify",
  enforce: "pre",
  async resolveId(source, importer) {
    if (source === "./dompurify/dompurify.js" && importer && normalizePath(importer) === monacoSanitizer) {
      const resolved = await this.resolve("dompurify", importer, { skipSelf: true });
      if (!resolved) throw new Error("Cannot resolve installed DOMPurify for the Monaco sanitizer");
      return resolved;
    }
  },
};

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
    plugins: [react(), monacoDOMPurify],
    optimizeDeps: {
      rolldownOptions: { plugins: [monacoDOMPurify] },
    },
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
      server: { deps: { inline: ["monaco-editor"] } },
      environment: "jsdom",
      globals: true,
      setupFiles: "./src/test/setup.ts",
      restoreMocks: true,
    },
  };
});
