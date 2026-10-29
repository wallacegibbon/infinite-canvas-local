import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

const webDir = dirname(fileURLToPath(import.meta.url));
const appRoot = resolve(webDir, "../..");
// 版本号可选：没有 VERSION 文件时用 dev，不影响构建。
const versionPath = [resolve(appRoot, "VERSION"), resolve(webDir, "../VERSION")].find((file) => existsSync(file));
const localVersion = versionPath ? readFileSync(versionPath, "utf8").trim() || "dev" : "dev";

// Expose /plugins/index.json with local plugin files from public/plugins.
// The frontend can discover and list them when enabled; development reads the directory live, while builds emit a static registry.
function localPluginsManifest(): Plugin {
    const pluginsDir = resolve(webDir, "public/plugins");
    const listLocalPlugins = () => {
        try {
            return readdirSync(pluginsDir)
                .filter((file) => file.endsWith(".js"))
                .sort()
                .map((file) => `/plugins/${file}`);
        } catch {
            return [];
        }
    };
    return {
        name: "local-plugins-manifest",
        configureServer(server) {
            server.middlewares.use("/plugins/index.json", (_req, res) => {
                res.setHeader("Content-Type", "application/json");
                res.end(JSON.stringify(listLocalPlugins()));
            });
        },
        generateBundle() {
            this.emitFile({ type: "asset", fileName: "plugins/index.json", source: JSON.stringify(listLocalPlugins()) });
        },
    };
}

// Vendor code is split by library family so the ~3.5 MB app entry does not have to be
// re-downloaded when only app code changes, and no single chunk dwarfs the rest. The
// entries are only a cache boundary: rollup keeps the execution order of side effects.
function vendorChunk(id: string): string | undefined {
    if (!id.includes("node_modules")) return undefined;
    if (/[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/.test(id)) return "react";
    if (/[\\/]node_modules[\\/](antd|@ant-design|@rc-component|rc-.+|dayjs)[\\/]/.test(id)) return "antd";
    if (/[\\/]node_modules[\\/]three[\\/]/.test(id)) return "three";
    return "vendor";
}

export default defineConfig({
    base: process.env.VITE_BASE || "/",
    plugins: [react(), localPluginsManifest()],
    resolve: {
        alias: {
            "@": resolve(webDir, "src"),
        },
    },
    define: {
        __APP_VERSION__: JSON.stringify(localVersion),
    },
    build: {
        // The bundle is served from the same machine, so bytes on the wire are cheap;
        // the split above exists so a change to app code does not invalidate the
        // vendor chunks. The default 500 kB threshold is therefore noise here, but it
        // is kept as a guard against the bundle quietly doubling.
        chunkSizeWarningLimit: 1200,
        rollupOptions: {
            output: {
                manualChunks: vendorChunk,
            },
        },
    },
});
