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
        promptBaseUrl: "http://127.0.0.1:8099/v1",
        promptApiKey: "nokey",
        promptModel: "qwen3.8-27b",
        ...overrides,
    }) as unknown as Config;

const reference = (name: string) => ({ id: name, name, dataUrl: "data:image/png;base64,AAAA" }) as never;

type Call = { url: string; method: string; body: unknown; headers: Record<string, string> };

/** The agent answers with an event stream, so a stub that speaks that framing is what these need. */
function stubGateway(input: { body?: string; json?: unknown; status?: number } = {}) {
    const status = input.status ?? 200;
    const calls: Call[] = [];
    vi.stubGlobal(
        "fetch",
        vi.fn(async (url: string, init: RequestInit = {}) => {
            calls.push({ url, method: String(init.method || "GET"), body: init.body, headers: (init.headers || {}) as Record<string, string> });
            if (input.body !== undefined) return new Response(input.body, { status, headers: { "Content-Type": "text/event-stream" } });
            return new Response(JSON.stringify(input.json ?? {}), { status, headers: { "Content-Type": "application/json" } });
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
    it("asks the agent's one route, and does not carry an endpoint of its own", async () => {
        const calls = stubGateway({ body: 'data: {"type":"delta","text":"an "}\n\ndata: {"type":"delta","text":"answer"}\n\ndata: {"type":"done","text":"an answer"}\n\n' });

        await expect(requestPromptChat(config(), { prompt: "describe a teapot" })).resolves.toBe("an answer");

        const { call, form } = formOf(calls);
        expect(call.url).toBe("http://127.0.0.1:8190/api/agent/chat");
        expect(form.get("prompt")).toBe("describe a teapot");
        // The model, the key and the base URL are the agent's own configuration now: a question
        // carries a question.
        for (const field of ["promptBaseUrl", "promptApiKey", "promptModel", "system", "command"]) {
            expect(form.get(field)).toBeNull();
        }
    });

    it("reports the answer as it arrives", async () => {
        stubGateway({ body: 'data: {"type":"delta","text":"half "}\n\ndata: {"type":"delta","text":"way"}\n\n' });

        const seen: string[] = [];
        await requestPromptChat(config(), { prompt: "hi" }, { onDelta: (text) => seen.push(text) });

        expect(seen).toEqual(["half ", "half way"]);
    });

    it("relays the agent's own error", async () => {
        stubGateway({ body: 'data: {"type":"error","message":"model_set: model not found: 9"}\n\ndata: {"type":"done"}\n\n' });

        await expect(requestPromptChat(config(), { prompt: "hi" })).rejects.toThrow("model_set: model not found: 9");
    });

    it("refuses a channel that is not the gateway", async () => {
        stubGateway({ body: "" });

        await expect(requestPromptChat(config({ baseUrl: "https://api.example.com/v1" }), { prompt: "hi" })).rejects.toThrow("promptOptimizeLocalOnly");
    });
});
