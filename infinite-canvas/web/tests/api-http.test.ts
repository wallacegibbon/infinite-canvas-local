import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * Tests for the transport layer that replaced axios.
 *
 * The mapping from failure to user-facing string is the part that used to live in an axios
 * response interceptor and in three per-file copies of `readAxiosError`. A browser can see
 * the result but cannot enumerate the branches, so they are pinned here: cancel, timeout,
 * a message the API sent, a sentence inferred from the status, and the fallback.
 */
const t = vi.fn((key: string) => key);

vi.mock("@/i18n", () => ({ default: { t: (key: string) => t(key) } }));

const noteGatewayError = vi.fn();
vi.mock("@/services/api/gateway-status", () => ({ noteGatewayError: (...args: unknown[]) => noteGatewayError(...args) }));

const { ApiError, apiErrorMessage, httpBlob, httpJson, isCancelled, readApiErrorMessage } = await import("@/services/api/http");

const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
    });

beforeEach(() => {
    noteGatewayError.mockClear();
    t.mockClear();
});

describe("httpJson", () => {
    it("returns the parsed body", async () => {
        vi.stubGlobal("fetch", async () => json({ ok: 1 }));
        await expect(httpJson<{ ok: number }>("http://example.test/x")).resolves.toEqual({ ok: 1 });
    });

    it("returns undefined for a 2xx with an empty body", async () => {
        vi.stubGlobal("fetch", async () => new Response(null, { status: 204 }));
        await expect(httpJson("http://example.test/x")).resolves.toBeUndefined();
    });

    it("throws ApiError with the status and the parsed body on a non-2xx", async () => {
        vi.stubGlobal("fetch", async () => json({ error: { message: "model not found" } }, 404));
        const error = await httpJson("http://example.test/x").catch((caught) => caught);
        expect(error).toBeInstanceOf(ApiError);
        expect(error.status).toBe(404);
        expect(apiErrorMessage(error, "fallback")).toBe("model not found");
    });

    it("reports a local-gateway network failure to the reconnect banner", async () => {
        // This is what the axios interceptor in image.ts used to do for every request.
        vi.stubGlobal("fetch", async () => {
            throw new TypeError("Failed to fetch");
        });
        const error = await httpJson("http://127.0.0.1:8190/v1/images/generations").catch((caught) => caught);
        expect(noteGatewayError).toHaveBeenCalledTimes(1);
        expect(noteGatewayError.mock.calls[0][1]).toBe("http://127.0.0.1:8190/v1/images/generations");
        expect(apiErrorMessage(error, "fallback")).toBe("apiErrors.requestFailed");
    });
});

describe("httpBlob", () => {
    it("returns the blob", async () => {
        vi.stubGlobal("fetch", async () => new Response(new Blob(["x"], { type: "video/mp4" }), { status: 200 }));
        const blob = await httpBlob("http://example.test/v.mp4");
        expect(blob).toBeInstanceOf(Blob);
    });

    it("still reads an error body as text when the caller expected a blob", async () => {
        // axios handed back an unreadable Blob here, which is why this is pinned.
        vi.stubGlobal("fetch", async () => json({ detail: "task expired" }, 410));
        const error = await httpBlob("http://example.test/v.mp4").catch((caught) => caught);
        expect(error).toBeInstanceOf(ApiError);
        expect(apiErrorMessage(error, "fallback")).toBe("task expired");
    });
});

describe("apiErrorMessage", () => {
    it("treats an abort as a cancellation", async () => {
        vi.stubGlobal(
            "fetch",
            (_url: string, init: RequestInit) =>
                new Promise((_resolve, reject) => {
                    init.signal?.addEventListener("abort", () => reject(init.signal?.reason));
                }),
        );
        const controller = new AbortController();
        const pending = httpJson("http://example.test/x", { signal: controller.signal });
        controller.abort();
        const error = await pending.catch((caught) => caught);
        expect(isCancelled(error)).toBe(true);
        expect(apiErrorMessage(error, "fallback")).toBe("apiErrors.requestCanceled");
    });

    it("tells a timeout apart from a cancel, using the caller's wording", async () => {
        vi.stubGlobal(
            "fetch",
            (_url: string, init: RequestInit) =>
                new Promise((_resolve, reject) => {
                    init.signal?.addEventListener("abort", () => reject(init.signal?.reason));
                }),
        );
        const error = await httpJson("http://example.test/x", { timeout: 5 }).catch((caught) => caught);
        expect(isCancelled(error)).toBe(false);
        expect(apiErrorMessage(error, "fallback", "imageTimeout")).toBe("apiErrors.imageTimeout");
    });

    it.each([
        [401, "apiErrors.authenticationFailed"],
        [403, "apiErrors.authenticationFailed"],
        [404, "apiErrors.notFound"],
        [429, "apiErrors.rateLimited"],
        [502, "apiErrors.badGateway"],
        [503, "apiErrors.serviceBusy"],
    ])("maps status %i to %s when the body carries no message", async (status, expected) => {
        vi.stubGlobal("fetch", async () => json({}, status));
        const error = await httpJson("http://example.test/x").catch((caught) => caught);
        expect(apiErrorMessage(error, "fallback")).toBe(expected);
    });

    it("falls back with the status when nothing else is known", async () => {
        vi.stubGlobal("fetch", async () => new Response("", { status: 418 }));
        const error = await httpJson("http://example.test/x").catch((caught) => caught);
        expect(apiErrorMessage(error, "请求失败")).toBe("请求失败（418）");
    });

    it("passes a plain Error message through", () => {
        expect(apiErrorMessage(new Error("截图失败"), "fallback")).toBe("截图失败");
    });

    it("uses the fallback for a non-Error value", () => {
        expect(apiErrorMessage("nope", "fallback")).toBe("fallback");
    });
});

describe("readApiErrorMessage", () => {
    it.each([
        [{ msg: "from msg" }, "from msg"],
        [{ message: "from message" }, "from message"],
        [{ error: "from error" }, "from error"],
        [{ error: { message: "nested" } }, "nested"],
        [{ detail: "from detail" }, "from detail"],
        [{ msg: JSON.stringify({ message: "serialized" }) }, "serialized"],
        ["plain text", "plain text"],
        [{}, ""],
        ["", ""],
        [null, ""],
    ])("reads %j as %j", (value, expected) => {
        expect(readApiErrorMessage(value)).toBe(expected);
    });

    it("recognizes an HTML error page instead of pasting markup", () => {
        expect(readApiErrorMessage("<!DOCTYPE html><html><body>502</body></html>")).toBe("apiErrors.htmlError");
    });
});
