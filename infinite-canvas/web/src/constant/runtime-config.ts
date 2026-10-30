// Runtime configuration access layer.
// Priority: window.__RUNTIME_CONFIG__ (injected by the container entrypoint) > build-time VITE_ variables > defaults.
// This supports both configuring the same image with docker run -e and injecting values during custom builds.
//
// The two analytics IDs that used to live here (GA4, Baidu) were deleted with the
// injector that was supposed to read them: nothing imported them, no script was ever
// built, and no documentation or .env entry mentioned them.

type RuntimeConfig = {
    LOCAL_GATEWAY_URL?: string; // e.g. http://127.0.0.1:8190/v1
    LOCAL_GATEWAY_HOST?: string; // host when URL is not given
    LOCAL_GATEWAY_PORT?: string; // port when URL is not given
};

declare global {
    interface Window {
        __RUNTIME_CONFIG__?: RuntimeConfig;
    }
}

const runtime: RuntimeConfig = (typeof window !== "undefined" && window.__RUNTIME_CONFIG__) || {};

function read(key: keyof RuntimeConfig, buildTime: string | undefined, fallback = ""): string {
    const value = runtime[key];
    if (typeof value === "string" && value.trim()) return value.trim();
    if (typeof buildTime === "string" && buildTime.trim()) return buildTime.trim();
    return fallback;
}

export const RUNTIME_LOCAL_GATEWAY_URL = read("LOCAL_GATEWAY_URL", undefined, "");
export const RUNTIME_LOCAL_GATEWAY_HOST = read("LOCAL_GATEWAY_HOST", undefined, "");
export const RUNTIME_LOCAL_GATEWAY_PORT = read("LOCAL_GATEWAY_PORT", undefined, "");
