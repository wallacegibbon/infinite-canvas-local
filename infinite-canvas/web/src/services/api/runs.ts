import i18n from "@/i18n";
import { apiErrorMessage, httpBlob, httpJson } from "@/services/api/http";
import type { AiConfig } from "@/stores/use-config-store";

/**
 * The gateway's run protocol (docs/protocol.md): submit a workflow run, poll it, fetch its outputs.
 *
 * This is the canvas's native path to the local gateway. The `/v1/*` services stay for channels
 * that are not the gateway; a run is the shape the gateway actually exposes, so nothing here maps
 * fields or guesses at a response envelope.
 */
type RunStatus = "queued" | "running" | "succeeded" | "failed" | "cancelled";

export type RunProgress = { phase?: string; step?: number; total?: number; percent?: number };

type RunOutput = {
    index: number;
    kind: string;
    mime: string;
    bytes: number;
    filename: string;
    url: string;
};

export type Run = {
    id: string;
    workflow: string;
    status: RunStatus;
    progress?: RunProgress;
    outputs?: RunOutput[];
    error?: { code?: string; message?: string; details?: unknown } | null;
    created_at?: string;
    started_at?: string | null;
    finished_at?: string | null;
    prompt_id?: string;
};

type RunRequest = {
    workflow: string;
    inputs?: Record<string, unknown>;
    media?: Record<string, string | string[]>;
};

type RunWaitOptions = {
    signal?: AbortSignal;
    /** Milliseconds between status reads. */
    intervalMs?: number;
    onProgress?: (run: Run) => void;
    onSteps?: (steps: { step: number; total: number }) => void;
    /**
     * Cancel the run on the gateway when the caller aborts. Set for a generation the user started;
     * leave unset when merely watching a run that should survive a page leaving.
     */
    cancelOnAbort?: boolean;
};

/** A terminal failure reported by the gateway, kept as a type so callers can tell it from a network error. */
export class RunFailedError extends Error {
    constructor(readonly run: Run) {
        super(runErrorMessage(run));
        this.name = "RunFailedError";
    }
}

const apiText = (key: string, options?: Record<string, unknown>) => i18n.t(`apiErrors.${key}`, options);
const DEFAULT_INTERVAL_MS = 500;

/** The gateway root for `/api/*`: the channel base URL without its `/v1` suffix. */
export function gatewayProtocolRoot(baseUrl: string) {
    const trimmed = baseUrl.trim().replace(/\/+$/, "");
    return trimmed.replace(/\/v1$/i, "");
}

function runUrl(config: AiConfig, path: string) {
    return `${gatewayProtocolRoot(config.baseUrl)}${path}`;
}

function jsonHeaders(config: AiConfig) {
    return { Authorization: `Bearer ${config.apiKey}`, "Content-Type": "application/json" };
}

/** Submit a run. Always answers with the run, never with its result: the id is the handle. */
export async function createRun(config: AiConfig, request: RunRequest): Promise<Run> {
    try {
        const run = await httpJson<Run>(runUrl(config, "/api/runs"), {
            method: "POST",
            headers: jsonHeaders(config),
            body: JSON.stringify(request),
        });
        if (!run?.id) throw new Error(apiText("runCreateFailed"));
        return run;
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("runCreateFailed")));
    }
}

/** Read one run. */
async function getRun(config: AiConfig, id: string): Promise<Run> {
    try {
        return await httpJson<Run>(runUrl(config, `/api/runs/${encodeURIComponent(id)}`), {
            headers: jsonHeaders(config),
        });
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("runQueryFailed")));
    }
}

/** Cancel a run. Idempotent on the gateway. */
async function cancelRun(config: AiConfig, id: string): Promise<Run> {
    return httpJson<Run>(runUrl(config, `/api/runs/${encodeURIComponent(id)}`), {
        method: "DELETE",
        headers: jsonHeaders(config),
    });
}

/**
 * Poll a run until it is terminal.
 *
 * Resolves with the finished run, throws `RunFailedError` when the gateway reports a failure or a
 * cancellation, and rethrows the abort error when the caller aborts (cancelling the run first if
 * `cancelOnAbort` is set).
 */
export async function waitForRun(config: AiConfig, id: string, options: RunWaitOptions = {}): Promise<Run> {
    const interval = options.intervalMs ?? DEFAULT_INTERVAL_MS;
    for (;;) {
        if (options.signal?.aborted) {
            await cancelOnAbort(config, id, options);
            throw abortError();
        }
        const run = await getRun(config, id);
        options.onProgress?.(run);
        if (run.progress && run.progress.total && run.progress.total > 0) {
            options.onSteps?.({ step: Math.min(run.progress.total, Math.max(0, run.progress.step || 0)), total: run.progress.total });
        }
        if (run.status === "succeeded") return run;
        if (run.status === "failed" || run.status === "cancelled") throw new RunFailedError(run);
        try {
            await delay(interval, options.signal);
        } catch (error) {
            await cancelOnAbort(config, id, options);
            throw error;
        }
    }
}

/** Download one output of a finished run. */
export async function downloadRunOutput(config: AiConfig, run: Run, index = 0, options: { signal?: AbortSignal } = {}): Promise<Blob> {
    const output = run.outputs?.[index];
    if (!output) throw new Error(apiText("runDownloadFailed"));
    const url = /^https?:/i.test(output.url) ? output.url : runUrl(config, output.url);
    try {
        return await httpBlob(url, { signal: options.signal });
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("runDownloadFailed")));
    }
}

function runErrorMessage(run: Run) {
    return run.error?.message || apiText("runFailed");
}

async function cancelOnAbort(config: AiConfig, id: string, options: RunWaitOptions) {
    if (!options.cancelOnAbort) return;
    try {
        await cancelRun(config, id);
    } catch {
        // Best effort: the caller is already aborting, and reporting this would replace its reason.
    }
}

function delay(ms: number, signal?: AbortSignal) {
    return new Promise<void>((resolve, reject) => {
        if (signal?.aborted) {
            reject(abortError());
            return;
        }
        const onAbort = () => {
            clearTimeout(timer);
            reject(abortError());
        };
        const timer = setTimeout(() => {
            signal?.removeEventListener("abort", onAbort);
            resolve();
        }, ms);
        signal?.addEventListener("abort", onAbort, { once: true });
    });
}

function abortError() {
    return new DOMException("Aborted", "AbortError");
}
