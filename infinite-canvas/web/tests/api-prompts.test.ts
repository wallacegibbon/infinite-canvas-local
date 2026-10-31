import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * The chat request the canvas sends for text.
 *
 * It used to be an OpenAI *Responses* call (`POST /v1/responses`) straight at the channel's address,
 * which the gateway never served: every text node in this build answered 404. Now it is one prompt
 * to the gateway's `/api/prompts/chat`, with the prompt endpoint's address, key and model travelling
 * in the form because the browser never contacts the model itself.
 */
vi.mock("@/i18n", () => ({ default: { t: (key: string) => key } }));
vi.mock("@/services/api/gateway-status", () => ({ noteGatewayError: () => undefined }));

// Media conversion is a browser concern; these tests are about the request the canvas builds.
vi.mock("@/services/image-storage", () => ({ imageToDataUrl: async () => "data:image/png;base64,AAAA" }));
vi.mock("@/lib/image-utils", () => ({
    dataUrlToFile: (image: { name?: string }) => new File([new Uint8Array([1, 2, 3])], image.name || "reference.png", { type: "image/png" }),
    readFileAsDataUrl: async () => "data:image/png;base64,BBBB",
}));

const { requestPromptChat } = await import("@/services/api/prompts");
type Config = Parameters<typeof requestPromptChat>[0];

const config = (overrides: Record<string, unknown> = {}) =>
    ({
        baseUrl: "http://127.0.0.1:8190",
        apiKey: "local",
        channels: [],
        models: [],
        model: "text-qwen",
        systemPrompt: "",
        promptCloudBaseUrl: "http://127.0.0.1:8099/v1",
        promptCloudApiKey: "nokey",
        promptCloudModel: "qwen3.8-27b",
        ...overrides,
    }) as unknown as Config;

const reference = (name: string) => ({ id: name, name, dataUrl: "data:image/png;base64,AAAA" }) as never;

type Call = { url: string; method: string; body: unknown; headers: Record<string, string> };

function stubGateway(body: unknown = { ok: true, text: "an answer" }, status = 200) {
    const calls: Call[] = [];
    vi.stubGlobal(
        "fetch",
        vi.fn(async (url: string, init: RequestInit = {}) => {
            calls.push({ url, method: String(init.method || "GET"), body: init.body, headers: (init.headers || {}) as Record<string, string> });
            return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
        }),
    );
    return calls;
}

/** The one POST's form, which is the whole request contract of this route. */
function formOf(calls: Call[]) {
    const call = calls.find((item) => item.method === "POST");
    if (!call) throw new Error("no POST was sent");
    return { call, form: call.body as FormData };
}

beforeEach(() => {
    vi.unstubAllGlobals();
});

describe("requestPromptChat", () => {
    it("posts the prompt to the gateway's prompt route, not to /v1", async () => {
        const calls = stubGateway();

        await expect(requestPromptChat(config(), { prompt: "describe a teapot" })).resolves.toBe("an answer");

        const { call, form } = formOf(calls);
        // The gateway has no /v1; the address in the settings has none either.
        expect(call.url).toBe("http://127.0.0.1:8190/api/prompts/chat");
        expect(form.get("prompt")).toBe("describe a teapot");
        expect(call.headers.Authorization).toBe("Bearer local");
    });

    it("carries the prompt endpoint's address, key and model from the settings", async () => {
        const calls = stubGateway();

        await requestPromptChat(config(), { prompt: "hi" });

        const { form } = formOf(calls);
        expect(form.get("cloudBaseUrl")).toBe("http://127.0.0.1:8099/v1");
        expect(form.get("cloudApiKey")).toBe("nokey");
        expect(form.get("cloudModel")).toBe("qwen3.8-27b");
    });

    it("sends no system message when neither the settings nor the caller has one", async () => {
        const calls = stubGateway();

        await requestPromptChat(config(), { prompt: "hi" });

        // An empty system turn is not the same request as no system turn: the gateway omits it, and
        // the canvas does not ask for it.
        expect(formOf(calls).form.get("system")).toBeNull();
    });

    it("puts the settings' system prompt before the caller's own", async () => {
        const calls = stubGateway();

        await requestPromptChat(config({ systemPrompt: "You are terse." }), { prompt: "hi", system: "Answer in Chinese." });

        expect(formOf(calls).form.get("system")).toBe("You are terse.\n\nAnswer in Chinese.");
    });

    it("attaches caller images as form files, in order", async () => {
        const calls = stubGateway();

        await requestPromptChat(config(), { prompt: "how many chairs?", images: [reference("a.png"), reference("b.png")] });

        const { form } = formOf(calls);
        const first = form.get("image") as File;
        const second = form.get("image2") as File;
        expect(first.name).toBe("a.png");
        expect(second.name).toBe("b.png");
    });

    it("refuses a channel that is not the gateway", async () => {
        stubGateway();

        await expect(requestPromptChat(config({ baseUrl: "https://api.example.com/v1" }), { prompt: "hi" })).rejects.toThrow("promptOptimizeLocalOnly");
    });

    it("reports a reply without text as a failure", async () => {
        stubGateway({ ok: true, text: "" });

        await expect(requestPromptChat(config(), { prompt: "hi" })).rejects.toThrow("promptOptimizeEmpty");
    });
});
