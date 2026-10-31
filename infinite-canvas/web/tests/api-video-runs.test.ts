import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * Video generation through the run protocol.
 *
 * A video node used to submit a multipart task to `/v1/videos` and then poll `/v1/videos/{id}`. It
 * now submits a run: media is uploaded once and referenced by role, the knobs are the workflow's own
 * typed inputs, and the id that comes back is a run id — which is what makes the node resumable
 * after a reload.
 */
const t = vi.fn((key: string) => key);

vi.mock("@/i18n", () => ({ default: { t: (key: string) => t(key) } }));
vi.mock("@/services/api/gateway-status", () => ({ noteGatewayError: () => undefined }));

// Media conversion is a browser concern; these tests are about the request the canvas builds.
vi.mock("@/services/image-storage", () => ({ imageToDataUrl: async () => "data:image/png;base64,AAAA" }));
vi.mock("@/lib/image-utils", () => ({
    dataUrlToFile: (image: { name?: string }) => new File([new Uint8Array([1, 2, 3])], image.name || "reference.png", { type: "image/png" }),
    readFileAsDataUrl: async () => "data:image/png;base64,AAAA",
}));
vi.mock("@/services/file-storage", () => ({
    getMediaBlob: async () => new Blob([new Uint8Array([4, 5, 6])], { type: "video/mp4" }),
    resolveMediaUrl: async () => "",
    uploadMediaFile: async () => ({ url: "", storageKey: "", bytes: 0, mimeType: "video/mp4" }),
}));

const { createVideoGenerationTask, createVideoUpscaleTask, isGatewayVideoModel, videoTaskProviderOf, waitForVideoGenerationTask } = await import("@/services/api/video");

type Config = Parameters<typeof createVideoGenerationTask>[0];
const gateway = (overrides: Record<string, unknown> = {}) =>
    ({
        baseUrl: "http://127.0.0.1:8190/v1",
        apiKey: "local",
        apiFormat: "openai",
        channels: [],
        models: ["video-h3-frames"],
        model: "video-h3-frames",
        videoModel: "video-h3-frames",
        videoSeconds: "5",
        size: "16:9",
        vquality: "720",
        videoMode: "frames",
        videoInterpolation: "off",
        frameMode: "first",
        seedFixed: "false",
        ...overrides,
    }) as unknown as Config;

const image = (name: string) => ({ id: name, name, dataUrl: "data:image/png;base64,AAAA" }) as never;

const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
    });

const run = (overrides: Record<string, unknown> = {}) => ({
    id: "task_video_1",
    workflow: "video-h3-frames",
    status: "running",
    progress: { step: 0, total: 0 },
    outputs: [],
    error: null,
    ...overrides,
});

/** Route a stubbed fetch by path, so a test names only the calls it cares about. */
function stubGateway(handlers: { run?: () => Response; status?: () => Response; delete?: () => Response; output?: () => Response }) {
    const calls: Array<{ url: string; method: string; body: unknown }> = [];
    const fetchMock = vi.fn(async (url: string, init: RequestInit = {}) => {
        calls.push({ url, method: String(init.method || "GET"), body: init.body });
        if (url.endsWith("/api/uploads")) return json({ upload: { id: `upl_${calls.length}`, kind: "image", mime: "image/png", bytes: 3, filename: "f" } });
        if (url.endsWith("/api/runs")) return handlers.run?.() ?? json(run({ status: "queued" }), 202);
        if (init.method === "DELETE") return handlers.delete?.() ?? json(run({ status: "cancelled" }));
        if (url.includes("/outputs/0")) return handlers.output?.() ?? new Response(new Blob(["MP4"], { type: "video/mp4" }), { status: 200 });
        return handlers.status?.() ?? json(run());
    });
    vi.stubGlobal("fetch", fetchMock);
    return { calls, fetchMock };
}

/** The JSON body of the one POST /api/runs call. */
function runBody(calls: Array<{ url: string; method: string; body: unknown }>) {
    const call = calls.find((item) => item.url.endsWith("/api/runs") && item.method === "POST");
    if (!call) throw new Error("no POST /api/runs");
    return JSON.parse(String(call.body));
}

