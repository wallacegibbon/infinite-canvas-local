import i18n from "@/i18n";
import { gatewayProtocolRoot } from "@/services/api/runs";
import { apiErrorMessage } from "@/services/api/http";
import { imageToDataUrl } from "@/services/image-storage";
import type { AiConfig } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";

const apiText = (key: string, options?: Record<string, unknown>) => i18n.t(`apiErrors.${key}`, options);

/** One entry of the agent's own model list (`SM model_list`). */
type AgentModel = { id: number; name: string; protocolType?: string; contextLimit?: number };

/**
 * The agent, as sessions.
 *
 * A session is one agent process with one socket, named by whatever surface owns it: the canvas panel
 * owns `canvas`, a node's panel owns `node:<id>`. Everything the agent knows about itself — which models
 * exist, which one this session is on, how hard it is thinking — belongs to that process and is only
 * ever read back from what it announces. Nothing here mirrors it, and nothing is shared between two
 * sessions: the processes are independent, they are small, and there are as many as there are surfaces.
 *
 * That is why there is a `connect` step at all: a session has to exist before its model can be chosen,
 * because the list of models comes from the agent itself.
 */
export type AgentEvent = {
    type: "models" | "delta" | "reasoning" | "model" | "reasoning_level" | "command" | "tool_call" | "tool_result" | "tool" | "done" | "error" | "closed";
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
    /** `tool`: what the agent wants run, as it sent it. The canvas is what runs it. */
    arguments?: unknown;
};

type AgentMessage = {
    prompt?: string;
    images?: string[];
    command?: string;
    args?: string;
    cancel?: boolean;
    close?: boolean;
    /** The canvas answering a `tool` event: what running it did. */
    toolResult?: { callId: string; text: string; isError?: boolean };
};

/** What one session is, right now — the agent's own last word about itself. */
export type AgentSession = {
    name: string;
    connected: boolean;
    models: AgentModel[];
    active?: AgentModel;
    level?: number;
};

type Turn = { answer: string; resolve: () => void; reject: (error: Error) => void };

type Live = {
    name: string;
    socket: WebSocket;
    opened: boolean;
    session: AgentSession;
    waiting?: Turn;
    opening: Promise<AgentSession>;
};

const sessions = new Map<string, Live>();

/**
 * Who is listening, by session name.
 *
 * Module-level and not held by the socket, because a panel subscribes when it mounts — before its
 * session exists, since the session is what the connect button starts. A registry that lived on the
 * socket would have nothing to attach to at that moment, and the panel would sit on a turn forever.
 */
const listeners = new Map<string, Set<(event: AgentEvent) => void>>();
function listenersOf(name: string) {
    let set = listeners.get(name);
    if (!set) {
        set = new Set();
        listeners.set(name, set);
    }
    return set;
}
/** Whoever wants every session's events, tools being the one thing that does not care who asked. */
const everyone = new Set<(name: string, event: AgentEvent) => void>();
const watchers = new Set<() => void>();
let revision = 0;

// A session that has never been opened must hand React the *same* object every time it is asked, or a
// snapshot comparison sees a change on every check and re-renders forever.
const blanks = new Map<string, AgentSession>();
function blank(name: string): AgentSession {
    const existing = blanks.get(name);
    if (existing) return existing;
    const created: AgentSession = { name, connected: false, models: [] };
    blanks.set(name, created);
    return created;
}

function notify() {
    revision++;
    for (const watcher of watchers) watcher();
}

/** React reads sessions through this: any change anywhere bumps the revision. */
export function subscribeAgentSessions(watcher: () => void): () => void {
    watchers.add(watcher);
    return () => watchers.delete(watcher);
}

/** What a session looks like now, or a disconnected one that has never been opened. */
export function agentSession(name: string): AgentSession {
    return sessions.get(name)?.session || blank(name);
}

/** The canvas panel's session, and one per node panel. */
export const CANVAS_AGENT = "canvas";
export const nodeAgent = (nodeId: string) => `node:${nodeId}`;

function socketUrl(config: AiConfig, name: string): string {
    const root = gatewayProtocolRoot(config.baseUrl);
    return `${root.replace(/^http/i, "ws")}/api/agent/socket/${encodeURIComponent(name)}`;
}

/**
 * Open a session, or return the one already open under this name.
 *
 * Resolves on the agent's startup broadcast, which is where the model list comes from — the caller
 * needs it before it can show, or let the user choose, a model for this session.
 */
