import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { ArrowUp, Bot, LoaderCircle, Square, Wrench, X } from "lucide-react";

import { cn } from "@/lib/utils";
import { canvasThemes } from "@/lib/canvas-theme";
import { CANVAS_DIALOG_CONTENT } from "@/lib/canvas-overlay";
import { useCanvasTheme } from "@/lib/canvas/use-canvas-theme";
import { AgentSettingsControls } from "./agent-settings-controls";
import { CANVAS_AGENT, cancelAgentSession, closeAgentSession, endAgentSession, onAgentEvent, openAgentSession, sendAgentMessage, type AgentEvent } from "@/services/api/agent";
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

type ToolStep = { callId: string; name: string; output?: string };

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

export function CanvasAgentPanel({ canvasId, nodes, onFocusNode }: Props) {
    const { t } = useTranslation();
    const theme = useCanvasTheme();
    const config = useEffectiveConfig();
    const open = useCanvasAgentStore((state) => state.open);
    const position = useCanvasAgentStore((state) => state.position);
    const setOpen = useCanvasAgentStore((state) => state.setOpen);
    const setPosition = useCanvasAgentStore((state) => state.setPosition);

    const [messages, setMessages] = useState<Message[]>([]);
    const [draft, setDraft] = useState("");
    const [status, setStatus] = useState<"idle" | "connecting" | "connected" | "closed">("idle");
    const [busy, setBusy] = useState(false);
    const [mention, setMention] = useState<{ top: number; query: string } | null>(null);
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
            await openAgentSession(config, CANVAS_AGENT);
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
        return () => closeAgentSession(CANVAS_AGENT);
    }, [open]);

    useEffect(() => onAgentEvent(CANVAS_AGENT, applyEvent), []);

    const applyEvent = (event: AgentEvent) => {
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
                else if (event.type === "tool_call") updated.tools = [...last.tools, { callId: event.callId || "", name: event.name || "tool" }];
                else updated.tools = last.tools.map((step) => (step.callId === event.callId ? { ...step, output: event.output } : step));
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
        setMention(null);
        setBusy(true);
        sendAgentMessage(CANVAS_AGENT, { prompt: context + text });
    };

    const closeSession = () => {
        endAgentSession(CANVAS_AGENT);
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

    // --- mentions ---------------------------------------------------------------------------------------
    const candidates = useMemo(() => {
        if (!mention) return [];
        const query = mention.query.toLowerCase();
        return nodes
            .filter((node) => node.type !== CanvasNodeType.Group)
            .filter((node) => !query || `${node.title || ""} ${node.id} ${NODE_LABEL[node.type] || node.type}`.toLowerCase().includes(query))
            .slice(0, 8);
    }, [mention, nodes]);

    const onChange = (value: string) => {
        setDraft(value);
        const caret = inputRef.current?.selectionStart ?? value.length;
        const upTo = value.slice(0, caret);
        const at = upTo.lastIndexOf("@");
        if (at < 0 || /\s/.test(upTo.slice(at + 1))) {
            setMention(null);
            return;
        }
        setMention({ top: at, query: upTo.slice(at + 1) });
    };

    const cite = (node: CanvasNodeData) => {
        if (!mention) return;
        const label = node.title || node.id;
        const before = draft.slice(0, mention.top);
        const after = draft.slice(mention.top + 1 + mention.query.length);
        setDraft(`${before}@${label} ${after}`);
        setCited((current) => (current.includes(node.id) ? current : [...current, node.id]));
        setMention(null);
        inputRef.current?.focus();
    };

    useEffect(() => {
        const element = scrollRef.current;
        if (element) element.scrollTop = element.scrollHeight;
    }, [messages]);

    if (!open) return null;

    const style = position ? { left: position.x, top: position.y } : { right: 24, bottom: 24 };

    return (
        <div
            ref={cardRef}
            data-canvas-overlay=""
            className="absolute z-[95] flex w-[380px] max-w-[calc(100%-16px)] flex-col overflow-hidden rounded-2xl border shadow-2xl backdrop-blur"
            style={{ ...style, height: 480, background: theme.toolbar.panel, borderColor: theme.toolbar.border, color: theme.node.text }}
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
                    <AgentSettingsControls session={CANVAS_AGENT} className="flex min-w-0 items-center gap-1" />
                </div>
            ) : null}

            <div ref={scrollRef} className="flex-1 space-y-3 overflow-y-auto px-3 py-3 text-sm" data-canvas-scroll="">
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
                {mention && candidates.length ? (
                    <div className="absolute bottom-full left-2 right-2 mb-1 overflow-hidden rounded-lg border shadow-xl" style={{ background: theme.toolbar.panel, borderColor: theme.toolbar.border }}>
                        {candidates.map((node) => (
                            <button key={node.id} type="button" className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs hover:opacity-80" onClick={() => cite(node)}>
                                <span className="truncate">{node.title || node.id}</span>
                                <span className="ml-auto shrink-0 opacity-50">{NODE_LABEL[node.type] || node.type}</span>
                            </button>
                        ))}
                    </div>
                ) : null}
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
                        <button type="button" className="rounded-full p-2 hover:opacity-80" onClick={() => cancelAgentSession(CANVAS_AGENT)} title={t("canvas.agent.stop")} aria-label={t("canvas.agent.stop")}>
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
