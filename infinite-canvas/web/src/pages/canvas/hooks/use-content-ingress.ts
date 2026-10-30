import { useCallback, useRef } from "react";
import type { ChangeEvent, Dispatch, DragEvent, RefObject, SetStateAction } from "react";
import { useTranslation } from "react-i18next";

import { toast } from "@/components/kit/toast";
import { NODE_DEFAULT_SIZE } from "@/constant/canvas";
import { isAudioFile } from "@/lib/canvas/canvas-generation-helpers";
import { VIDEO_NODE_MAX_HEIGHT, VIDEO_NODE_MAX_WIDTH } from "@/lib/canvas/canvas-generated-video";
import { audioMetadata, createCanvasNode, imageMetadata, videoMetadata } from "@/lib/canvas/canvas-node-factory";
import { fitNodeSize } from "@/lib/canvas/canvas-node-size";
import { NODE_STATUS_SUCCESS } from "@/lib/canvas/canvas-node-status";
import { uploadMediaFile } from "@/services/file-storage";
import { uploadImage } from "@/services/image-storage";
import { CanvasNodeType, type CanvasNodeData, type ContextMenuState, type Position } from "@/types/canvas";

/**
 * How content gets onto the canvas: files chosen from a picker, files dropped on it, an image or a
 * block of text read out of the system clipboard.
 *
 * Everything here answers one question — "the user handed me a File or a string, make it a node" —
 * and none of it reads the graph. That is what makes it a module: the only state it needs from the
 * page is where the viewport is looking, and the only thing it needs to give back is the hidden
 * file input the picker clicks through.
 */
export type ContentIngressDeps = {
    setNodes: Dispatch<SetStateAction<CanvasNodeData[]>>;
    setSelectedNodeIds: Dispatch<SetStateAction<Set<string>>>;
    setSelectedConnectionId: Dispatch<SetStateAction<string | null>>;
    setDialogNodeId: Dispatch<SetStateAction<string | null>>;
    setContextMenu: Dispatch<SetStateAction<ContextMenuState | null>>;
    containerRef: RefObject<HTMLDivElement | null>;
    size: { width: number; height: number };
    screenToCanvas: (clientX: number, clientY: number) => Position;
    getCanvasCenter: () => Position;
};

