import { useEffect, useRef, type RefObject } from "react";

import { getNodeSpec } from "@/constant/canvas";
import { resolveMediaUrl } from "@/services/file-storage";
import { imageToDataUrl } from "@/services/image-storage";
import { onAnyAgentEvent, sendAgentMessage, type ToolContent } from "@/services/api/agent";
import { CanvasNodeType, type CanvasConnection, type CanvasGenerationMode, type CanvasNodeData, type CanvasNodeMetadata, type CanvasNodeTypeId, type Position } from "@/types/canvas";

/**
 * The canvas's hands.
 *
 * The gateway advertises the tools and cannot run any of them: it holds the socket, not the nodes. So a
 * tool call arrives here — `{"type":"tool","callId":…,"name":…,"arguments":…}` — and this runs it against
 * the page's own state and answers on the same session it came from. Whatever the user's buttons do, a
 * tool does; nothing here is a second way to change a canvas.
 *
 * It answers every session, not just the canvas panel's: the canvas is one, and a node's agent asking to
 * see it is the same question as the panel's agent asking.
 */
export function useCanvasAgentTools({
    nodesRef,
    connectionsRef,
    createNode,
    generateNode,
    setNodePrompt,
}: {
    nodesRef: RefObject<CanvasNodeData[]>;
    connectionsRef: RefObject<CanvasConnection[]>;
    createNode: CanvasHands["createNode"];
    generateNode: CanvasHands["generateNode"];
    setNodePrompt: CanvasHands["setNodePrompt"];
}) {
    // The refs and callbacks outlive a render, and a tool call can arrive between two of them.
    const hands = useRef<CanvasHands>({
        nodes: () => [],
        connections: () => [],
        createNode,
        generateNode,
        setNodePrompt,
    });
    hands.current = {
        nodes: () => nodesRef.current || [],
        connections: () => connectionsRef.current || [],
        createNode,
        generateNode,
        setNodePrompt,
    };

    useEffect(
        () =>
            onAnyAgentEvent((session, event) => {
                if (event.type !== "tool" || !event.callId) return;
                const callId = event.callId;
                const answer = (content: ToolContent[], isError?: boolean) => sendAgentMessage(session, { toolResult: { callId, content, isError } });
                void runTool(event.name || "", event.arguments, hands.current).then(
                    (result) => answer(result.content, result.isError),
                    (error: unknown) => answer([textBlock(error instanceof Error ? error.message : String(error))], true),
                );
            }),
        [],
    );
}

/** What the page lends a tool: its state to read, the way it makes a node, and the button that runs one. */
type CanvasHands = {
    nodes: () => CanvasNodeData[];
    connections: () => CanvasConnection[];
    createNode: (type: CanvasNodeTypeId, position?: Position, patch?: CanvasNodeMetadata) => CanvasNodeData;
    generateNode: (nodeId: string, mode: CanvasGenerationMode, prompt: string) => Promise<void>;
    setNodePrompt: (nodeId: string, prompt: string) => void;
};

/** One tool's result: the content blocks the agent reads, and whether it failed rather than did something. */
type ToolAnswer = { content: ToolContent[]; isError?: boolean };

const textBlock = (text: string): ToolContent => ({ type: "text", text });

/** The node types a tool may make. A group is structure, not content, and plugins are the user's. */
const CREATE_TYPES: string[] = [CanvasNodeType.Image, CanvasNodeType.Video, CanvasNodeType.Audio, CanvasNodeType.Text, CanvasNodeType.Config];

async function runTool(name: string, args: unknown, hands: CanvasHands): Promise<ToolAnswer> {
    switch (name) {
        case "ping":
            return { content: [textBlock(`listening: ${hands.nodes().length} nodes, ${hands.connections().length} connections`)] };
        case "list_nodes":
            return { content: [textBlock(describeCanvas(hands.nodes(), hands.connections()))] };
        case "create_node":
            return createNode(hands, args);
        case "read_node":
            return readNode(hands, args);
        case "set_prompt":
            return setPrompt(hands, args);
        case "generate":
            return pressGenerate(hands, args);
        default:
            return { content: [textBlock(`this canvas has no tool called ${name}`)], isError: true };
    }
}

/**
 * Make one node, the way the user's own button makes one — placed where the view is looking, selected, and
 * with its panel open when that is what pressing the button does.
 *
 * The position a tool states is the node's top-left corner, which is what `list_nodes` reports; the page's
 * own create takes a centre.
 */