beforeEach(() => {
    t.mockClear();
    vi.unstubAllGlobals();
});

describe("isGatewayVideoModel", () => {
    it("is true only for a gateway channel serving a video workflow", () => {
        expect(isGatewayVideoModel({ baseUrl: "http://127.0.0.1:8190/v1", model: "video-h3-frames" })).toBe(true);
        expect(isGatewayVideoModel({ baseUrl: "http://127.0.0.1:8190/v1", model: "local-gateway::video-h3-t2v" })).toBe(true);
        expect(isGatewayVideoModel({ baseUrl: "http://127.0.0.1:8190/v1", model: "image-qwen21" })).toBe(false);
        expect(isGatewayVideoModel({ baseUrl: "https://api.example.com/v1", model: "video-h3-frames" })).toBe(false);
    });
});

describe("createVideoGenerationTask", () => {
    it("submits one run with the frame uploaded by role", async () => {
        const { calls } = stubGateway({});

        const task = await createVideoGenerationTask(gateway(), "a cat walks", [image("first.png")]);

        expect(task.provider).toBe("run");
        expect(task.id).toBe("task_video_1");
        const body = runBody(calls);
        expect(body.workflow).toBe("video-h3-frames");
        expect(body.inputs).toMatchObject({ prompt: "a cat walks", mode: "frames", frameMode: "first", seconds: 5 });
        expect(body.inputs.size).toMatch(/^\d+x\d+$/);
        expect(body.media).toEqual({ first_frame: "upl_1" });
        expect(calls[0].url).toBe("http://127.0.0.1:8190/api/uploads");
    });

    it("sends the second image as the last frame when both are asked for", async () => {
        const { calls } = stubGateway({});

        await createVideoGenerationTask(gateway({ frameMode: "both" }), "morph", [image("a.png"), image("b.png")]);

        expect(runBody(calls).media).toEqual({ first_frame: "upl_1", last_frame: "upl_2" });
    });

    it("references multi-modal media by role instead of by form field", async () => {
        const { calls } = stubGateway({});

        await createVideoGenerationTask(gateway({ model: "video-h3-ref2va", videoModel: "video-h3-ref2va", videoMode: "reference" }), "same dancer", [image("a.png")], {
            videos: [{ id: "v1", name: "ref.mp4", type: "video/mp4", storageKey: "k1" } as never],
            audios: [{ id: "a1", name: "ref.mp3", type: "audio/mpeg", storageKey: "k2" } as never],
        });

        const body = runBody(calls);
        expect(body.workflow).toBe("video-h3-ref2va");
        expect(body.inputs.mode).toBe("reference");
        expect(body.inputs.frameMode).toBeUndefined();
        expect(Object.keys(body.media).sort()).toEqual(["audio", "reference", "video"]);
        expect(body.media.reference).toEqual(["upl_1"]);
    });

    it("asks for interpolation on the same run", async () => {
        const { calls } = stubGateway({});

        await createVideoGenerationTask(gateway({ videoInterpolation: "2x" }), "a cat", [image("a.png")]);

        expect(runBody(calls).inputs.interpolation).toBe("2x");
    });

    it("sends no media for a text-to-video workflow", async () => {
        const { calls } = stubGateway({});

        await createVideoGenerationTask(gateway({ model: "video-h3-t2v", videoModel: "video-h3-t2v", videoMode: "text" }), "a cat", [image("a.png")]);

        const body = runBody(calls);
        expect(body.inputs.mode).toBe("text");
        expect(body.media).toBeUndefined();
    });
});

