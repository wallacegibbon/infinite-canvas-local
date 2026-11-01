import { useCallback } from "react";
import { useTranslation } from "react-i18next";
import { nanoid } from "nanoid";

import { toast } from "@/components/kit/toast";
import { createVideoInterpolateTask, storeGeneratedVideo, waitForVideoGenerationTask } from "@/services/api/video";
import { useEffectiveConfig } from "@/stores/use-config-store";
import { fitNodeSize } from "@/lib/canvas/canvas-node-size";
import { isGenerationCanceled } from "@/lib/canvas/canvas-generation-helpers";
import { VIDEO_NODE_MAX_HEIGHT, VIDEO_NODE_MAX_WIDTH, applyGeneratedVideo } from "@/lib/canvas/canvas-generated-video";
import { NODE_STATUS_ERROR, NODE_STATUS_LOADING } from "@/lib/canvas/canvas-node-status";
import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";
import type { NodeEditorDeps } from "@/pages/canvas/hooks/node-editor-deps";

/**
 * The two editors that produce a new *video* node: upscale and frame interpolation.
 *
 * They are split from the image editors because they share almost nothing with them beyond the
 * spawn-select-open pattern — different task API, different polling, and a node whose size comes
 * from the clip rather than from a fixed default.
 */
export function useVideoEditors(deps: NodeEditorDeps) {
    const { setNodes, setConnections, setSelectedNodeIds, setSelectedConnectionId, setDialogNodeId, setRunningNodeId, startGenerationRequest, finishGenerationRequest } = deps;
    const { t } = useTranslation();
    const effectiveConfig = useEffectiveConfig();

    const interpolateVideoNode = useCallback(
        async (node: CanvasNodeData) => {
            if (!node.metadata?.content) return;
            const childId = nanoid();
            const sourceWidth = node.metadata?.naturalWidth || node.width;
            const sourceHeight = node.metadata?.naturalHeight || node.height;
            const size = fitNodeSize(sourceWidth, sourceHeight, VIDEO_NODE_MAX_WIDTH, VIDEO_NODE_MAX_HEIGHT);
            setRunningNodeId(childId);
            setNodes((prev) => [
                ...prev,
                {
                    id: childId,
                    type: CanvasNodeType.Video,
                    title: t("canvas.nodeToolbar.interpolateTitle"),
                    titleKey: "canvas.nodeToolbar.interpolateTitle",
                    position: { x: node.position.x + node.width + 96, y: node.position.y },
                    width: size.width,
                    height: size.height,
                    metadata: { prompt: node.metadata?.prompt, status: NODE_STATUS_LOADING },
                },
            ]);
            setConnections((prev) => [...prev, { id: nanoid(), fromNodeId: node.id, toNodeId: childId }]);
            setSelectedNodeIds(new Set([childId]));
            setSelectedConnectionId(null);
            setDialogNodeId(childId);
            const controller = startGenerationRequest(childId, node.id, childId);
            try {
                const task = await createVideoInterpolateTask(
                    effectiveConfig,
                    {
                        name: `${node.title || node.id}.mp4`,
                        type: node.metadata?.mimeType || "video/mp4",
                        url: node.metadata?.content,
                        storageKey: node.metadata?.storageKey,
                    },
                    { multiplier: 2 },
                    { signal: controller.signal },
                );
                setNodes((prev) => prev.map((item) => (item.id === childId ? { ...item, metadata: { ...item.metadata, videoTaskId: task.id, model: "video-rife", params: { multiplier: 2 } } } : item)));
                const video = await storeGeneratedVideo(await waitForVideoGenerationTask(effectiveConfig, task, { signal: controller.signal }));
                setNodes((prev) =>
                    prev.map((item) =>
                        item.id === childId
                            ? applyGeneratedVideo(item, video, {
                                  prompt: node.metadata?.prompt,
                                  model: "video-rife",
                                  videoMode: "interpolate",
                                  previewUrl: undefined,
                                  previewMime: undefined,
                                  previewStep: undefined,
                                  previewTotal: undefined,
                              })
                            : item,
                    ),
                );
            } catch (error) {
                if (isGenerationCanceled(error)) return;
                const errorDetails = error instanceof Error ? error.message : t("canvas.runtime.generationFailed");
                toast.error(errorDetails);
                setNodes((prev) => prev.map((item) => (item.id === childId ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, errorDetails } } : item)));
            } finally {
                finishGenerationRequest(childId, controller);
                setRunningNodeId((current) => (current === childId ? null : current));
            }
        },
        [effectiveConfig, finishGenerationRequest, startGenerationRequest, t],
    );

    return { interpolateVideoNode };
}
