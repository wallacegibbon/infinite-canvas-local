import { RUNTIME_LOCAL_GATEWAY_HOST, RUNTIME_LOCAL_GATEWAY_PORT, RUNTIME_LOCAL_GATEWAY_URL } from "./runtime-config";

function runtimeGatewayBaseUrl() {
    if (RUNTIME_LOCAL_GATEWAY_URL) return RUNTIME_LOCAL_GATEWAY_URL.replace(/\/+$/, "").endsWith("/v1") ? RUNTIME_LOCAL_GATEWAY_URL.replace(/\/+$/, "") : `${RUNTIME_LOCAL_GATEWAY_URL.replace(/\/+$/, "")}/v1`;
    const host = RUNTIME_LOCAL_GATEWAY_HOST || pageHostname() || "127.0.0.1";
    const port = RUNTIME_LOCAL_GATEWAY_PORT || "8190";
    return `http://${host}:${port}/v1`;
}

// The gateway listens on the machine that serves this bundle, so the page's own hostname is
// the right address for it: a canvas opened as http://192.168.3.232:3000 must reach
// http://192.168.3.232:8190, not the loopback of whatever device holds the browser. Only a
// non-browser context (and a wildcard bind, which start.sh cannot turn into a URL) falls back
// to 127.0.0.1.
function pageHostname(): string {
    if (typeof window === "undefined") return "";
    return window.location?.hostname || "";
}

export const LOCAL_GATEWAY_BASE_URL = import.meta.env.VITE_LOCAL_GATEWAY_URL || runtimeGatewayBaseUrl();
export const LOCAL_GATEWAY_ORIGIN = (() => {
    try {
        return new URL(LOCAL_GATEWAY_BASE_URL).origin;
    } catch {
        return "";
    }
})();
export const LOCAL_GATEWAY_CHANNEL_ID = "local-gateway";

// There is no hardcoded model list. A model is a workflow, and the gateway reports which ones it has
// (`GET /api/workflows`); a list in the bundle drifted from the registry every time a workflow was
// added or removed — it still named two that no longer exist. The channel starts empty and the sync
// fills it (hooks/use-local-gateway-sync.ts).

export function isLocalGatewayUrl(url: string) {
    try {
        const parsed = new URL(String(url || ""));
        if (LOCAL_GATEWAY_ORIGIN && parsed.origin === LOCAL_GATEWAY_ORIGIN) return true;
        return ["127.0.0.1", "localhost", "::1", "[::1]"].includes(parsed.hostname);
    } catch {
        return false;
    }
}
