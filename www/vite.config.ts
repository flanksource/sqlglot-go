/// <reference types="vitest/config" />
import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { mockApiPlugin } from "./mock/plugin.ts";
import { clickyCheckoutRoot, clickySourceAliases, useClickySource } from "./vite.clicky-ui.ts";

const wwwRoot = import.meta.dirname;
const useMock = process.env.MOCK === "true";
// The sqlglot ui server (cmd/sqlglot/ui.go) listens here by default. `sqlglot ui --dev`
// sets SQLGLOT_API_URL and SQLGLOT_DEV_PORT (a free port it picked), and serves this dev
// server through its own address. Without it, the OS assigns a random port (0).
const apiTarget = process.env.SQLGLOT_API_URL ?? "http://127.0.0.1:9150";
const devPort = process.env.SQLGLOT_DEV_PORT ? Number(process.env.SQLGLOT_DEV_PORT) : 0;
if (!Number.isInteger(devPort)) {
  throw new Error(`SQLGLOT_DEV_PORT must be an integer port, received ${process.env.SQLGLOT_DEV_PORT}`);
}

// clicky-ui's runtime dependencies that call React. Resolved through the local
// checkout, vitest hands them to Node, which binds each to the checkout's own
// React and the first hook one calls under www's React 19 fails with a null
// dispatcher. Inlining them routes their `react` import through the dedupe below,
// as the dev server's pre-bundling already does.
const CLICKY_REACT_DEPENDENCIES = [
  "@tanstack/react-virtual",
  "@radix-ui/react-slot",
  "@radix-ui/react-compose-refs",
  "@monaco-editor/react",
  "jotai",
  "@flanksource/icons",
  "@floating-ui/react",
  "@floating-ui/react-dom",
];

const backend = { target: apiTarget, changeOrigin: true, xfwd: true };

export default defineConfig(() => {
  const clickySource = useClickySource();
  return {
    plugins: [tailwindcss(), react(), ...(useMock ? [mockApiPlugin()] : [])],
    build: { outDir: "dist", emptyOutDir: true },
    resolve: {
      alias: [
        { find: "monaco-editor", replacement: path.resolve(wwwRoot, "node_modules/monaco-editor") },
        // Active unless the published package was asked for; precedes every other alias.
        ...(clickySource ? clickySourceAliases() : []),
        { find: /^react$/, replacement: path.resolve(wwwRoot, "node_modules/react") },
        { find: /^react\/jsx-runtime$/, replacement: path.resolve(wwwRoot, "node_modules/react/jsx-runtime.js") },
        { find: /^react\/jsx-dev-runtime$/, replacement: path.resolve(wwwRoot, "node_modules/react/jsx-dev-runtime.js") },
        { find: /^react-dom$/, replacement: path.resolve(wwwRoot, "node_modules/react-dom") },
        { find: /^@tanstack\/react-query$/, replacement: path.resolve(wwwRoot, "node_modules/@tanstack/react-query") },
      ],
      dedupe: ["react", "react-dom", "@tanstack/react-query"],
    },
    optimizeDeps: {
      entries: ["index.html"],
      // Local source stays live; the published package needs prebundling for its
      // transitive CommonJS dependencies.
      exclude: clickySource ? ["@flanksource/clicky-ui"] : [],
    },
    server: {
      // Serving clicky-ui straight from its source checkout needs its workspace
      // root allowed, because package dependencies live in the root node_modules.
      ...(clickySource && { fs: { allow: [wwwRoot, clickyCheckoutRoot] } }),
      ...(!useMock && { proxy: { "/api": backend, "/health": backend } }),
      host: "127.0.0.1",
      port: devPort,
      strictPort: true,
    },
    test: {
      environment: "jsdom",
      include: ["src/**/*.test.{ts,tsx}"],
      server: { deps: { inline: ["@flanksource/clicky-ui", ...(clickySource ? CLICKY_REACT_DEPENDENCIES : [])] } },
    },
  };
});