function createNode(hands: CanvasHands, args: unknown): ToolAnswer {
    const input = (typeof args === "object" && args ? args : {}) as { type?: unknown; prompt?: unknown; position?: unknown };
    const type = typeof input.type === "string" ? input.type.trim() : "";
    if (!CREATE_TYPES.includes(type)) return { content: [textBlock(`no node type "${type}": this canvas makes ${CREATE_TYPES.join(", ")}`)], isError: true };

    const prompt = typeof input.prompt === "string" ? input.prompt : "";
    // A text node holds its words in `content`; every other node holds a prompt, which its own panel edits.
    const patch: CanvasNodeMetadata | undefined = prompt ? (type === CanvasNodeType.Text ? { content: prompt } : { prompt }) : undefined;
    const corner = readPosition(input.position);
    const centre = corner ? centreOf(type, corner) : undefined;

    const node = hands.createNode(type, centre, patch);
    return { content: [textBlock(`created ${node.id} (${node.type}) at (${Math.round(node.position.x)}, ${Math.round(node.position.y)})`)] };
}

/**
 * Write one node's prompt, in the same field the node's own panel writes it to.
 *
 * Changing what a node says and running it again is one gesture in the canvas: the result is replaced in that
 * node (docs/decisions.md #77), not made beside it. A variant is a new node fed through the reference bar,
 * which is what `create_node` plus a connection is — so both intents keep their own tool.
 */
function setPrompt(hands: CanvasHands, args: unknown): ToolAnswer {
    const input = (typeof args === "object" && args ? args : {}) as { nodeId?: unknown; prompt?: unknown };
    const nodeId = typeof input.nodeId === "string" ? input.nodeId.trim() : "";
    if (typeof input.prompt !== "string") return { content: [textBlock("set_prompt needs a prompt")], isError: true };
    const node = hands.nodes().find((candidate) => candidate.id === nodeId);
    if (!node) return { content: [textBlock(`no node with id "${nodeId}" on this canvas`)], isError: true };

    hands.setNodePrompt(node.id, input.prompt);
    return { content: [textBlock(`the prompt of ${node.id} is now: ${input.prompt.slice(0, 200)}`)], isError: false };
}

/**
 * Press one node's generate button — the same function the node's own panel presses.
 *
 * It is not waited on: a picture takes minutes, and the tool's job is to start it, not to be the progress
 * bar. The node itself shows what is happening, which is where the user will look anyway.
 *
 * The mode is the one the node's own panel would use: a content node generates what it is, and a config node
 * generates the kind its composer is set to (that is what pressing its button does).
 */
function pressGenerate(hands: CanvasHands, args: unknown): ToolAnswer {
    const input = (typeof args === "object" && args ? args : {}) as { nodeId?: unknown };
    const nodeId = typeof input.nodeId === "string" ? input.nodeId.trim() : "";
    const node = hands.nodes().find((candidate) => candidate.id === nodeId);
    if (!node) return { content: [textBlock(`no node with id "${nodeId}" on this canvas`)], isError: true };

    // A content node generates what it is; a config node generates the kind its composer is set to.
    const mode: string = node.type === CanvasNodeType.Config ? node.metadata?.generationMode || "image" : node.type;
    if (!GENERATE_MODES.includes(mode)) return { content: [textBlock(`a ${node.type} node has nothing to generate`)], isError: true };

    void hands.generateNode(node.id, mode as CanvasGenerationMode, wordsOf(node));
    return { content: [textBlock(`pressed generate on ${node.id} (${mode}): the node shows its own progress`)], isError: false };
}

const GENERATE_MODES: string[] = [CanvasNodeType.Text, CanvasNodeType.Image, CanvasNodeType.Video, CanvasNodeType.Audio];

/**
 * Read one node's actual content.
 *
 * What comes back is the node's own stuff, not a description of it: the words it holds, and — for a node
 * that holds a picture, a film or a sound — the media itself, as the block the protocol defines for it.
 * The block carries its type, so the choice of block is the media's, not ours: an image is an image, a
 * sound is a sound, and a film travels as a `resource` blob because the protocol has no video block.
 */
