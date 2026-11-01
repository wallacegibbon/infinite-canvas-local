import type { CanvasNodeData } from "@/types/canvas";
import type { UploadedFile } from "@/services/file-storage";
import { fitNodeSize } from "@/lib/canvas/canvas-node-size";
import { videoMetadata } from "@/lib/canvas/canvas-node-factory";

// A generated clip is capped on screen: a 4K result would otherwise cover the whole viewport the
// moment it lands, and the node is a thumbnail until you open it.
export const VIDEO_NODE_MAX_WIDTH = 420;
export const VIDEO_NODE_MAX_HEIGHT = 420;

/** Swap a video node's body for a finished file, resizing and re-centring the node around it. */
export function applyGeneratedVideo(item: CanvasNodeData, video: UploadedFile, extra: CanvasNodeData["metadata"] = {}): CanvasNodeData {
    const videoSize = fitNodeSize(video.width || item.width, video.height || item.height, VIDEO_NODE_MAX_WIDTH, VIDEO_NODE_MAX_HEIGHT);
    return {
        ...item,
        width: videoSize.width,
        height: videoSize.height,
        position: { x: item.position.x + item.width / 2 - videoSize.width / 2, y: item.position.y + item.height / 2 - videoSize.height / 2 },
        metadata: { ...item.metadata, ...videoMetadata(video), ...extra },
    };
}
