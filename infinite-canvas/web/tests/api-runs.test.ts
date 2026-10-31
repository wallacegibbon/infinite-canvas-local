import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * The run protocol client.
 *
 * Two properties matter and neither is visible in the browser: a relative output URL must resolve
 * against the gateway root (not the `/v1` API root), and polling must stop at a terminal state,
 * reporting failed runs and aborting without leaving a request in flight.
 */
const t = vi.fn((key: string) => key);

vi.mock("@/i18n", () => ({ default: { t: (key: string) => t(key) } }));

const noteGatewayError = vi.fn();
vi.mock("@/services/api/gateway-status", () => ({ noteGatewayError: (...args: unknown[]) => noteGatewayError(...args) }));

const { RunFailedError, createRun, downloadRunOutput, gatewayProtocolRoot, waitForRun } = await import("@/services/api/runs");

type Config = Parameters<typeof createRun>[0];
const config = { baseUrl: "http://gateway.test:8190/v1", apiKey: "local" } as unknown as Config;

const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
    });

const run = (overrides: Record<string, unknown> = {}) => ({
    id: "task_1",
    workflow: "audio-minimax-music-3",
    status: "running",
    progress: { step: 0, total: 0 },
    outputs: [],
    error: null,
    ...overrides,
});

beforeEach(() => {
    t.mockClear();
    noteGatewayError.mockClear();
    vi.unstubAllGlobals();
});

describe("gatewayProtocolRoot", () => {
    it("strips the /v1 suffix so /api routes address the gateway, not its API prefix", () => {
        expect(gatewayProtocolRoot("http://gateway.test:8190/v1")).toBe("http://gateway.test:8190");
        expect(gatewayProtocolRoot("http://gateway.test:8190/v1/")).toBe("http://gateway.test:8190");
        expect(gatewayProtocolRoot("http://gateway.test:8190/")).toBe("http://gateway.test:8190");
    });
});

describe("createRun", () => {
    it("posts the workflow and inputs to /api/runs", async () => {
        const fetchMock = vi.fn(async () => json(run({ status: "queued" })));
        vi.stubGlobal("fetch", fetchMock);

        const created = await createRun(config, {
            workflow: "audio-minimax-music-3",
            inputs: { prompt: "indie folk", lyrics: "[Verse]\nhello" },
        });

        expect(created.id).toBe("task_1");
        const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
        expect(url).toBe("http://gateway.test:8190/api/runs");
        expect(init.method).toBe("POST");
        expect(JSON.parse(String(init.body))).toEqual({
            workflow: "audio-minimax-music-3",
            inputs: { prompt: "indie folk", lyrics: "[Verse]\nhello" },
        });
    });
});

describe("waitForRun", () => {
    it("polls until succeeded and reports steps", async () => {
        const fetchMock = vi
            .fn()
            .mockResolvedValueOnce(json(run({ progress: { step: 3, total: 10 } })))
            .mockResolvedValueOnce(json(run({ status: "succeeded", progress: { step: 10, total: 10 }, outputs: [{ index: 0, url: "/api/runs/task_1/outputs/0" }] })));
        vi.stubGlobal("fetch", fetchMock);

        const steps: Array<{ step: number; total: number }> = [];
        const finished = await waitForRun(config, "task_1", { intervalMs: 1, onSteps: (value) => steps.push(value) });

        expect(finished.status).toBe("succeeded");
        expect(fetchMock).toHaveBeenCalledTimes(2);
        expect(steps).toEqual([
            { step: 3, total: 10 },
            { step: 10, total: 10 },
        ]);
    });

    it("throws the gateway's message when the run fails", async () => {
        vi.stubGlobal(
            "fetch",
            vi.fn(async () => json(run({ status: "failed", error: { code: "comfy_execution_failed", message: "Prompt outputs failed validation" } }))),
        );

        const error = await waitForRun(config, "task_1", { intervalMs: 1 }).catch((caught) => caught);
        expect(error).toBeInstanceOf(RunFailedError);
        expect(error.message).toBe("Prompt outputs failed validation");
    });

    it("cancels the run when the caller aborts, if asked to", async () => {
        const fetchMock = vi.fn(async () => json(run({ status: "cancelled" })));
        vi.stubGlobal("fetch", fetchMock);
        const controller = new AbortController();
        controller.abort();

        await expect(waitForRun(config, "task_1", { signal: controller.signal, cancelOnAbort: true })).rejects.toMatchObject({ name: "AbortError" });

        const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
        expect(url).toBe("http://gateway.test:8190/api/runs/task_1");
        expect(init.method).toBe("DELETE");
    });

    it("does not cancel a run it is only watching", async () => {
        const fetchMock = vi.fn(async () => json(run()));
        vi.stubGlobal("fetch", fetchMock);
        const controller = new AbortController();
        controller.abort();

        await expect(waitForRun(config, "task_1", { signal: controller.signal, cancelOnAbort: false })).rejects.toMatchObject({ name: "AbortError" });
        expect(fetchMock).not.toHaveBeenCalled();
    });
});

describe("downloadRunOutput", () => {
    it("resolves a relative output URL against the gateway root", async () => {
        const fetchMock = vi.fn(async () => new Response(new Blob(["MP3"], { type: "audio/mpeg" }), { status: 200 }));
        vi.stubGlobal("fetch", fetchMock);

        const blob = await downloadRunOutput(config, run({ status: "succeeded", outputs: [{ index: 0, url: "/api/runs/task_1/outputs/0" }] }));

        expect(await blob.text()).toBe("MP3");
        const [url] = fetchMock.mock.calls[0] as unknown as [string];
        expect(url).toBe("http://gateway.test:8190/api/runs/task_1/outputs/0");
    });

    it("fails when the run has no such output", async () => {
        await expect(downloadRunOutput(config, run({ status: "succeeded", outputs: [] }))).rejects.toThrow();
    });
});
