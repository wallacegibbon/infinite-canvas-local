import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * The words the canvas asks the agent for.
 *
 * A text node's body is written by *that node's session* — the agent its panel connected, on the model
 * the user chose there. It is one question on an existing socket: no second connection, no second
 * process, and nothing that could reach the canvas's own agent.
 */
vi.mock("@/i18n", () => ({ default: { t: (key: string) => key } }));
vi.mock("@/services/api/gateway-status", () => ({ noteGatewayError: () => undefined }));
vi.mock("@/services/image-storage", () => ({ imageToDataUrl: async () => "data:image/png;base64,AAAA" }));

const { openAgentSession, agentSession, endAgentSession } = await import("@/services/api/agent");
const { requestPromptChat } = await import("@/services/api/prompts");
type Config = Parameters<typeof openAgentSession>[0];

const config = () =>
    ({
        baseUrl: "http://127.0.0.1:8190",
        apiKey: "local",
        channels: [],
        models: [],
        model: "text-qwen",
        systemPrompt: "",
    }) as unknown as Config;

const reference = (name: string) => ({ id: name, name, dataUrl: "data:image/png;base64,AAAA" }) as never;

/** The socket the client opens, standing in for the browser's. */
class FakeSocket {
    static last: FakeSocket | null = null;
    static count = 0;
    // The real class carries these; the client compares against them.
    static readonly CONNECTING = 0;
    static readonly OPEN = 1;
    static readonly CLOSING = 2;
    static readonly CLOSED = 3;

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
        FakeSocket.count++;
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

    emit(event: unknown) {
        this.onmessage?.({ data: JSON.stringify(event) });
    }

    lastSent() {
        return JSON.parse(this.sent[this.sent.length - 1] || "{}") as Record<string, unknown>;
    }
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

/** Open a session the way a panel does: dial, then the agent's startup broadcast arrives. */
async function connectedSession(name: string) {
    const opening = openAgentSession(config(), name);
    await settle();
    FakeSocket.last!.emit({ type: "models", models: [{ id: 1, name: "Fake / one" }], active: { id: 1, name: "Fake / one" }, level: 1 });
    await opening;
    return FakeSocket.last!;
}

beforeEach(() => {
    vi.unstubAllGlobals();
    FakeSocket.last = null;
    FakeSocket.count = 0;
    vi.stubGlobal("WebSocket", FakeSocket);
});

describe("requestPromptChat", () => {
    it("asks the session it is given, and opens nothing of its own", async () => {
        const socket = await connectedSession("node:one");
        const before = FakeSocket.count;

        const pending = requestPromptChat("node:one", config(), { prompt: "describe a teapot" });
        await settle();

        // One socket for the session, and the question went on it.
        expect(FakeSocket.count).toBe(before);
        expect(socket.lastSent().prompt).toBe("describe a teapot");

        socket.emit({ type: "delta", text: "an answer" });
        socket.emit({ type: "done" });
        await expect(pending).resolves.toBe("an answer");
    });

    it("carries a reference image as the data URL it already is", async () => {
        const socket = await connectedSession("node:two");

        const pending = requestPromptChat("node:two", config(), { prompt: "what is this", images: [reference("a.png")] });
        await settle();

        expect(socket.lastSent().images).toEqual(["data:image/png;base64,AAAA"]);

        socket.emit({ type: "done" });
        await pending;
    });

    it("reports the answer as it arrives", async () => {
        const socket = await connectedSession("node:three");

        const seen: string[] = [];
        const pending = requestPromptChat("node:three", config(), { prompt: "hi" }, { onDelta: (text) => seen.push(text) });
        await settle();

        socket.emit({ type: "delta", text: "half " });
        socket.emit({ type: "delta", text: "way" });
        socket.emit({ type: "done" });

        await expect(pending).resolves.toBe("half way");
        expect(seen).toEqual(["half ", "half way"]);
    });

    it("relays the agent's own error", async () => {
        const socket = await connectedSession("node:four");

        const pending = requestPromptChat("node:four", config(), { prompt: "hi" });
        await settle();

        socket.emit({ type: "error", message: "model_set: model not found: 9" });

        await expect(pending).rejects.toThrow("model_set: model not found: 9");
    });
});

describe("agent sessions", () => {
    it("keeps each session's model to itself", async () => {
        const first = await connectedSession("node:five");
        first.emit({ type: "model", active: { id: 2, name: "Fake / two" } });

        const second = await connectedSession("node:six");

        expect(agentSession("node:five").active?.name).toBe("Fake / two");
        // The second session has its own process and its own model: nothing leaks between them.
        expect(agentSession("node:six").active?.name).toBe("Fake / one");
        expect(second.url).toContain("/api/agent/socket/node%3Asix");
    });

    it("releases the session on request, by asking the agent to end it", async () => {
        const socket = await connectedSession("node:seven");
        endAgentSession("node:seven");
        expect(socket.lastSent().close).toBe(true);
    });
});
