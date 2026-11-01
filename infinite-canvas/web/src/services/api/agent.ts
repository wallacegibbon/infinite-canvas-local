import i18n from "@/i18n";
import { gatewayProtocolRoot } from "@/services/api/runs";
import { apiErrorMessage } from "@/services/api/http";
import { imageToDataUrl } from "@/services/image-storage";
import { useConfigStore, type AgentModel, type AiConfig } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";

const apiText = (key: string, options?: Record<string, unknown>) => i18n.t(`apiErrors.${key}`, options);

/**
 * The agent, as two routes with very different lifetimes.
 *
 * A conversation is a socket: one connection per canvas, both directions, open for as long as the panel
 * is. A one-shot question — a text node's body, a rewritten prompt, a caption — is a plain POST that
 * spawns a process of its own and releases it when the answer ends, which is why it cannot name a
 * session and never reaches the agent a canvas is holding.
 */
export type AgentEvent = {
    type: "models" | "delta" | "reasoning" | "model" | "reasoning_level" | "command" | "tool_call" | "tool_result" | "done" | "error" | "closed";
    text?: string;
    elapsedMs?: number;
    models?: AgentModel[];
    active?: AgentModel;
    level?: number;
    coreVersion?: string;
    ok?: boolean;
    message?: string;
    callId?: string;
    name?: string;
    input?: string;
    output?: string;
};

/** What the canvas says on the socket. One of them per message. */
type AgentMessage = {
    prompt?: string;
    images?: string[];
    command?: string;
    args?: string;
    cancel?: boolean;
    close?: boolean;
};

/**
 * The one open conversation.
 *
 * A module-level value rather than React state on purpose: the socket is a resource with a lifetime,
 * and a re-render that closed and reopened it would drop whatever the agent said in between.
 */
type Conversation = {
    url: string;
    socket: WebSocket;
};

let conversation: Conversation | null = null;

/**
 * Who is listening. Module-level and not per-connection on purpose: a panel subscribes when it mounts,
 * which is *before* any socket exists, and a subscription that died with a connection would have to be
 * re-made by every component at a moment it cannot know about.
 */
const listeners = new Set<(event: AgentEvent) => void>();

/** Where a canvas's conversation lives. The canvas id is the name the gateway files the agent under. */
function agentSocketUrl(config: AiConfig, canvasId: string): string {
    const root = gatewayProtocolRoot(config.baseUrl);
    return `${root.replace(/^http/i, "ws")}/api/agent/socket/${encodeURIComponent(canvasId)}`;
}

/** Subscribe to everything the agent says. Returns the unsubscribe. */
export function onAgentEvent(listener: (event: AgentEvent) => void): () => void {
    listeners.add(listener);
    return () => listeners.delete(listener);
}

/**
 * Open the conversation, or return the one already open.
 *
 * Resolves on the agent's startup broadcast, which is where the model list comes from, so a caller that
 * awaits this can show the models it may ask about.
 */
