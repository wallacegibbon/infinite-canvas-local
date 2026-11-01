import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";

/**
 * Whether a node already holds a result.
 *
 * It decides where that node's prompt lives: a node that holds a result keeps the words you are editing in
 * `composerContent`, so the result is not overwritten by typing; a node that holds none keeps them in
 * `prompt`, ready to be run. The node's own prompt panel asks this, and so does the agent's `set_prompt` —
 * one question, so a prompt written by the agent lands in the same field the user's typing would.
 */
export function holdsResult(node: CanvasNodeData): boolean {
    const content = node.metadata?.content || "";
    if (node.type === CanvasNodeType.Text) return Boolean(content.trim());
    if (node.type === CanvasNodeType.Image) return Boolean(content);
    return false;
}
