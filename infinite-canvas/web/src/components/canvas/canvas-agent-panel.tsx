import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { ArrowUp, Bot, LoaderCircle, Square, Wrench, X } from "lucide-react";

import { cn } from "@/lib/utils";
import { canvasThemes } from "@/lib/canvas-theme";
import { CANVAS_DIALOG_CONTENT } from "@/lib/canvas-overlay";
import { useCanvasTheme } from "@/lib/canvas/use-canvas-theme";
import { AgentSettingsControls } from "./agent-settings-controls";
import { cancelAgentSession, canvasAgent, closeAgentSession, endAgentSession, onAgentEvent, openAgentSession, sendAgentMessage, type AgentEvent } from "@/services/api/agent";
import { useCanvasAgentStore } from "@/stores/canvas/use-canvas-agent-store";
import { useEffectiveConfig } from "@/stores/use-config-store";
import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";

/**
 * The canvas agent, as a card that floats above the canvas instead of living in it.
 *
 * It is not a node: it does not pan, it does not zoom, it is not selected, exported or garbage
 * collected with the content. That is what a conversation is — a surface for working *on* the canvas,
 * not another thing on it. It carries `data-canvas-overlay` so the wheel and the drag stop at its edge
 * (see `lib/canvas-overlay.ts`), and its position is the window's, not the document's.
 */

type ToolStep = { callId: string; name: string; input?: string; output?: string };

type Message = {
    id: string;
    role: "user" | "assistant" | "error";
    text: string;
    reasoning: string;
    tools: ToolStep[];
    /** An assistant message is still being written until the agent says the turn ended. */
    streaming: boolean;
};

type Props = {
    canvasId: string;
    nodes: CanvasNodeData[];
    onFocusNode: (nodeId: string) => void;
    /** Ask the canvas to let the user click the node to cite. Resolves with the node, or null if cancelled. */
    onPickNode: () => Promise<string | null>;
};

const NODE_LABEL: Record<string, string> = {
    [CanvasNodeType.Image]: "image",
    [CanvasNodeType.Video]: "video",
    [CanvasNodeType.Audio]: "audio",
    [CanvasNodeType.Text]: "text",
    [CanvasNodeType.Config]: "config",
};

let messageSeq = 0;
const nextId = () => `m${Date.now().toString(36)}${(messageSeq++).toString(36)}`;