describe("createVideoUpscaleTask", () => {
    it("is one run with the clip uploaded by role", async () => {
        const { calls, fetchMock } = stubGateway({});
        // The source clip is read as a blob, so the upload route answers with a video id.
        fetchMock.mockImplementationOnce(async (url: string, init: RequestInit = {}) => {
            calls.push({ url, method: String(init.method || "GET"), body: init.body });
            return json({ upload: { id: "upl_video", kind: "video", mime: "video/mp4", bytes: 3, filename: "source.mp4" } });
        });

        const task = await createVideoUpscaleTask(gateway(), { name: "clip.mp4", type: "video/mp4", storageKey: "k1" }, { scale: 2, resolution: 1104, fps: 24, seed: 7, batchSize: 5 });

        expect(task).toMatchObject({ provider: "run", model: "video-seedvr2-upscale" });
        const body = runBody(calls);
        expect(body.workflow).toBe("video-seedvr2-upscale");
        expect(body.media).toEqual({ video: "upl_video" });
        // `scale` and `fps` are dialog values the graph derives itself; sending them would be a 400.
        expect(body.inputs).toEqual({ resolution: 1104, seed: 7, batch_size: 5 });
    });
});

describe("waitForVideoGenerationTask", () => {
    it("reports steps and preview frames, then returns the run's video", async () => {
        const { calls } = stubGateway({
            status: () =>
                json(
                    run({
                        status: "succeeded",
                        progress: { step: 8, total: 8 },
                        preview: { url: "/api/runs/task_video_1/preview?t=42", mime: "image/jpeg" },
                        outputs: [{ index: 0, kind: "video", mime: "video/mp4", bytes: 3, filename: "v.mp4", url: "/api/runs/task_video_1/outputs/0" }],
                    }),
                ),
        });

        const steps: Array<{ step: number; total: number }> = [];
        const previews: Array<{ url: string } | null> = [];
        const result = await waitForVideoGenerationTask(
            gateway(),
            { id: "task_video_1", provider: "run", model: "video-h3-frames" },
            {
                onSteps: (value) => steps.push(value),
                onPreview: (value) => previews.push(value),
            },
        );

        expect(await result.blob?.text()).toBe("MP4");
        expect(steps).toEqual([{ step: 8, total: 8 }]);
        expect(previews[0]).toMatchObject({ url: "http://127.0.0.1:8190/api/runs/task_video_1/preview?t=42" });
        expect(previews[previews.length - 1]).toBeNull();
        expect(calls.some((call) => call.url.endsWith("/outputs/0"))).toBe(true);
    });

    it("turns a failed run into a video task failure so the node stops polling it", async () => {
        stubGateway({ status: () => json(run({ status: "failed", error: { code: "comfy_execution_failed", message: "Prompt outputs failed validation" } })) });

        const error = await waitForVideoGenerationTask(gateway(), { id: "task_video_1", provider: "run", model: "video-h3-frames" }).catch((caught) => caught);

        expect(error.name).toBe("VideoTaskFailed");
        expect(error.message).toBe("Prompt outputs failed validation");
    });

    it("cancels the run only when the user stopped it", async () => {
        const { calls } = stubGateway({});
        const controller = new AbortController();
        const watching = waitForVideoGenerationTask(gateway(), { id: "task_video_1", provider: "run", model: "video-h3-frames" }, { signal: controller.signal });
        controller.abort("user-stop");

        await expect(watching).rejects.toMatchObject({ name: "AbortError" });
        expect(calls.some((call) => call.method === "DELETE")).toBe(true);
    });

    it("leaves the run alone when the page goes away", async () => {
        const { calls } = stubGateway({});
        const controller = new AbortController();
        const watching = waitForVideoGenerationTask(gateway(), { id: "task_video_1", provider: "run", model: "video-h3-frames" }, { signal: controller.signal });
        controller.abort();

        await expect(watching).rejects.toMatchObject({ name: "AbortError" });
        expect(calls.some((call) => call.method === "DELETE")).toBe(false);
    });
});

describe("videoTaskProviderOf", () => {
    it("keeps the stored provider and defaults to the /v1 contract", () => {
        expect(videoTaskProviderOf("run")).toBe("run");
        expect(videoTaskProviderOf("gemini")).toBe("gemini");
        expect(videoTaskProviderOf("openai")).toBe("openai");
        expect(videoTaskProviderOf(undefined)).toBe("openai");
    });
});
