import { LOCAL_GATEWAY_BASE_URL } from "@/constant/app-mode";
import { noteGatewayError } from "@/services/api/gateway-status";

const HEARTBEAT_MS = 5000;
let started = false;

function sessionUrl(path: string) {
    return `${LOCAL_GATEWAY_BASE_URL}${path}`;
}

// Register the canvas session with the gateway so the launcher can reclaim the backend once the last page closes.
export function startLocalSession() {
    if (started || typeof window === "undefined") return;
    started = true;
    const id = typeof crypto.randomUUID === "function" ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(16).slice(2)}`;
    let timer: number | undefined;
    let active = false;
    let hiddenSince = document.visibilityState === "hidden" ? Date.now() : 0;

    const post = (path: string, keepalive = false, report = true) =>
        fetch(sessionUrl(path), {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ id }),
            keepalive,
        }).catch((error) => {
            if (report) noteGatewayError(error, sessionUrl(path));
            return undefined;
        });

    const open = () => {
        if (active) return;
        active = true;
        void post("/api/session/open");
        timer = window.setInterval(() => void post("/api/session/heartbeat"), HEARTBEAT_MS);
    };

    // Browser sleep or discarding a background tab also fires pagehide; a page hidden for more
    // than 60s is not treated as closed, or idling would kill the backend by mistake.
    const close = (event?: PageTransitionEvent) => {
        if (!active) return;
        if (event?.persisted) return;
        if (hiddenSince && Date.now() - hiddenSince > 60000) return;
        active = false;
        if (timer !== undefined) window.clearInterval(timer);
        timer = undefined;
        void post("/api/session/close", true, false);
    };

    document.addEventListener("visibilitychange", () => {
        hiddenSince = document.visibilityState === "hidden" ? Date.now() : 0;
    });
    window.addEventListener("pagehide", close);
    window.addEventListener("pageshow", (event) => {
        if (event.persisted) open();
    });
    open();
}