export function useContentIngress(deps: ContentIngressDeps) {
    const { setNodes, setSelectedNodeIds, setSelectedConnectionId, setDialogNodeId, setContextMenu, containerRef, size, screenToCanvas, getCanvasCenter } = deps;
    const { t } = useTranslation();

    // The hidden <input type="file"> in the page's JSX. Not a dependency the page passes in: the
    // module owns the picker, so it owns the element the picker clicks.
    const imageInputRef = useRef<HTMLInputElement>(null);
    // Which node — if any — the picker was opened to replace. Cleared on every change.
    const uploadTargetRef = useRef<{ nodeId?: string; position?: Position } | null>(null);

const createImageFileNode = useCallback(async (file: File, position: Position) => {
    const image = await uploadImage(file);
    const size = fitNodeSize(image.width, image.height);
    const id = `image-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
    const newNode: CanvasNodeData = {
        id,
        type: CanvasNodeType.Image,
        title: file.name,
        position: { x: position.x - size.width / 2, y: position.y - size.height / 2 },
        width: size.width,
        height: size.height,
        metadata: imageMetadata(image),
    };

    setNodes((prev) => [...prev, newNode]);
    setSelectedNodeIds(new Set([id]));
    setSelectedConnectionId(null);
    setDialogNodeId(id);
}, []);

const createVideoFileNode = useCallback(async (file: File, position: Position) => {
    const video = await uploadMediaFile(file, "video");
    const size = fitNodeSize(video.width || 1280, video.height || 720, VIDEO_NODE_MAX_WIDTH, VIDEO_NODE_MAX_HEIGHT);
    const id = `video-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
    setNodes((prev) => [
        ...prev,
        {
            id,
            type: CanvasNodeType.Video,
            title: file.name,
            position: { x: position.x - size.width / 2, y: position.y - size.height / 2 },
            width: size.width,
            height: size.height,
            metadata: videoMetadata(video),
        },
    ]);
    setSelectedNodeIds(new Set([id]));
    setSelectedConnectionId(null);
    setDialogNodeId(id);
}, []);

const createAudioFileNode = useCallback(async (file: File, position: Position) => {
    const audio = await uploadMediaFile(file, "audio");
    const spec = NODE_DEFAULT_SIZE[CanvasNodeType.Audio];
    const id = `audio-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
    setNodes((prev) => [
        ...prev,
        {
            id,
            type: CanvasNodeType.Audio,
            title: file.name,
            position: { x: position.x - spec.width / 2, y: position.y - spec.height / 2 },
            width: spec.width,
            height: spec.height,
            metadata: audioMetadata(audio),
        },
    ]);
    setSelectedNodeIds(new Set([id]));
    setSelectedConnectionId(null);
}, []);

const createTextNodeFromClipboard = useCallback(
    (text: string) => {
        const trimmed = text.trim();
        if (!trimmed) return false;

        const node = {
            ...createCanvasNode(CanvasNodeType.Text, getCanvasCenter(), { content: trimmed, status: NODE_STATUS_SUCCESS }),
            title: trimmed.slice(0, 32) || t("canvas.runtime.clipboardText"),
        };

        setNodes((prev) => [...prev, node]);
        setSelectedNodeIds(new Set([node.id]));
        setSelectedConnectionId(null);
        setContextMenu(null);
        setDialogNodeId(node.id);
        return true;
    },
    [getCanvasCenter, t],
);

const pasteSystemClipboard = useCallback(async () => {
    if (!navigator.clipboard) return;

    const items = await navigator.clipboard.read();
    const imageItem = items.find((item) => item.types.some((type) => type.startsWith("image/")));
    if (imageItem) {
        const imageType = imageItem.types.find((type) => type.startsWith("image/"));
        if (!imageType) return;
        const blob = await imageItem.getType(imageType);
        const file = new File([blob], "clipboard-image.png", { type: imageType });
        void createImageFileNode(file, getCanvasCenter());
        toast.success(t("canvas.runtime.clipboardImageAdded"));
        return;
    }

    const text = await navigator.clipboard.readText();
    if (createTextNodeFromClipboard(text)) toast.success(t("canvas.runtime.clipboardTextAdded"));
}, [createImageFileNode, createTextNodeFromClipboard, getCanvasCenter, t]);

const handleUploadRequest = useCallback((nodeId?: string, position?: Position) => {
    uploadTargetRef.current = { nodeId, position };
    imageInputRef.current?.click();
}, []);

const handleImageInputChange = useCallback(
    async (event: ChangeEvent<HTMLInputElement>) => {
        const files = Array.from(event.target.files || []).filter(
            (f) => f.type.startsWith("image/") || f.type.startsWith("video/") || isAudioFile(f),
        );
        if (!files.length) {
            uploadTargetRef.current = null;
            event.target.value = "";
            return;
        }

        const target = uploadTargetRef.current;
        const basePosition =
            target?.position ||
            screenToCanvas(
                (containerRef.current?.getBoundingClientRect().left || 0) + size.width / 2,
                (containerRef.current?.getBoundingClientRect().top || 0) + size.height / 2,
            );
        const STAGGER = 40; // Offset between multiple imported files.

        // When replacing a target node, use the first file as the replacement and create the rest nearby.
        if (target?.nodeId) {
            const [first, ...rest] = files;

            // Replace the target node with the first file.
            if (isAudioFile(first)) {
                const audio = await uploadMediaFile(first, "audio");
                const spec = NODE_DEFAULT_SIZE[CanvasNodeType.Audio];
                setNodes((prev) =>
                    prev.map((node) =>
                        node.id === target.nodeId
                            ? {
                                  ...node,
                                  type: CanvasNodeType.Audio,
                                  title: first.name,
                                  position: { x: node.position.x + node.width / 2 - spec.width / 2, y: node.position.y + node.height / 2 - spec.height / 2 },
                                  width: spec.width,
                                  height: spec.height,
                                  metadata: { ...node.metadata, ...audioMetadata(audio), errorDetails: undefined },
                              }
                            : node,
                    ),
                );
                setSelectedNodeIds(new Set([target.nodeId]));
                setSelectedConnectionId(null);
            } else if (first.type.startsWith("video/")) {
                const video = await uploadMediaFile(first, "video");
                const nextSize = fitNodeSize(video.width || 1280, video.height || 720, VIDEO_NODE_MAX_WIDTH, VIDEO_NODE_MAX_HEIGHT);
                setNodes((prev) =>
                    prev.map((node) =>
                        node.id === target.nodeId
                            ? {
                                  ...node,
                                  type: CanvasNodeType.Video,
                                  title: first.name,
                                  position: { x: node.position.x + node.width / 2 - nextSize.width / 2, y: node.position.y + node.height / 2 - nextSize.height / 2 },
                                  width: nextSize.width,
                                  height: nextSize.height,
                                  metadata: { ...node.metadata, ...videoMetadata(video), errorDetails: undefined },
                              }
                            : node,
                    ),
                );
                setSelectedNodeIds(new Set([target.nodeId]));
                setSelectedConnectionId(null);
            } else {
                const image = await uploadImage(first);
                const s = fitNodeSize(image.width, image.height);
                setNodes((prev) =>
                    prev.map((node) =>
                        node.id === target.nodeId
                            ? {
                                  ...node,
                                  type: CanvasNodeType.Image,
                                  title: first.name,
                                  width: s.width,
                                  height: s.height,
                                  metadata: {
                                      ...node.metadata,
                                      ...imageMetadata(image),
                                      errorDetails: undefined,
                                      freeResize: false,
                                      images: undefined,
                                      generationType: undefined,
                                      model: undefined,
                                      size: undefined,
                                      quality: undefined,
                                      count: undefined,
                                      references: undefined,
                                      primaryImageId: undefined,
                                  },
                              }
                            : node,
                    ),
                );
                setSelectedNodeIds(new Set([target.nodeId]));
                setSelectedConnectionId(null);
            }

            // Create the remaining files near the target node.
            for (let i = 0; i < rest.length; i++) {
                const offsetPos = { x: basePosition.x + (i + 1) * STAGGER, y: basePosition.y + (i + 1) * STAGGER };
                const f = rest[i];
                if (isAudioFile(f)) {
                    void createAudioFileNode(f, offsetPos);
                } else if (f.type.startsWith("video/")) {
                    void createVideoFileNode(f, offsetPos);
                } else {
                    void createImageFileNode(f, offsetPos);
                }
            }
        } else {
            // Without a replacement target, create all files near the canvas center.
            for (let i = 0; i < files.length; i++) {
                const offsetPos = { x: basePosition.x + i * STAGGER, y: basePosition.y + i * STAGGER };
                const f = files[i];
                if (isAudioFile(f)) {
                    void createAudioFileNode(f, offsetPos);
                } else if (f.type.startsWith("video/")) {
                    void createVideoFileNode(f, offsetPos);
                } else {
                    void createImageFileNode(f, offsetPos);
                }
            }
        }

        uploadTargetRef.current = null;
        event.target.value = "";
    },
    [createAudioFileNode, createImageFileNode, createVideoFileNode, screenToCanvas, size.height, size.width],
);

const handleDrop = useCallback(
    (event: DragEvent<HTMLDivElement>) => {
        event.preventDefault();
        const files = Array.from(event.dataTransfer.files).filter(
            (item) => item.type.startsWith("image/") || item.type.startsWith("video/") || isAudioFile(item),
        );
        if (!files.length) return;

        const basePos = screenToCanvas(event.clientX, event.clientY);
        const STAGGER = 40;
        for (let i = 0; i < files.length; i++) {
            const pos = { x: basePos.x + i * STAGGER, y: basePos.y + i * STAGGER };
            const f = files[i];
            if (isAudioFile(f)) {
                void createAudioFileNode(f, pos);
            } else if (f.type.startsWith("video/")) {
                void createVideoFileNode(f, pos);
            } else {
                void createImageFileNode(f, pos);
            }
        }
    },
    [createAudioFileNode, createImageFileNode, createVideoFileNode, screenToCanvas],
    );

    return { imageInputRef, handleUploadRequest, handleImageInputChange, handleDrop, pasteSystemClipboard };
}