export function CanvasAgentPanel({ canvasId, nodes, onFocusNode, onPickNode }: Props) {
    const { t } = useTranslation();
    const theme = useCanvasTheme();
    const config = useEffectiveConfig();
    const open = useCanvasAgentStore((state) => state.open);
    const position = useCanvasAgentStore((state) => state.position);
    const size = useCanvasAgentStore((state) => state.size);
    const setSize = useCanvasAgentStore((state) => state.setSize);
    const setOpen = useCanvasAgentStore((state) => state.setOpen);
    const setPosition = useCanvasAgentStore((state) => state.setPosition);

    /** This canvas's conversation, by name. It is also the name of the file the agent keeps it in. */
    const session = useMemo(() => canvasAgent(canvasId), [canvasId]);

    const [messages, setMessages] = useState<Message[]>([]);
    const [draft, setDraft] = useState("");
    const [status, setStatus] = useState<"idle" | "connecting" | "connected" | "closed">("idle");
    const [busy, setBusy] = useState(false);
    /** The nodes this draft mentions, by id. They travel with the message as context. */
    const [cited, setCited] = useState<string[]>([]);

    const scrollRef = useRef<HTMLDivElement | null>(null);
    const inputRef = useRef<HTMLTextAreaElement | null>(null);
    const dragRef = useRef<{ dx: number; dy: number } | null>(null);
    const cardRef = useRef<HTMLDivElement | null>(null);

    // The conversation is the socket, and the socket belongs to this panel's lifetime, not to a render.
    const connect = useCallback(async () => {
        setStatus((current) => (current === "connected" ? current : "connecting"));
        try {
            await openAgentSession(config, session);
            setStatus("connected");
        } catch (error) {
            setStatus("closed");
            setMessages((current) => [...current, { id: nextId(), role: "error", text: error instanceof Error ? error.message : t("canvas.agent.connectFailed"), reasoning: "", tools: [], streaming: false }]);
        }
    }, [canvasId, config, t]);

    // The panel opens the conversation, and it is the only thing that does. The ref keeps the effect off
    // the render: `config` is a fresh object every time, so depending on `connect` would close and reopen
    // the socket on every keystroke elsewhere on the canvas.
    const connectRef = useRef(connect);
    connectRef.current = connect;

    useEffect(() => {
        if (!open) return;
        void connectRef.current();
        // Closing the panel closes the socket and leaves the agent: the tab going away is not the user
        // giving up on the conversation, and reopening it finds the same history.
        return () => closeAgentSession(session);
    }, [open, session]);

    useEffect(() => onAgentEvent(session, applyEvent), [session]);

    const applyEvent = (event: AgentEvent) => {
        if (event.type === "history") {
            // The agent has just said what its conversation is. That is the whole truth about this
            // panel's past — a reload left nothing on screen, and a reopened panel may have been
            // showing a copy that the file has since moved past — so it replaces, and it does not append.
            setMessages(
                (event.history || []).map((entry) => ({
                    id: nextId(),
                    role: entry.role,
                    text: entry.text,
                    reasoning: "",
                    tools: [],
                    streaming: false,
                })),
            );
            return;
        }
        if (event.type === "closed") {
            setStatus("closed");
            setBusy(false);
            setMessages((current) => current.map((message) => (message.streaming ? { ...message, streaming: false } : message)));
            return;
        }
        if (event.type === "error") {
            setMessages((current) => [...current, { id: nextId(), role: "error", text: event.message || "", reasoning: "", tools: [], streaming: false }]);
            return;
        }
        if (event.type === "done") {
            setBusy(false);
            setMessages((current) => current.map((message) => (message.streaming ? { ...message, streaming: false } : message)));
            return;
        }
        if (event.type === "delta" || event.type === "reasoning" || event.type === "tool_call" || event.type === "tool_result") {
            setMessages((current) => {
                const last = current[current.length - 1];
                if (!last || last.role !== "assistant") return current;
                const updated = { ...last };
                if (event.type === "delta") updated.text = last.text + (event.text || "");
                else if (event.type === "reasoning") updated.reasoning = last.reasoning + (event.text || "");
                else if (event.type === "tool_call") {
                    // A call arrives in frames, not in one message: the name first, so the step can be
                    // shown while the tool runs, then the input as it fills in. One step per call,
                    // updated — a step per frame would show the same call twice.
                    const callId = event.callId || "";
                    // A name and an input are each their own fragment; an empty one says nothing.
                    const merge = (step: ToolStep): ToolStep => ({
                        ...step,
                        ...(event.name ? { name: event.name } : {}),
                        ...(event.input ? { input: event.input } : {}),
                    });
                    updated.tools = last.tools.some((step) => step.callId === callId) ? last.tools.map((step) => (step.callId === callId ? merge(step) : step)) : [...last.tools, merge({ callId, name: "tool" })];
                } else updated.tools = last.tools.map((step) => (step.callId === event.callId ? { ...step, output: event.output } : step));
                return [...current.slice(0, -1), updated];
            });
        }
    };

    const submit = async () => {
        const text = draft.trim();
        if (!text || busy) return;
        // Sending into a socket that is not open would drop the message and leave the panel stuck on a
        // turn that never ends, so the conversation is opened (or reopened) first. It is idempotent.
        try {
            await connectRef.current();
        } catch {
            return;
        }
        const context = cited.length ? `${t("canvas.agent.contextMarker")}\n${cited.map((id) => describeNode(nodes.find((node) => node.id === id))).join("\n")}\n\n` : "";
        setMessages((current) => [...current, { id: nextId(), role: "user", text, reasoning: "", tools: [], streaming: false }, { id: nextId(), role: "assistant", text: "", reasoning: "", tools: [], streaming: true }]);
        setDraft("");
        setCited([]);
        setBusy(true);
        sendAgentMessage(session, { prompt: context + text });
    };

    const closeSession = () => {
        endAgentSession(session);
        setStatus("closed");
        setBusy(false);
        setMessages([]);
    };

    // --- dragging ---------------------------------------------------------------------------------------
    // Pointer events are captured on the header, so a fast drag that leaves the card keeps moving it.
    const startDrag = (event: React.PointerEvent) => {
        const rect = cardRef.current?.getBoundingClientRect();
        if (!rect) return;
        dragRef.current = { dx: event.clientX - rect.left, dy: event.clientY - rect.top };
        setPosition({ x: rect.left, y: rect.top });
        (event.target as HTMLElement).setPointerCapture(event.pointerId);
    };
    const moveDrag = (event: React.PointerEvent) => {
        const drag = dragRef.current;
        if (!drag) return;
        const host = cardRef.current?.parentElement?.getBoundingClientRect();
        const width = cardRef.current?.offsetWidth || 380;
        const height = cardRef.current?.offsetHeight || 480;
        const maxX = (host?.width || window.innerWidth) - width - 8;
        const maxY = (host?.height || window.innerHeight) - height - 8;
        setPosition({ x: Math.min(Math.max(8, event.clientX - drag.dx), Math.max(8, maxX)), y: Math.min(Math.max(8, event.clientY - drag.dy), Math.max(8, maxY)) });
    };
    const endDrag = (event: React.PointerEvent) => {
        dragRef.current = null;
        (event.target as HTMLElement).releasePointerCapture?.(event.pointerId);
    };

    // --- size -------------------------------------------------------------------------------------------
    const width = size?.width ?? 380;
    const height = size?.height ?? 480;
    /**
     * Which corner is being pulled, and the room the panel had when the gesture started.
     *
     * `anchor` is the panel's explicit position, or null when it is sitting in its default corner (anchored by
     * its right and bottom edges) — a corner can only move the far one if there is a near one to hold still.
     */
    const resizeRef = useRef<{ corner: "top-left" | "bottom-right"; x: number; y: number; width: number; height: number; anchor: { x: number; y: number } | null } | null>(null);
    const startResize = (corner: "top-left" | "bottom-right") => (event: React.PointerEvent) => {
        event.preventDefault();
        event.stopPropagation();
        resizeRef.current = { corner, x: event.clientX, y: event.clientY, width, height, anchor: position };
        (event.target as HTMLElement).setPointerCapture?.(event.pointerId);
    };
    const moveResize = (event: React.PointerEvent) => {
        const grip = resizeRef.current;
        if (!grip) return;
        const host = cardRef.current?.parentElement?.getBoundingClientRect();
        // The panel is the window's: it may be made as big as the canvas it floats in, and no bigger.
        const limit = (value: number, min: number, max: number) => Math.min(Math.max(min, value), Math.max(min, max));
        const dx = event.clientX - grip.x;
        const dy = event.clientY - grip.y;
        // Pulling a corner towards the middle shrinks; from the top-left that means the opposite sign.
        const sign = grip.corner === "top-left" ? -1 : 1;
        const nextWidth = limit(grip.width + sign * dx, 320, (host?.width || window.innerWidth) - 16);
        const nextHeight = limit(grip.height + sign * dy, 280, (host?.height || window.innerHeight) - 16);
        setSize({ width: nextWidth, height: nextHeight });
        // With an explicit position the panel is pinned by its top-left corner, so moving that corner is moving
        // the panel: the opposite one stays where it is, which is what makes it look like the corner was pulled.
        if (grip.anchor && grip.corner === "top-left") setPosition({ x: grip.anchor.x + (grip.width - nextWidth), y: grip.anchor.y + (grip.height - nextHeight) });
    };
    const endResize = (event: React.PointerEvent) => {
        resizeRef.current = null;
        (event.target as HTMLElement).releasePointerCapture?.(event.pointerId);
    };

    // --- citations --------------------------------------------------------------------------------------
    /** The `@` a pick is answering, so the label lands where it was typed. */
    const pickAtRef = useRef<number>(-1);
    /** Where the caret belongs once the draft has been rewritten — a pick puts it after the label. */
    const caretRef = useRef<number | null>(null);

    /** Put the caret in the box at this position, which is also what takes the focus back from the canvas. */
    const focusAt = (caret: number | null) => {
        if (caret === null) {
            caretRef.current = null;
            return;
        }
        const input = inputRef.current;
        if (!input) return;
        caretRef.current = null;
        input.focus();
        input.setSelectionRange(caret, caret);
    };

    const cite = (nodeId: string) => {
        const at = pickAtRef.current;
        pickAtRef.current = -1;
        if (at < 0) return;
        const node = nodes.find((candidate) => candidate.id === nodeId);
        const label = node?.title || nodeId;
        // The `@` and the space around the label: the next thing typed belongs after it, not inside it.
        caretRef.current = at + label.length + 2;
        setDraft((current) => `${current.slice(0, at)}@${label} ${current.slice(at + 1)}`);
        setCited((current) => (current.includes(nodeId) ? current : [...current, nodeId]));
    };

    // The click happened on the canvas, so the box has to take the focus and the caret back once the draft is
    // in the DOM — the pick is a way of typing, and typing continues where it left off.
    useEffect(() => {
        if (caretRef.current === null) return;
        focusAt(caretRef.current);
    }, [draft]);

    /**
     * `@` is a request to pick a node on the canvas, the way a reference's + does — the user points at the
     * thing itself rather than at a name in a list. The character stays in the draft, where the label
     * replaces it once something is picked; a cancelled pick leaves the `@` as typed, and the box focused.
     */
    const onChange = (value: string) => {
        const caret = inputRef.current?.selectionStart ?? value.length;
        // Any `@` that just landed at the caret asks for a pick. It does not need a space in front of it: Chinese
        // text has no spaces to put there, and an `@` that was not meant as a citation is one escape away from
        // staying exactly as typed.
        const typed = value.length > draft.length && caret > 0 && value[caret - 1] === "@";
        setDraft(value);
        if (!typed) return;
        const at = caret - 1;
        pickAtRef.current = at;
        void onPickNode().then((nodeId) => {
            if (nodeId) cite(nodeId);
            else focusAt(at + 1);
        });
    };

    /**
     * Follow the newest message only while the reader is at the bottom.
     *
     * Someone who scrolled up to read is not asking to be dragged back down, and someone who scrolls back to
     * the end is asking for the follow to resume. The position is the truth and is read again on every
     * message, because a wheel gesture and a scroll event both have to survive a tab that is not drawing —
     * and because being at the bottom is the whole condition: there is no separate "follow mode" to get out
     * of step with where the reader actually is.
     */
    const followRef = useRef(true);
    const atBottom = (element: HTMLDivElement) => element.scrollHeight - element.scrollTop - element.clientHeight < 24;

    useEffect(() => {
        const element = scrollRef.current;
        if (!element) return;
        if (!followRef.current) {
            if (!atBottom(element)) return;
            followRef.current = true;
        }
        element.scrollTop = element.scrollHeight;
    }, [messages]);

    if (!open) return null;

    const style = position ? { left: position.x, top: position.y } : { right: 24, bottom: 24 };

    return (
        <div
            ref={cardRef}
            data-canvas-overlay=""
            className="absolute z-[95] flex max-w-[calc(100%-16px)] flex-col overflow-hidden rounded-2xl border shadow-2xl backdrop-blur"
            style={{ ...style, width, height, background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}
        >
            <div className="flex cursor-grab items-center gap-2 border-b px-3 py-2 active:cursor-grabbing" style={{ borderColor: theme.toolbar.border }} onPointerDown={startDrag} onPointerMove={moveDrag} onPointerUp={endDrag} onPointerCancel={endDrag}>
                <Bot className="size-4 shrink-0" style={{ color: theme.node.activeStroke }} />
                <span className="text-sm font-medium">{t("canvas.agent.title")}</span>
                <span className="truncate text-xs opacity-60">{status === "connected" ? t("canvas.agent.connected") : status === "connecting" ? t("canvas.agent.connecting") : t("canvas.agent.disconnected")}</span>
                {status === "connected" ? (
                    <button type="button" className="ml-auto rounded p-1 text-xs opacity-60 hover:opacity-100" onClick={closeSession} title={t("canvas.agent.end")} aria-label={t("canvas.agent.end")}>
                        <Square className="size-3" />
                    </button>
                ) : null}
                <button type="button" className={cn("rounded p-1 opacity-60 hover:opacity-100", status === "connected" ? "" : "ml-auto")} onClick={() => setOpen(false)} title={t("canvas.agent.close")} aria-label={t("canvas.agent.close")}>
                    <X className="size-3.5" />
                </button>
            </div>

            {status === "connected" ? (
                <div className="border-b px-2 py-1.5" style={{ borderColor: theme.toolbar.border }}>
                    <AgentSettingsControls session={session} className="flex min-w-0 items-center gap-1" />
                </div>
            ) : null}

            <div
                ref={scrollRef}
                className="flex-1 space-y-3 overflow-y-auto px-3 py-3 text-sm"
                data-canvas-scroll=""
                onScroll={(event) => {
                    followRef.current = atBottom(event.currentTarget);
                }}
                onWheel={(event) => {
                    // A wheel up is the reader taking over, at once: waiting for the scroll position to say so
                    // would let one more message drag them back down in the middle of the gesture.
                    if (event.deltaY < 0) followRef.current = false;
                }}
            >
                {messages.length === 0 ? <p className="text-xs opacity-50">{t("canvas.agent.hint")}</p> : null}
                {messages.map((message) => (
                    <div key={message.id} className={message.role === "user" ? "ml-6 rounded-xl px-3 py-2" : "mr-2"} style={message.role === "user" ? { background: theme.node.fill, border: `1px solid ${theme.node.stroke}` } : undefined}>
                        {message.tools.length ? (
                            <div className="mb-1 space-y-0.5 text-[11px] opacity-60">
                                {message.tools.map((step) => (
                                    <div key={step.callId} className="flex items-center gap-1">
                                        <Wrench className="size-3" />
                                        <span className="truncate">{t("canvas.agent.tool", { name: step.name })}</span>
                                    </div>
                                ))}
                            </div>
                        ) : null}
                        {message.text ? <p className="whitespace-pre-wrap leading-relaxed">{message.text}</p> : null}
                        {message.role === "assistant" && !message.text && message.streaming ? (
                            <p className="flex items-center gap-1.5 text-xs opacity-50">
                                <LoaderCircle className="size-3 animate-spin" />
                                {t("canvas.agent.thinking")}
                            </p>
                        ) : null}
                        {message.role === "error" ? (
                            <p className="text-xs" style={{ color: "#ef4444" }}>
                                {message.text}
                            </p>
                        ) : null}
                    </div>
                ))}
            </div>

            <div className="relative border-t px-2 py-2" style={{ borderColor: theme.toolbar.border }}>
                <textarea
                    ref={inputRef}
                    value={draft}
                    onChange={(event) => onChange(event.target.value)}
                    onKeyDown={(event) => {
                        if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
                            event.preventDefault();
                            submit();
                        }
                    }}
                    rows={2}
                    placeholder={t("canvas.agent.placeholder")}
                    className="w-full resize-none bg-transparent px-1 text-sm outline-none placeholder:opacity-40"
                    style={{ color: theme.node.text }}
                />
                <div className="flex items-center justify-end gap-1">
                    {busy ? (
                        <button type="button" className="rounded-full p-2 hover:opacity-80" onClick={() => cancelAgentSession(session)} title={t("canvas.agent.stop")} aria-label={t("canvas.agent.stop")}>
                            <Square className="size-3.5" />
                        </button>
                    ) : null}
                    <button
                        type="button"
                        className="rounded-full p-2 hover:opacity-80 disabled:opacity-30"
                        style={{ background: theme.node.fill, border: `1px solid ${theme.node.stroke}` }}
                        disabled={!draft.trim() || busy}
                        onClick={() => void submit()}
                        title={t("canvas.agent.send")}
                        aria-label={t("canvas.agent.send")}
                    >
                        <ArrowUp className="size-4" />
                    </button>
                </div>
            </div>

            {/* Drag the corner and the panel is the size you left it — for this session, like its position. */}
            <div data-canvas-agent-grip="top-left" className="absolute left-0 top-0 z-10 size-3 cursor-nwse-resize" onPointerDown={startResize("top-left")} onPointerMove={moveResize} onPointerUp={endResize} onPointerCancel={endResize} aria-hidden>
                <div className="absolute left-1 top-1 size-1.5 rounded-tl-sm border-l-2 border-t-2 opacity-40" style={{ borderColor: theme.node.text }} />
            </div>
            <div
                data-canvas-agent-grip="bottom-right"
                className="absolute bottom-0 right-0 z-10 size-4 cursor-nwse-resize"
                onPointerDown={startResize("bottom-right")}
                onPointerMove={moveResize}
                onPointerUp={endResize}
                onPointerCancel={endResize}
                aria-hidden
            >
                <div className="absolute bottom-1 right-1 size-2 rounded-br-sm border-b-2 border-r-2 opacity-40" style={{ borderColor: theme.node.text }} />
            </div>
        </div>
    );
}

/** How one cited node is described to the agent. Ids are what a tool call could act on; the text is what
 * a model can read today. */
function describeNode(node?: CanvasNodeData): string {
    if (!node) return "";
    const kind = NODE_LABEL[node.type] || node.type;
    const title = node.title || "";
    const prompt = (node.metadata?.prompt || node.metadata?.texts?.[0]?.content || "").slice(0, 200);
    return `- ${node.id} (${kind}) ${title}${prompt ? `: ${prompt}` : ""}`;
}
