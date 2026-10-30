import type { Dispatch, SetStateAction } from "react";
import type { CanvasConnection, CanvasNodeData } from "@/types/canvas";

/**
 * The canvas owns the node graph; an editor only knows how to add to it.
 *
 * Every editor below spawns a child node beside its source, selects it, opens its composer and then
 * runs one generation request. None of them read the graph — they take the node they were handed and
 * append to it — so the whole surface they need is a handful of setters plus the request registry.
 * Passing that in as one object keeps the editors a closed module that can be tested by handing it
 * fakes, instead of a closure over fifty pieces of component state.
 */
export type NodeEditorDeps = {
    setNodes: Dispatch<SetStateAction<CanvasNodeData[]>>;
    setConnections: Dispatch<SetStateAction<CanvasConnection[]>>;
    setSelectedNodeIds: Dispatch<SetStateAction<Set<string>>>;
    setSelectedConnectionId: Dispatch<SetStateAction<string | null>>;
    setDialogNodeId: Dispatch<SetStateAction<string | null>>;
    setRunningNodeId: Dispatch<SetStateAction<string | null>>;
    closeNodeTool: () => void;
    startGenerationRequest: (targetNodeId: string, originNodeId: string, runningId?: string, controller?: AbortController, expectedProgressCount?: number) => AbortController;
    finishGenerationRequest: (targetNodeId: string, controller: AbortController) => void;
};
