import { resolve } from "node:path";
import { defineConfig } from "vitest/config";

// Kept separate from `vite.config.ts` on purpose: the app build pulls in the React
// plugin and the local-plugins manifest generator, neither of which the unit tests
// need. These tests are pure functions, so `node` is the right environment.
export default defineConfig({
    resolve: {
        alias: { "@": resolve(import.meta.dirname, "src") },
    },
    test: {
        include: ["tests/**/*.test.ts"],
    },
});
