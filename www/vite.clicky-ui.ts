import path from "node:path";
import fs from "node:fs";

// The local clicky-ui checkout, the sibling every Flanksource repo links against.
export const clickyCheckoutRoot = path.resolve(
  process.env.HOME || "",
  "go/src/github.com/flanksource/clicky-ui",
);
const clickySourceRoot = path.resolve(clickyCheckoutRoot, "packages/ui/src");

// Alias @flanksource/clicky-ui straight to the local checkout's source for local
// development, tests, and builds, so an unreleased component or prop is the same
// code on every local surface. SQLGLOT_LOCAL_UI=0 selects the pinned package for
// the explicit published build; CI defaults to that package unless a task
// explicitly requests the local checkout with SQLGLOT_LOCAL_UI=1. A missing
// requested checkout is an error rather than a quiet build against different code.
export function useClickySource(): boolean {
  const localOverride = process.env.SQLGLOT_LOCAL_UI;
  if (localOverride === "0" || (localOverride !== "1" && process.env.CI === "true")) {
    return false;
  }
  if (!fs.existsSync(clickySourceRoot)) {
    throw new Error(
      `local clicky-ui workspace not found at ${clickyCheckoutRoot}; ` +
        "set SQLGLOT_LOCAL_UI=0 to resolve the published @flanksource/clicky-ui instead",
    );
  }
  return true;
}

// Runtime exports declared in clicky-ui's package.json. Keep the longer nested
// entry before its parent so every public import resolves to local source.
const CLICKY_SOURCE_ENTRIES = {
  "monaco/schema": "monaco-schema.ts",
  "expressions/playground": "expressions/playground.ts",
  expressions: "expressions.ts",
  components: "components.ts",
  "mdx-editor": "mdx-editor.ts",
  comments: "comments.ts",
  connections: "connections.ts",
  devtools: "devtools.ts",
  profiles: "profiles.ts",
  "tailwind-preset": "tailwind-preset.ts",
  clicky: "clicky.ts",
  monaco: "monaco.ts",
  icons: "icons.ts",
  hooks: "hooks.ts",
  utils: "utils.ts",
  data: "data.ts",
  jotai: "jotai.ts",
  rpc: "rpc.ts",
  chat: "chat.ts",
  ai: "ai.ts",
} as const;

// clicky-ui source aliases must precede any bare-package fallbacks so the longer
// (subpath) matches win.
export function clickySourceAliases() {
  return [
    {
      find: "@flanksource/clicky-ui/styles.css",
      replacement: path.resolve(clickySourceRoot, "styles/full.css"),
    },
    {
      find: "@flanksource/clicky-ui/mdx-editor.css",
      replacement: path.resolve(clickySourceRoot, "styles/mdx-editor.css"),
    },
    ...Object.entries(CLICKY_SOURCE_ENTRIES).map(([name, entry]) => ({
      find: `@flanksource/clicky-ui/${name}`,
      replacement: path.resolve(clickySourceRoot, entry),
    })),
    {
      find: "@flanksource/clicky-ui",
      replacement: path.resolve(clickySourceRoot, "index.ts"),
    },
  ];
}