export function connectAgentSocket(config: AiConfig, canvasId: string): Promise<void> {
    const url = agentSocketUrl(config, canvasId);
    if (conversation?.url === url && conversation.socket.readyState <= WebSocket.OPEN) {
        return Promise.resolve();
    }
    closeAgentSocket();

    return new Promise((resolve, reject) => {
        let socket: WebSocket;
        try {
            socket = new WebSocket(url);
        } catch (error) {
            reject(new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout")));
            return;
        }
        const opened: Conversation = { url, socket };
        conversation = opened;

        let greeted = false;
        socket.onmessage = (message) => {
            let event: AgentEvent;
            try {
                event = JSON.parse(message.data as string) as AgentEvent;
            } catch {
                return;
            }
            rememberAgentEvent(event);
            for (const listener of listeners) listener(event);
            const id = canvasId;
            if (!greeted && event.type === "models") {
                greeted = true;
                useConfigStore.getState().setAgentSession(id);
                resolve();
            }
            if (event.type === "error" && event.message) {
                if (!greeted) {
                    greeted = true;
                    reject(new Error(event.message));
                }
            }
        };
        socket.onerror = () => {
            if (!greeted) {
                greeted = true;
                reject(new Error(apiText("requestFailed")));
            }
        };
        socket.onclose = () => {
            if (conversation === opened) conversation = null;
            useConfigStore.getState().setAgentSession(null);
            for (const listener of listeners) listener({ type: "closed" });
            if (!greeted) {
                greeted = true;
                reject(new Error(apiText("requestFailed")));
            }
        };
    });
}

/** Say something on the conversation. Silently does nothing when there is none — a message with no
 * connection is not an error to report, it is a panel that has not been opened. */
export function sendAgentMessage(message: AgentMessage): void {
    if (!conversation || conversation.socket.readyState !== WebSocket.OPEN) return;
    conversation.socket.send(JSON.stringify(message));
}

/** Stop the answer that is being written. The agent keeps its history; it only stops working. */
export function cancelAgentTurn(): void {
    sendAgentMessage({ cancel: true });
}

/** Close the socket but keep the agent: this is the tab going away, not the user giving up. */
export function closeAgentSocket(): void {
    const open = conversation;
    conversation = null;
    if (open) {
        open.socket.onclose = null;
        open.socket.close();
    }
    useConfigStore.getState().setAgentSession(null);
}

/**
 * End the conversation for good: the agent is released, history and all.
 *
 * The socket is not closed here — the gateway closes it after releasing the process, and closing it
 * locally first would race the message out of the buffer, leaving an agent with nothing to end it.
 */
export function endAgentConversation(): void {
    sendAgentMessage({ close: true });
}

/**
 * Ask for one answer that nothing follows: a text node's body, a rewritten prompt, a caption.
 *
 * A socket that carries one question and dies with the answer. It names no session and never reads
 * one, which is the whole point: this must not be able to reach the agent a canvas is holding. The
 * conversation and the one-shot speak the same messages, because there is one transport here — only
 * the lifetime differs, and the route says which.
 */
export function requestAgentOnce(config: AiConfig, request: { prompt: string; images?: ReferenceImage[] }, options?: { signal?: AbortSignal; onEvent?: (event: AgentEvent) => void }): Promise<string> {
    const url = `${gatewayProtocolRoot(config.baseUrl).replace(/^http/i, "ws")}/api/agent/once`;
    return new Promise((resolve, reject) => {
        let socket: WebSocket;
        try {
            socket = new WebSocket(url);
        } catch (error) {
            reject(new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout")));
            return;
        }
        let answer = "";
        let failure = "";
        let settled = false;
        const finish = (error?: Error) => {
            if (settled) return;
            settled = true;
            socket.onmessage = null;
            socket.onerror = null;
            socket.onclose = null;
            socket.close();
            if (error) reject(error);
            else resolve(answer);
        };

        options?.signal?.addEventListener("abort", () => finish(new Error(apiText("requestFailed"))), { once: true });

        socket.onopen = async () => {
            const images = await Promise.all((request.images || []).map((image) => imageToDataUrl(image)));
            if (settled) return;
            socket.send(JSON.stringify({ prompt: request.prompt, images }));
        };
        socket.onmessage = (message) => {
            let event: AgentEvent;
            try {
                event = JSON.parse(message.data as string) as AgentEvent;
            } catch {
                return;
            }
            rememberAgentEvent(event);
            if (event.type === "delta") answer += event.text || "";
            // The agent's own sentence is the failure: relaying it is all this layer does.
            if (event.type === "error" && event.message) failure = event.message;
            options?.onEvent?.(event);
            if (event.type === "error") finish(new Error(failure || apiText("requestFailed")));
            if (event.type === "done") finish();
        };
        socket.onerror = () => finish(new Error(apiText("requestFailed")));
        socket.onclose = () => finish(new Error(failure || apiText("requestFailed")));
    });
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
