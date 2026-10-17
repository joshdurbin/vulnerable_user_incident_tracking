/// <reference types="vitest/config" />
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// In development the API runs on :8080 (collisionctl api); in the cluster the web
// container's nginx proxies /v1 to it, so the browser only ever talks to one origin.
export default defineConfig({
  plugins: [react()],
  // Pre-bundling moves maplibre-gl out of node_modules, which breaks the URL of its worker script.
  optimizeDeps: { exclude: ["maplibre-gl"] },
  server: { port: 5173, proxy: { "/v1": "http://localhost:8080" } },
  test: { environment: "jsdom", globals: true, setupFiles: ["./src/test-setup.ts"], css: false },
});
