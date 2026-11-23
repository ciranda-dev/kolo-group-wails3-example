import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import wails from "@wailsio/runtime/plugins/vite";
import { writeFileSync } from "node:fs";

// The build empties dist, and dist/.gitkeep is tracked (main.go embeds the folder): put it back.
const keepDist = {
  name: "keep-dist",
  apply: "build",
  closeBundle() {
    writeFileSync(new URL("./dist/.gitkeep", import.meta.url), "");
  },
};

// The page carries a content security policy for the built app. The dev server injects inline
// scripts and styles (hot reload), which that policy would block, so it serves the page without it.
const devWithoutPolicy = {
  name: "dev-without-policy",
  apply: "serve",
  transformIndexHtml: {
    order: "pre",
    handler: (html) => html.replace(/\s*<meta http-equiv="Content-Security-Policy"[^>]*>/, ""),
  },
};

export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  plugins: [react(), wails("./bindings"), keepDist, devWithoutPolicy],
});
