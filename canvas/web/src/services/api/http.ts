import i18n from "@/i18n";
import { noteGatewayError } from "@/services/api/gateway-status";

/**
 * The app's HTTP layer.
 *
 * This replaces `axios` in the four service files that used it, and replaces the three
 * copies of `readApiErrorMessage` / `readAxiosError` / `statusMessage` those files each
 * carried. Nothing in the app used anything axios alone can do: no `onUploadProgress`
 * (the one capability `fetch` genuinely lacks), no interceptors, no `CancelToken`. The
 * codebase was already split — 32 axios calls in four files, and 17 `fetch` calls
 * everywhere else, including SSE and blob downloads.
 *
 * Two behaviours are *better* than the axios paths they replace:
 *
 *   - A timeout is now distinguishable from a cancel. axios surfaced timeouts as
 *     `ECONNABORTED`, which each file had to special-case; here `fetch` is aborted with a
 *     `TimeoutError` reason, so `apiErrorMessage` can tell "took too long" from "the user
 *     stopped it".
 *   - A non-2xx response that was expected to be a blob is still read as text for its
 *     error message, where axios handed back an unreadable Blob.
 */
const apiText = (key: string, options?: Record<string, unknown>) => i18n.t(`apiErrors.${key}`, options);

/** A response that arrived with a status outside 2xx, carrying the parsed body. */
export class ApiError extends Error {
    constructor(
        readonly status: number,
        readonly data: unknown,
    ) {
        super(`HTTP ${status}`);
        this.name = "ApiError";
    }
}

type HttpInit = Omit<RequestInit, "signal"> & {
    /** Milliseconds, as axios's `timeout`. Aborts with a `TimeoutError`. */
    timeout?: number;
    signal?: AbortSignal;
};

function isDomError(error: unknown, name: string): boolean {
    return error instanceof DOMException && error.name === name;
}

/** True when the caller's own signal ended the request, as opposed to a timeout. */
export function isCancelled(error: unknown): boolean {
    return isDomError(error, "AbortError");
}

/**
 * One `fetch` wrapper: links the caller's signal, applies the timeout, and reports a
 * network failure as a `TypeError` the way `fetch` does — `apiErrorMessage` turns that
 * into the same "request failed" string axios's `ERR_NETWORK` produced.
 */
async function send(url: string, init: HttpInit): Promise<Response> {
    const { timeout, signal, ...rest } = init;
    const timeoutController = timeout ? new AbortController() : undefined;
    const timer = timeoutController ? setTimeout(() => timeoutController.abort(new DOMException(`timeout after ${timeout}ms`, "TimeoutError")), timeout) : undefined;

    // `AbortSignal.any` would be shorter but is newer than the language level this file
    // is checked against; linking by hand keeps the requirement to plain `fetch`.
    const relay = () => timeoutController?.abort(signal?.reason);
    signal?.addEventListener("abort", relay, { once: true });

    try {
        return await fetch(url, { ...rest, signal: timeoutController ? timeoutController.signal : signal });
    } catch (error) {
        // This is what an axios response interceptor used to do in `image.ts`: one failing
        // request to the local gateway flips the shared "reconnecting" banner instead of
        // raising one toast per node. `noteGatewayError` only reacts to network failures,
        // and only for gateway URLs, so it can be called from here for every request.
        noteGatewayError(error, url);
        throw error;
    } finally {
        if (timer !== undefined) clearTimeout(timer);
        signal?.removeEventListener("abort", relay);
    }
}

/** Read the body once, as JSON when it parses and as text otherwise. */
async function parseBody(response: Response): Promise<unknown> {
    const text = await response.text();
    if (!text) return "";
    try {
        return JSON.parse(text);
    } catch {
        return text;
    }
}

export async function httpJson<T>(url: string, init: HttpInit = {}): Promise<T> {
    const response = await send(url, init);
    if (!response.ok) throw new ApiError(response.status, await parseBody(response));
    const text = await response.text();
    if (!text) return undefined as T;
    try {
        return JSON.parse(text) as T;
    } catch {
        // A 2xx that is not JSON is a gateway or proxy saying something we cannot use;
        // surfacing the text is more useful than a JSON parse error.
        throw new ApiError(response.status, text);
    }
}

export async function httpBlob(url: string, init: HttpInit = {}): Promise<Blob> {
    const response = await send(url, init);
    if (!response.ok) throw new ApiError(response.status, await parseBody(response));
    return response.blob();
}

/** A request whose body is not read, where only success matters. */
export async function httpVoid(url: string, init: HttpInit = {}): Promise<void> {
    const response = await send(url, init);
    if (!response.ok) throw new ApiError(response.status, await parseBody(response));
    await response.body?.cancel();
}

/**
 * Pull a human message out of whatever an API returned: a string, serialized JSON, a
 * plain-text or HTML error page, or an object with `msg` / `message` / `error` / `detail`.
 */
export function readApiErrorMessage(value: unknown): string {
    if (!value) return "";
    if (typeof value === "string") {
        try {
            const parsed = JSON.parse(value);
            const inner = readApiErrorMessage(parsed) || value;
            if (inner === value && typeof parsed === "object" && Object.keys(parsed as object).length === 0) return "";
            return inner;
        } catch {
            if (/^\s*<(!doctype\s+html|html[\s>])/i.test(value)) return apiText("htmlError", { preview: `${value.slice(0, 80)}...` });
            return value;
        }
    }
    if (typeof value !== "object") return "";
    const payload = value as { msg?: unknown; message?: unknown; error?: unknown; detail?: unknown };
    const errorMsg = typeof payload.error === "string" ? payload.error : (payload.error as { message?: unknown })?.message;
    return readApiErrorMessage(payload.msg) || readApiErrorMessage(payload.message) || readApiErrorMessage(errorMsg) || readApiErrorMessage(payload.detail) || "";
}

/** HTTP status to a translated sentence, for responses that carry no usable message. */
function statusMessage(status: number | undefined, fallback: string): string {
    if (status === 401 || status === 403) return apiText("authenticationFailed");
    if (status === 404) return apiText("notFound");
    if (status === 429) return apiText("rateLimited");
    if (status === 502) return apiText("badGateway");
    if (status === 503) return apiText("serviceBusy");
    return status ? `${fallback}（${status}）` : fallback;
}

/**
 * The one error translator. Order matters and mirrors what the axios version did:
 * cancellation, timeout, a message the API sent, then a sentence inferred from the status,
 * then the fallback.
 *
 * `timeoutMessage` lets the image service keep its longer, image-specific timeout text.
 */
export function apiErrorMessage(error: unknown, fallback: string, timeoutMessage = "requestFailed"): string {
    if (isCancelled(error)) return apiText("requestCanceled");
    if (isDomError(error, "TimeoutError")) return apiText(timeoutMessage);
    if (error instanceof ApiError) {
        return readApiErrorMessage(error.data) || statusMessage(error.status, fallback);
    }
    // `fetch` reports a network failure as a TypeError, which is axios's ERR_NETWORK.
    if (error instanceof TypeError) return apiText("requestFailed");
    return error instanceof Error ? readApiErrorMessage(error.message) || error.message : fallback;
}
