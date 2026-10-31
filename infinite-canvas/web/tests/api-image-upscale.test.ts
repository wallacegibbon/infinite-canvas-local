import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * Image upscaling through the run protocol.
 *
 * The upscaler used to be a multipart post to `/v1/images/upscale`. It is one of the workflows the
 * canvas drives from a button rather than the model list, so it was the last image action outside
 * the run protocol: now the source image is uploaded once, named by role, and the chosen workflow's
 * own knobs are the typed inputs.
 */
vi.mock("@/i18n", () => ({ default: { t: (key: string) => key } }));
vi.mock("@/services/api/gateway-status", () => ({ noteGatewayError: () => undefined }));

// Media conversion is a browser concern; these tests are about the request the canvas builds.
vi.mock("@/services/image-storage", () => ({ imageToDataUrl: async () => "data:image/png;base64,AAAA" }));
vi.mock("@/lib/image-utils", () => ({
    dataUrlToFile: (image: { name?: string }) => new File([new Uint8Array([1, 2, 3])], image.name || "source.png", { type: "image/png" }),
    readFileAsDataUrl: async () => "data:image/png;base64,BBBB",
}));

const { requestUpscale } = await import("@/services/api/image");
type Config = Parameters<typeof requestUpscale>[0];

const gateway = () =>
    ({
        baseUrl: "http://127.0.0.1:8190/v1",
        apiKey: "local",
        apiFormat: "openai",
        channels: [],
        models: ["image-qwen21"],
        model: "image-qwen21",
        imageModel: "image-qwen21",
        size: "1024x1024",
        count: "1",
        systemPrompt: "",
        quality: "",
        params: {},
    }) as unknown as Config;

const image = (name: string) => ({ id: name, name, dataUrl: "data:image/png;base64,AAAA" }) as never;

const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
    });

const run = (overrides: Record<string, unknown> = {}) => ({
    id: "task_upscale_1",
    workflow: "image-esrgan-upscale",
    status: "running",
    progress: { step: 0, total: 0 },
    outputs: [],
    error: null,
    ...overrides,
});

const succeeded = () =>
    json(
        run({
            status: "succeeded",
            outputs: [{ index: 0, kind: "image", mime: "image/png", bytes: 3, filename: "out.png", url: "/api/runs/task_upscale_1/outputs/0" }],
        }),
    );

type Call = { url: string; method: string; body: unknown };

/** Route a stubbed fetch by path, so a test names only the calls it cares about. */
function stubGateway(handlers: { status?: () => Response } = {}) {
    const calls: Call[] = [];
    const fetchMock = vi.fn(async (url: string, init: RequestInit = {}) => {
        calls.push({ url, method: String(init.method || "GET"), body: init.body });
        if (url.endsWith("/api/uploads")) return json({ upload: { id: `upl_${calls.length}`, kind: "image", mime: "image/png", bytes: 3, filename: "source.png" } });
        if (url.endsWith("/api/runs")) return json(run({ status: "queued" }), 202);
        if (url.includes("/outputs/0")) return new Response(new Blob(["PNG"], { type: "image/png" }), { status: 200 });
        return handlers.status?.() ?? succeeded();
    });
    vi.stubGlobal("fetch", fetchMock);
    return { calls, fetchMock };
}

/** The JSON body of the one POST /api/runs call. */
function runBody(calls: Call[]) {
    const call = calls.find((item) => item.url.endsWith("/api/runs") && item.method === "POST");
    if (!call) throw new Error("no POST /api/runs");
    return JSON.parse(String(call.body));
}

beforeEach(() => {
    vi.unstubAllGlobals();
});

describe("requestUpscale", () => {
    it("is one run with the source image uploaded by role", async () => {
        const { calls } = stubGateway();

        const images = await requestUpscale(gateway(), image("source.png"), {
            workflowId: "image-esrgan-upscale",
            params: { upscale_model: "RealESRGAN_x4plus.pth" },
        });

        expect(images).toHaveLength(1);
        expect(images[0].dataUrl).toBe("data:image/png;base64,BBBB");
        expect(calls[0].url).toBe("http://127.0.0.1:8190/api/uploads");
        // Nothing reaches the OpenAI-shaped surface any more: the id in hand is a run id.
        expect(calls.some((call) => call.url.includes("/v1/"))).toBe(false);

        const body = runBody(calls);
        expect(body.workflow).toBe("image-esrgan-upscale");
        expect(body.inputs).toEqual({ upscale_model: "RealESRGAN_x4plus.pth" });
        expect(body.media).toEqual({ image: ["upl_1"] });
    });

    it("sends no prompt: the workflow's own default is the prompt", async () => {
        const { calls } = stubGateway();

        await requestUpscale(gateway(), image("source.png"), { workflowId: "image-esrgan-upscale" });

        // An undeclared key is a 400 from the typed input contract, so a prompt here would break
        // every upscale that has its own text binding.
        expect(runBody(calls).inputs).toEqual({});
    });

    it("reports the run's failure as the request's failure", async () => {
        stubGateway({
            status: () => json(run({ status: "failed", error: { code: "comfy_execution_failed", message: "Prompt outputs failed validation" } })),
        });

        await expect(requestUpscale(gateway(), image("source.png"), { workflowId: "image-esrgan-upscale" })).rejects.toThrow("Prompt outputs failed validation");
    });

    it("refuses a remote base URL: upscaling is a local ComfyUI workflow", async () => {
        await expect(requestUpscale({ ...gateway(), baseUrl: "https://api.example.com/v1" } as Config, image("source.png"), { workflowId: "image-esrgan-upscale" })).rejects.toThrow("upscaleLocalOnly");
    });
});
