import i18n from "@/i18n";
import { dataUrlToFile } from "@/lib/image-utils";
import { gatewayProtocolRoot } from "@/services/api/runs";
import { apiErrorMessage } from "@/services/api/http";
import { imageToDataUrl } from "@/services/image-storage";
import { useConfigStore, type AgentModel, type AiConfig } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";

const apiText = (key: string, options?: Record<string, unknown>) => i18n.t(`apiErrors.${key}`, options);

/**
 * The agent, as one stream.
 *
 * A prompt and a command are the same thing to AlayaCore — frames on its stdin — so there is one
 * request: what we send is written to it (`command` + `args`, or `prompt` + images), and what it says
 * comes back here. This file only transcribes; it decides nothing about the content.
 */
type AgentEvent = {
    type: "models" | "delta" | "reasoning" | "model" | "reasoning_level" | "command" | "done" | "error";
    text?: string;
    models?: AgentModel[];
    active?: AgentModel;
    level?: number;
    coreVersion?: string;
    ok?: boolean;
    message?: string;
};

/**
 * Connect: start an agent and keep it, so the model list is on screen before anything is asked.
 *
 * `canvasId` is the name the process is filed under. It is the canvas's own id, which is what makes a
 * reload find the agent it left instead of starting a second one — the gateway holds one process per
 * name and keeps it until it is told to let go.
 */
export async function requestAgentConnect(config: AiConfig, canvasId: string, options?: { signal?: AbortSignal }): Promise<void> {
    // The same root every other service uses: the channel address without its legacy `/v1`.
    const root = gatewayProtocolRoot(config.baseUrl);
    try {
        const formData = new FormData();
        formData.set("session", canvasId);
        const response = await fetch(`${root}/api/agent/connect`, { method: "POST", headers: { Authorization: `Bearer ${config.apiKey}` }, body: formData, signal: options?.signal });
        if (!response.ok) throw new Error(apiText("requestFailed"));
        const payload = (await response.json()) as { sessionId?: string; state?: AgentEvent };
        if (payload.state) rememberAgentEvent(payload.state);
        useConfigStore.getState().setAgentSession(payload.sessionId || null);
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
    }
}

type AgentRequest = {
    prompt?: string;
    images?: ReferenceImage[];
    /** One of AlayaCore's own command names, forwarded verbatim. */
    command?: string;
    args?: string;
};

/**
 * Ask the agent a question that the canvas will follow up on.
 *
 * Names the connected session, so the answer comes from the process that holds this canvas's history
 * and can use its tools. Only the canvas agent panel calls this.
 */
export async function requestAgentChat(config: AiConfig, request: AgentRequest, options?: { signal?: AbortSignal; onEvent?: (event: AgentEvent) => void }): Promise<string> {
    return streamAgent(config, "/api/agent/chat", request, useConfigStore.getState().agentSession, options);
}

/**
 * Ask for one answer that nothing follows: a text node's body, a rewritten prompt, a caption.
 *
 * The session is passed as a literal null, not looked up — a one-shot question has no business
 * reaching the agent a canvas is holding, whose history is the user's work. The gateway keeps the two
 * apart the same way: `/api/agent/once` spawns a process for this request and releases it.
 */
export async function requestAgentOnce(config: AiConfig, request: AgentRequest, options?: { signal?: AbortSignal; onEvent?: (event: AgentEvent) => void }): Promise<string> {
    return streamAgent(config, "/api/agent/once", request, null, options);
}

/**
 * Run one agent request and read its stream.
 *
 * Returns the answer text as the deltas assembled it (empty for a command), and reports every event
 * so a caller can render the model list, the current model, or a refusal as they arrive.
 */
async function streamAgent(config: AiConfig, path: string, request: AgentRequest, session: string | null, options?: { signal?: AbortSignal; onEvent?: (event: AgentEvent) => void }): Promise<string> {
    // The same root every other service uses: the channel address without its legacy `/v1`.
    const root = gatewayProtocolRoot(config.baseUrl);

    const formData = new FormData();
    if (request.prompt) formData.set("prompt", request.prompt);
    if (request.command) formData.set("command", request.command);
    if (request.args !== undefined) formData.set("args", request.args);
    if (session) formData.set("session", session);
    const files = await Promise.all((request.images || []).map(async (image) => dataUrlToFile({ ...image, dataUrl: await imageToDataUrl(image) })));
    files.forEach((file, index) => formData.append(index === 0 ? "image" : `image${index + 1}`, file));

    let answer = "";
    let failure = "";
    try {
        const response = await fetch(`${root}${path}`, { method: "POST", headers: { Authorization: `Bearer ${config.apiKey}` }, body: formData, signal: options?.signal });
        if (!response.ok) throw new Error(apiText("requestFailed"));
        if (!response.body) throw new Error(apiText("requestFailed"));

        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        for (;;) {
            const { done, value } = await reader.read();
            if (done) break;
            buffer += decoder.decode(value, { stream: true });
            // EventSource framing: one `data: {...}` per line, a blank line between events.
            for (;;) {
                const index = buffer.indexOf("\n\n");
                if (index < 0) break;
                const block = buffer.slice(0, index);
                buffer = buffer.slice(index + 2);
                const payload = block.replace(/^data: /, "").trim();
                if (!payload) continue;
                const event = JSON.parse(payload) as AgentEvent;
                rememberAgentEvent(event);
                if (event.type === "delta") answer += event.text || "";
                // The agent's own sentence is the failure: relaying it is all this layer does.
                if (event.type === "error" && event.message) failure = event.message;
                options?.onEvent?.(event);
            }
        }
        if (!answer && failure) throw new Error(failure);
        return answer;
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
    }
}

/**
 * Keep the canvas's picture of the agent current: the list, the active model, the level.
 *
 * The agent announces these itself, so there is nothing to fetch and nothing to cache on the gateway;
 * whatever it last said is what the selects show.
 */
function rememberAgentEvent(event: AgentEvent) {
    const { updateAgent } = useConfigStore.getState();
    switch (event.type) {
        case "models":
            updateAgent({ models: event.models || [], active: event.active, level: event.level, coreVersion: event.coreVersion });
            break;
        case "model":
            if (event.active) updateAgent({ active: event.active });
            break;
        case "reasoning_level":
            if (typeof event.level === "number") updateAgent({ level: event.level });
            break;
    }
}