export function openAgentSession(config: AiConfig, name: string): Promise<AgentSession> {
    const existing = sessions.get(name);
    if (existing) return existing.opening;

    let socket: WebSocket;
    try {
        socket = new WebSocket(socketUrl(config, name));
    } catch (error) {
        return Promise.reject(new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout")));
    }
    const live: Live = {
        name,
        socket,
        opened: false,
        session: blank(name),
        opening: Promise.resolve(blank(name)),
    };
    sessions.set(name, live);

    live.opening = new Promise<AgentSession>((resolve, reject) => {
        socket.onmessage = (message) => {
            let event: AgentEvent;
            try {
                event = JSON.parse(message.data as string) as AgentEvent;
            } catch {
                return;
            }
            adopt(live, event);

            // The turn a caller is waiting for, whichever surface asked.
            if (event.type === "delta" && live.waiting) live.waiting.answer += event.text || "";
            if (event.type === "done" && live.waiting) {
                const turn = live.waiting;
                live.waiting = undefined;
                turn.resolve();
            }
            if (event.type === "error" && live.waiting) {
                const turn = live.waiting;
                live.waiting = undefined;
                turn.reject(new Error(event.message || apiText("requestFailed")));
            }

            if (!live.opened) {
                live.opened = true;
                if (event.type === "models") {
                    live.session.connected = true;
                    resolve(live.session);
                } else if (event.type === "error") {
                    sessions.delete(name);
                    socket.close();
                    reject(new Error(event.message || apiText("requestFailed")));
                    return;
                }
            }
            for (const listener of listenersOf(name)) listener(event);
            for (const listener of everyone) listener(name, event);
            notify();
        };
        socket.onerror = () => {
            if (live.opened) return;
            live.opened = true;
            sessions.delete(name);
            reject(new Error(apiText("requestFailed")));
        };
        socket.onclose = () => {
            const wasOpen = live.opened;
            live.opened = false;
            live.session = blank(name);
            if (sessions.get(name) === live) sessions.delete(name);
            live.waiting?.reject(new Error(apiText("requestFailed")));
            live.waiting = undefined;
            for (const listener of listenersOf(name)) listener({ type: "closed" });
            notify();
            if (!wasOpen) reject(new Error(apiText("requestFailed")));
        };
    });
    return live.opening;
}

/**
 * Keep the session's own picture current: the agent announces these, nobody else sets them.
 *
 * The object is replaced rather than mutated, because its identity is what tells React something
 * changed. A mutation would update the display of whoever read it first and nobody else.
 */
function adopt(live: Live, event: AgentEvent) {
    switch (event.type) {
        case "models":
            live.session = { ...live.session, connected: true, models: event.models || [], active: event.active, level: event.level };
            break;
        case "model":
            if (event.active) live.session = { ...live.session, active: event.active };
            break;
        case "reasoning_level":
            if (typeof event.level === "number") live.session = { ...live.session, level: event.level };
            break;
    }
}

/** Subscribe to one session's events. Works before the session exists, which is the point. */
export function onAgentEvent(name: string, listener: (event: AgentEvent) => void): () => void {
    const set = listenersOf(name);
    set.add(listener);
    return () => {
        set.delete(listener);
    };
}

/**
 * Subscribe to every session's events, with the name they arrived on.
 *
 * The canvas is one, and a tool call is the page doing something to itself, so the surfaces that run
 * tools do not care which agent asked — they answer on the name they were given. A subscription per
 * session would have to be made for sessions that do not exist yet, and for the ones that never will.
 */
export function onAnyAgentEvent(listener: (name: string, event: AgentEvent) => void): () => void {
    everyone.add(listener);
    return () => {
        everyone.delete(listener);
    };
}

/** Say something on a session. */
export function sendAgentMessage(name: string, message: AgentMessage): boolean {
    const live = sessions.get(name);
    if (!live || live.socket.readyState !== WebSocket.OPEN) return false;
    live.socket.send(JSON.stringify(message));
    return true;
}

/**
 * Ask one question on a session and wait for the turn to end, keeping the answer the deltas built.
 *
 * This is what a rewrite, a caption and a text node's body all are: one question on the session the
 * surface already has. Nothing here opens a second connection or a second process.
 */
async function askAgentSession(name: string, request: { prompt: string; images?: ReferenceImage[] }, options?: { signal?: AbortSignal; onEvent?: (event: AgentEvent) => void }): Promise<string> {
    const live = sessions.get(name);
    if (!live || live.socket.readyState !== WebSocket.OPEN) throw new Error(apiText("requestFailed"));

    const images = await Promise.all((request.images || []).map((image) => imageToDataUrl(image)));
    const listener = options?.onEvent;
    const registry = listener ? listenersOf(name) : undefined;
    if (listener && registry) registry.add(listener);

    try {
        return await new Promise<string>((resolve, reject) => {
            const turn: Turn = { answer: "", resolve: () => resolve(turn.answer), reject };
            live.waiting = turn;
            options?.signal?.addEventListener(
                "abort",
                () => {
                    if (live.waiting === turn) live.waiting = undefined;
                    cancelAgentSession(name);
                    reject(new Error(apiText("requestFailed")));
                },
                { once: true },
            );
            if (!sendAgentMessage(name, { prompt: request.prompt, images })) {
                live.waiting = undefined;
                reject(new Error(apiText("requestFailed")));
            }
        });
    } finally {
        if (listener && registry) registry.delete(listener);
    }
}

/**
 * Ask one question on a session that exists for that answer alone, and release the agent afterwards.
 *
 * This is what a node panel's agent is: connect starts it, the model is chosen on it, one answer comes
 * back, and it is gone. Asking again means pressing connect again — which is honest, because a second
 * answer would otherwise be a second turn in a conversation nobody asked to continue.
 */
export async function askOnceAgentSession(name: string, request: { prompt: string; images?: ReferenceImage[] }, options?: { signal?: AbortSignal; onEvent?: (event: AgentEvent) => void }): Promise<string> {
    try {
        return await askAgentSession(name, request, options);
    } finally {
        endAgentSession(name);
    }
}

/** Stop the answer being written. The session keeps its history. */
export function cancelAgentSession(name: string): void {
    sendAgentMessage(name, { cancel: true });
}

/** Close the socket and keep the agent: the window went away, not the conversation. */
export function closeAgentSession(name: string): void {
    const live = sessions.get(name);
    if (!live) return;
    sessions.delete(name);
    live.socket.onclose = null;
    live.socket.close();
    live.session = blank(name);
    notify();
}

/** End the session for good: the agent is released, history and all. */
export function endAgentSession(name: string): void {
    if (!sendAgentMessage(name, { close: true })) {
        closeAgentSession(name);
        return;
    }
    // The gateway closes the socket after releasing. Drop this side shortly after either way, so a dead
    // session cannot be mistaken for a live one.
    setTimeout(() => closeAgentSession(name), 1000);
}