async function readNode(hands: CanvasHands, args: unknown): Promise<ToolAnswer> {
    const input = (typeof args === "object" && args ? args : {}) as { nodeId?: unknown };
    const nodeId = typeof input.nodeId === "string" ? input.nodeId.trim() : "";
    const node = hands.nodes().find((candidate) => candidate.id === nodeId);
    if (!node) return { content: [textBlock(`no node with id "${nodeId}" on this canvas`)], isError: true };

    const content: ToolContent[] = [];
    const words = wordsOf(node);
    if (words) content.push(textBlock(words));

    const media = await mediaOf(node);
    if (media) content.push(media);
    if (!content.length) content.push(textBlock(`${node.id} (${node.type}) holds nothing to read`));
    return { content };
}

/** The words a node holds: a text node's body, or the prompt its own panel shows. */
function wordsOf(node: CanvasNodeData): string {
    return (node.metadata?.composerContent ?? node.metadata?.prompt ?? node.metadata?.content ?? node.metadata?.texts?.[0]?.content ?? "").trim();
}

/** The block a node's media travels in — or nothing, for a node that holds no media. */
async function mediaOf(node: CanvasNodeData): Promise<ToolContent | undefined> {
    if (!MEDIA_TYPES.includes(node.type)) return undefined;
    const dataUrl = await dataUrlOf(node);
    if (!dataUrl) return undefined;
    const parsed = /^data:([^;,]+);base64,(.*)$/s.exec(dataUrl);
    if (!parsed) return undefined;
    const mimeType = node.metadata?.mimeType || parsed[1];
    const data = parsed[2];
    // A film has no block of its own in the protocol; it travels as a resource whose mimeType says what it is.
    return node.type === CanvasNodeType.Video ? { type: "resource", resource: { uri: `canvas://${node.id}`, mimeType, blob: data } } : { type: node.type === CanvasNodeType.Audio ? "audio" : "image", data, mimeType };
}

const MEDIA_TYPES: string[] = [CanvasNodeType.Image, CanvasNodeType.Video, CanvasNodeType.Audio];

/** A node's media as a data URL, however it is stored: an uploaded file in the page's own store, or a URL. */
async function dataUrlOf(node: CanvasNodeData): Promise<string> {
    const content = node.metadata?.content || "";
    const storageKey = node.metadata?.storageKey;
    if (content.startsWith("data:")) return content;
    if (node.type === CanvasNodeType.Image) return imageToDataUrl({ url: content, storageKey });
    const url = await resolveMediaUrl(storageKey, content);
    if (!url) return "";
    if (url.startsWith("data:")) return url;
    return blobToDataUrl(await (await fetch(url)).blob());
}

function blobToDataUrl(blob: Blob): Promise<string> {
    return new Promise((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve(String(reader.result || ""));
        reader.onerror = () => reject(new Error("the file could not be read"));
        reader.readAsDataURL(blob);
    });
}

/** Where the node's centre has to be for its top-left corner to land where the tool asked. */
function centreOf(type: string, corner: Position): Position {
    const spec = getNodeSpec(type);
    return { x: corner.x + spec.width / 2, y: corner.y + spec.height / 2 };
}

function readPosition(value: unknown): Position | undefined {
    if (typeof value !== "object" || !value) return undefined;
    const { x, y } = value as { x?: unknown; y?: unknown };
    return typeof x === "number" && typeof y === "number" ? { x, y } : undefined;
}

/**
 * The whole canvas as text: every node with the id a tool call can act on, and every edge.
 *
 * One line per node, because a model reads lines and diffs them; nothing is omitted for looking empty,
 * since a node with no prompt is a fact about the canvas too.
 */
function describeCanvas(nodes: CanvasNodeData[], connections: CanvasConnection[]): string {
    const lines = [`${nodes.length} nodes, ${connections.length} connections`];
    for (const node of nodes) {
        const prompt = wordsOf(node).slice(0, 300);
        const title = node.title ? ` "${node.title}"` : "";
        const at = ` at (${Math.round(node.position.x)}, ${Math.round(node.position.y)})`;
        lines.push(`- ${node.id} (${node.type})${title}${at}${prompt ? ` prompt: ${prompt}` : ""}`);
    }
    if (connections.length) lines.push("connections:");
    for (const connection of connections) lines.push(`- ${connection.fromNodeId} -> ${connection.toNodeId}`);
    return lines.join("\n");
}
