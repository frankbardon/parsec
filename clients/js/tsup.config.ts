import { defineConfig } from "tsup";

export default defineConfig({
  entry: {
    index: "src/index.ts",
    channels: "src/channels.ts",
    scopes: "src/scopes.ts",
  },
  format: ["esm", "cjs"],
  dts: true,
  splitting: false,
  sourcemap: true,
  clean: true,
  target: "es2022",
  treeshake: true,
  outDir: "dist",
});
