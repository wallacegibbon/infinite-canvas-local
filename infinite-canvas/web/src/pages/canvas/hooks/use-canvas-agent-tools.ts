import { useEffect, useRef, type RefObject } from "react";

import { getNodeSpec } from "@/constant/canvas";
import { onAnyAgentEvent, sendAgentMessage } from "@/services/api/agent";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData, type CanvasNodeMetadata, type CanvasNodeTypeId, type Position } from "@/types/canvas";

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
export function useCanvasAgentTools({ nodesRef, connectionsRef, createNode }: { nodesRef: RefObject<CanvasNodeData[]>; connectionsRef: RefObject<CanvasConnection[]>; createNode: CanvasHands["createNode"] }) {
    // The refs and callbacks outlive a render, and a tool call can arrive between two of them.
    const hands = useRef<CanvasHands>({
        nodes: () => [],
        connections: () => [],
        createNode,
    });
    hands.current = {
        nodes: () => nodesRef.current || [],
        connections: () => connectionsRef.current || [],
        createNode,
    };

    useEffect(
        () =>
            onAnyAgentEvent((session, event) => {
                if (event.type !== "tool" || !event.callId) return;
                sendAgentMessage(session, { toolResult: { callId: event.callId, ...runTool(event.name || "", event.arguments, hands.current) } });
            }),
        [],
    );
}

/** What the page lends a tool: its state to read, and the one way it makes a node. */
type CanvasHands = {
    nodes: () => CanvasNodeData[];
    connections: () => CanvasConnection[];
    createNode: (type: CanvasNodeTypeId, position?: Position, patch?: CanvasNodeMetadata) => CanvasNodeData;
};

/** One tool's result: the text the agent reads, and whether it failed rather than did something. */
type ToolAnswer = { text: string; isError?: boolean };

/** The node types a tool may make. A group is structure, not content, and plugins are the user's. */
const CREATE_TYPES: string[] = [CanvasNodeType.Image, CanvasNodeType.Video, CanvasNodeType.Audio, CanvasNodeType.Text, CanvasNodeType.Config];

function runTool(name: string, args: unknown, hands: CanvasHands): ToolAnswer {
    switch (name) {
        case "ping":
            return { text: `listening: ${hands.nodes().length} nodes, ${hands.connections().length} connections` };
        case "list_nodes":
            return { text: describeCanvas(hands.nodes(), hands.connections()) };
        case "create_node":
            return createNode(hands, args);
        default:
            return { text: `this canvas has no tool called ${name}`, isError: true };
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
    if (!CREATE_TYPES.includes(type)) return { text: `no node type "${type}": this canvas makes ${CREATE_TYPES.join(", ")}`, isError: true };

    const prompt = typeof input.prompt === "string" ? input.prompt : "";
    // A text node holds its words in `content`; every other node holds a prompt, which its own panel edits.
    const patch: CanvasNodeMetadata | undefined = prompt ? (type === CanvasNodeType.Text ? { content: prompt } : { prompt }) : undefined;
    const corner = readPosition(input.position);
    const centre = corner ? centreOf(type, corner) : undefined;

    const node = hands.createNode(type, centre, patch);
    return { text: `created ${node.id} (${node.type}) at (${Math.round(node.position.x)}, ${Math.round(node.position.y)})` };
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
        // What the node's own panel shows the user as its prompt, in the same order of preference.
        const prompt = (node.metadata?.composerContent ?? node.metadata?.prompt ?? node.metadata?.content ?? node.metadata?.texts?.[0]?.content ?? "").trim().slice(0, 300);
        const title = node.title ? ` "${node.title}"` : "";
        const at = ` at (${Math.round(node.position.x)}, ${Math.round(node.position.y)})`;
        lines.push(`- ${node.id} (${node.type})${title}${at}${prompt ? ` prompt: ${prompt}` : ""}`);
    }
    if (connections.length) lines.push("connections:");
    for (const connection of connections) lines.push(`- ${connection.fromNodeId} -> ${connection.toNodeId}`);
    return lines.join("\n");
}
