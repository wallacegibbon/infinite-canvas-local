import { useEffect, useRef, type RefObject } from "react";

import { onAnyAgentEvent, sendAgentMessage } from "@/services/api/agent";
import type { CanvasConnection, CanvasNodeData } from "@/types/canvas";

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
export function useCanvasAgentTools({ nodesRef, connectionsRef }: { nodesRef: RefObject<CanvasNodeData[]>; connectionsRef: RefObject<CanvasConnection[]> }) {
    // The refs outlive a render, and a tool call can arrive between two of them.
    const canvas = useRef({ nodesRef, connectionsRef });
    canvas.current = { nodesRef, connectionsRef };

    useEffect(
        () =>
            onAnyAgentEvent((session, event) => {
                if (event.type !== "tool" || !event.callId) return;
                const answer = runTool(event.name || "", canvas.current.nodesRef.current || [], canvas.current.connectionsRef.current || []);
                sendAgentMessage(session, { toolResult: { callId: event.callId, ...answer } });
            }),
        [],
    );
}

/** One tool's result: the text the agent reads, and whether it failed rather than did something. */
type ToolAnswer = { text: string; isError?: boolean };

function runTool(name: string, nodes: CanvasNodeData[], connections: CanvasConnection[]): ToolAnswer {
    switch (name) {
        case "ping":
            return { text: `listening: ${nodes.length} nodes, ${connections.length} connections` };
        case "list_nodes":
            return { text: describeCanvas(nodes, connections) };
        default:
            return { text: `this canvas has no tool called ${name}`, isError: true };
    }
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
        const prompt = (node.metadata?.composerContent ?? node.metadata?.prompt ?? node.metadata?.texts?.[0]?.content ?? "").trim().slice(0, 300);
        const title = node.title ? ` "${node.title}"` : "";
        const at = ` at (${Math.round(node.position.x)}, ${Math.round(node.position.y)})`;
        lines.push(`- ${node.id} (${node.type})${title}${at}${prompt ? ` prompt: ${prompt}` : ""}`);
    }
    if (connections.length) lines.push("connections:");
    for (const connection of connections) lines.push(`- ${connection.fromNodeId} -> ${connection.toNodeId}`);
    return lines.join("\n");
}
