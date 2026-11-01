import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * The one-shot request the canvas sends for text.
 *
 * A text node's body is one answer that nothing follows, so it goes to `/api/agent/once` — a socket
 * that carries one question and dies with the answer. It is deliberately not the conversation's socket,
 * which names a canvas and keeps the agent's history: a shortcut must never be able to spend that.
 */
vi.mock("@/i18n", () => ({ default: { t: (key: string) => key } }));
vi.mock("@/services/api/gateway-status", () => ({ noteGatewayError: () => undefined }));

// Media conversion is a browser concern; these tests are about the request the canvas builds.
vi.mock("@/services/image-storage", () => ({ imageToDataUrl: async () => "data:image/png;base64,AAAA" }));

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
        ...overrides,
    }) as unknown as Config;

const reference = (name: string) => ({ id: name, name, dataUrl: "data:image/png;base64,AAAA" }) as never;

/**
 * The socket the client opens, standing in for the browser's. It records what was sent and lets a test
 * play the agent.
 */
class FakeSocket {
    static last: FakeSocket | null = null;

    url: string;
    sent: string[] = [];
    readyState = 0;
    onopen: (() => void) | null = null;
    onmessage: ((event: { data: string }) => void) | null = null;
    onerror: (() => void) | null = null;
    onclose: (() => void) | null = null;

    constructor(url: string) {
        this.url = url;
        FakeSocket.last = this;
        // Opening is asynchronous in a browser, and the client sends on `open`.
        queueMicrotask(() => {
            this.readyState = 1;
            this.onopen?.();
        });
    }

    send(data: string) {
        this.sent.push(data);
    }

    close() {
        this.readyState = 3;
    }

    /** Say something as the agent. */
    emit(event: unknown) {
        this.onmessage?.({ data: JSON.stringify(event) });
    }
}

/** Let the client's open handler (which awaits image conversion) run. */
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

beforeEach(() => {
    vi.unstubAllGlobals();
    FakeSocket.last = null;
    vi.stubGlobal("WebSocket", FakeSocket);
});

describe("requestPromptChat", () => {
    it("opens the one-shot socket and names no session", async () => {
        const pending = requestPromptChat(config(), { prompt: "describe a teapot" });
        await settle();

        const socket = FakeSocket.last;
        if (!socket) throw new Error("no socket was opened");
        expect(socket.url).toBe("ws://127.0.0.1:8190/api/agent/once");
        const sent = JSON.parse(socket.sent[0]) as Record<string, unknown>;
        expect(sent.prompt).toBe("describe a teapot");
        // No session, ever: the whole point of the route is that it cannot reach a canvas's agent.
        expect(sent.session).toBeUndefined();

        socket.emit({ type: "delta", text: "an answer" });
        socket.emit({ type: "done", text: "an answer" });
        await expect(pending).resolves.toBe("an answer");
    });

    it("sends a reference image as the data URL it already is", async () => {
        const pending = requestPromptChat(config(), { prompt: "what is this", images: [reference("a.png")] });
        await settle();

        const sent = JSON.parse(FakeSocket.last!.sent[0]) as { images?: string[] };
        expect(sent.images).toEqual(["data:image/png;base64,AAAA"]);

        FakeSocket.last!.emit({ type: "done", text: "" });
        await pending.catch(() => undefined);
    });

    it("reports the answer as it arrives", async () => {
        const seen: string[] = [];
        const pending = requestPromptChat(config(), { prompt: "hi" }, { onDelta: (text) => seen.push(text) });
        await settle();

        FakeSocket.last!.emit({ type: "delta", text: "half " });
        FakeSocket.last!.emit({ type: "delta", text: "way" });
        FakeSocket.last!.emit({ type: "done", text: "half way" });

        await expect(pending).resolves.toBe("half way");
        expect(seen).toEqual(["half ", "half way"]);
    });

    it("relays the agent's own error", async () => {
        const pending = requestPromptChat(config(), { prompt: "hi" });
        await settle();

        FakeSocket.last!.emit({ type: "error", message: "model_set: model not found: 9" });

        await expect(pending).rejects.toThrow("model_set: model not found: 9");
    });
});
