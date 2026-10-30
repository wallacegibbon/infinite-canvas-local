import { useCallback } from "react";
import type { Dispatch, SetStateAction } from "react";
import { useTranslation } from "react-i18next";
import { nanoid } from "nanoid";

import { toast } from "@/components/kit/toast";
import { requestAudioGeneration, storeGeneratedAudio } from "@/services/api/audio";
import { requestEdit, requestGeneration, requestImageQuestion } from "@/services/api/image";
import { uploadImage } from "@/services/image-storage";
import { NODE_DEFAULT_SIZE } from "@/constant/canvas";
import { fitNodeSize, nodeSizeFromRatio } from "@/lib/canvas/canvas-node-size";
import { audioMetadata, buildAudioGenerationMetadata, buildImageGenerationMetadata } from "@/lib/canvas/canvas-node-factory";
import { generationReferenceUrls, getGenerationCount, isGenerationCanceled } from "@/lib/canvas/canvas-generation-helpers";
import { NODE_STATUS_ERROR, NODE_STATUS_IDLE, NODE_STATUS_LOADING, NODE_STATUS_SUCCESS } from "@/lib/canvas/canvas-node-status";
import { buildNodeResponseMessages, hydrateNodeGenerationContext } from "@/components/canvas/canvas-node-generation";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData, type CanvasNodeImage, type CanvasNodeText } from "@/types/canvas";
import type { ReferenceAudio, ReferenceVideo } from "@/types/media";

/**
 * One generation request, as every mode needs to see it.
 *
 * The four modes disagree about almost everything below this line, but they agree about this: which
 * node was asked, what the hydrated prompt turned out to be, which config was resolved, whether the
 * result overwrites the source, and the abort controller of the request that owns the whole run.
 */
export type GenerationRun = {
    nodeId: string;
    prompt: string;
    sourceNode: CanvasNodeData | undefined;
    effectivePrompt: string;
    generationConfig: ReturnType<typeof import("@/lib/canvas/canvas-generation-helpers").buildGenerationConfig>;
    generationContext: Awaited<ReturnType<typeof hydrateNodeGenerationContext>>;
    overwriteExisting: boolean;
    /** The text a Text node already held, so the text mode can tell "edit this" from "write new". */
    sourceTextContent: string;
    runController: AbortController;
    /**
     * Filled in by whichever mode ran, so the caller's `catch` can put the nodes it created into the
     * error state. It has to be mutable and it has to survive the mode returning early, which is why
     * it is a field on the run rather than a return value.
     */
    pendingChildIds: string[];
};

export type GenerationRunDeps = {
    setNodes: Dispatch<SetStateAction<CanvasNodeData[]>>;
    setConnections: Dispatch<SetStateAction<CanvasConnection[]>>;
    setSelectedNodeIds: Dispatch<SetStateAction<Set<string>>>;
    setSelectedConnectionId: Dispatch<SetStateAction<string | null>>;
    setDialogNodeId: Dispatch<SetStateAction<string | null>>;
    setExpandedBatchNodeIds: Dispatch<SetStateAction<Set<string>>>;
    startGenerationRequest: (targetNodeId: string, originNodeId: string, runningId?: string, controller?: AbortController, expectedProgressCount?: number) => AbortController;
    finishGenerationRequest: (targetNodeId: string, controller: AbortController) => void;
    completeVideoNodeTask: (
        nodeId: string,
        config: GenerationRun["generationConfig"],
        prompt: string,
        images: Parameters<typeof import("@/services/api/video").createVideoGenerationTask>[2],
        signal: AbortSignal,
        extra?: CanvasNodeData["metadata"],
        videos?: ReferenceVideo[],
        audios?: ReferenceAudio[],
    ) => Promise<void>;
};

/**
 * The four things "generate" can mean.
 *
 * They were four branches of a single 440-line `handleGenerateNode`, sharing nothing but the setup
 * above and the `catch` in the caller. Each one spawns or repurposes nodes, drives its own request
 * shape — parallel images, a polled video task, a single audio clip, a streamed text answer — and
 * then stamps the outcome onto the graph.
 */
export function useGenerationRuns(deps: GenerationRunDeps) {
    const { setNodes, setConnections, setSelectedNodeIds, setSelectedConnectionId, setDialogNodeId, setExpandedBatchNodeIds, startGenerationRequest, finishGenerationRequest, completeVideoNodeTask } = deps;
    const { t } = useTranslation();

    const runImageGeneration = useCallback(async (run: GenerationRun) => {
        const { nodeId, prompt, sourceNode, effectivePrompt, generationConfig, generationContext, overwriteExisting, runController } = run;
        const count = getGenerationCount(generationConfig.count);
        const isConfigNode = sourceNode?.type === CanvasNodeType.Config;
        const isImageNode = sourceNode?.type === CanvasNodeType.Image;
        const isEmptyImageNode = isImageNode && !sourceNode?.metadata?.content;
        const reuseImageNode = isImageNode && (overwriteExisting || isEmptyImageNode);
        const sourceReference =
            isImageNode && sourceNode?.metadata?.content && !overwriteExisting && !generationContext.referenceImages.length
                ? [{ id: sourceNode.id, name: `${sourceNode.title || sourceNode.id}.png`, type: sourceNode.metadata.mimeType || "image/png", dataUrl: sourceNode.metadata.content, storageKey: sourceNode.metadata.storageKey }]
                : [];
        const referenceImages = [...new Map([...sourceReference, ...generationContext.referenceImages].map((image) => [image.id, image])).values()];
        const generationType = referenceImages.length ? ("edit" as const) : ("generation" as const);
        const generationMetadata = buildImageGenerationMetadata(generationType, generationConfig, count, referenceImages);
        const parentConfig = NODE_DEFAULT_SIZE[isConfigNode ? CanvasNodeType.Config : isImageNode ? CanvasNodeType.Image : CanvasNodeType.Text];
        const imageConfig = NODE_DEFAULT_SIZE[CanvasNodeType.Image];
        const parentPosition = sourceNode?.position || { x: 0, y: 0 };
        const rootId = reuseImageNode ? nodeId : nanoid();
        const imageIds = Array.from({ length: count }, () => nanoid());
        run.pendingChildIds = [rootId];
        const rootNode: CanvasNodeData = {
            id: rootId,
            type: CanvasNodeType.Image,
            title: effectivePrompt.slice(0, 32) || "Generated Image",
            position: {
                x: reuseImageNode ? parentPosition.x : parentPosition.x + parentConfig.width + 96,
                y: parentPosition.y + parentConfig.height / 2 - imageConfig.height / 2,
            },
            width: reuseImageNode ? sourceNode?.width || imageConfig.width : imageConfig.width,
            height: reuseImageNode ? sourceNode?.height || imageConfig.height : imageConfig.height,
            metadata: {
                prompt: effectivePrompt,
                status: NODE_STATUS_LOADING,
                images: imageIds.map((id) => ({ id, status: NODE_STATUS_LOADING, content: "", naturalWidth: 0, naturalHeight: 0, bytes: 0, mimeType: "" })),
                primaryImageId: undefined,
                ...generationMetadata,
            },
        };

        setNodes((prev) => [
            ...prev.map((node) =>
                node.id === nodeId
                    ? isConfigNode
                        ? {
                              ...node,
                              metadata: { ...node.metadata, status: NODE_STATUS_LOADING, errorDetails: undefined },
                          }
                        : reuseImageNode
                          ? {
                                ...node,
                                position: rootNode.position,
                                width: rootNode.width,
                                height: rootNode.height,
                                title: rootNode.title,
                                metadata: { ...node.metadata, ...rootNode.metadata, content: undefined, storageKey: undefined, naturalWidth: undefined, naturalHeight: undefined, bytes: undefined, mimeType: undefined, errorDetails: undefined },
                            }
                          : isImageNode
                            ? {
                                  ...node,
                                  metadata: { ...node.metadata, status: NODE_STATUS_SUCCESS, errorDetails: undefined },
                              }
                            : {
                                  ...node,
                                  type: CanvasNodeType.Text,
                                  title: prompt.slice(0, 32) || "Prompt",
                                  width: parentConfig.width,
                                  height: parentConfig.height,
                                  metadata: { ...node.metadata, content: prompt, prompt, status: NODE_STATUS_SUCCESS, fontSize: 14, errorDetails: undefined },
                              }
                    : node,
            ),
            ...(reuseImageNode ? [] : [rootNode]),
        ]);
        if (!reuseImageNode) setConnections((prev) => [...prev, { id: nanoid(), fromNodeId: nodeId, toNodeId: rootId }]);
        setSelectedNodeIds(new Set([nodeId]));
        setSelectedConnectionId(null);
        setDialogNodeId(nodeId);

        const controller = startGenerationRequest(rootId, nodeId, nodeId, runController, count);
        let hasSuccess = false;
        let hasFailure = false;
        let firstError = "";
        await Promise.all(
            imageIds.map(async (imageId) => {
                try {
                    const image = referenceImages.length
                        ? await requestEdit({ ...generationConfig, count: "1" }, effectivePrompt, referenceImages, { signal: controller.signal }).then((items) => items[0])
                        : await requestGeneration({ ...generationConfig, count: "1" }, effectivePrompt, { signal: controller.signal }).then((items) => items[0]);
                    const uploaded = await uploadImage(image.dataUrl, { signal: controller.signal });
                    const imageSize = fitNodeSize(uploaded.width, uploaded.height, imageConfig.width, imageConfig.height);
                    const item: CanvasNodeImage = { id: imageId, status: NODE_STATUS_SUCCESS, content: uploaded.url, storageKey: uploaded.storageKey, naturalWidth: uploaded.width, naturalHeight: uploaded.height, bytes: uploaded.bytes, mimeType: uploaded.mimeType };
                    setNodes((prev) =>
                        prev.map((node) => {
                            if (node.id !== rootId) return node;
                            const images = node.metadata?.images?.map((image) => (image.id === imageId ? item : image)) || [];
                            if (node.metadata?.primaryImageId) return { ...node, metadata: { ...node.metadata, images } };
                            const center = { x: node.position.x + node.width / 2, y: node.position.y + node.height / 2 };
                            return {
                                ...node,
                                position: { x: center.x - imageSize.width / 2, y: center.y - imageSize.height / 2 },
                                ...imageSize,
                                metadata: {
                                    ...node.metadata,
                                    content: item.content,
                                    storageKey: item.storageKey,
                                    naturalWidth: item.naturalWidth,
                                    naturalHeight: item.naturalHeight,
                                    bytes: item.bytes,
                                    mimeType: item.mimeType,
                                    images,
                                    primaryImageId: imageId,
                                },
                            };
                        }),
                    );
                    hasSuccess = true;
                    if (isConfigNode) setNodes((prev) => prev.map((node) => (node.id === nodeId ? { ...node, metadata: { ...node.metadata, status: NODE_STATUS_SUCCESS, errorDetails: undefined } } : node)));
                    return true;
                } catch (error) {
                    if (isGenerationCanceled(error)) return false;
                    const errorDetails = error instanceof Error ? error.message : t("canvas.runtime.generationFailed");
                    if (!firstError) firstError = errorDetails;
                    hasFailure = true;
                    setNodes((prev) => prev.map((node) => (node.id === rootId ? { ...node, metadata: { ...node.metadata, images: node.metadata?.images?.map((image) => (image.id === imageId ? { ...image, status: NODE_STATUS_ERROR, errorDetails } : image)) } } : node)));
                }
                return false;
            }),
        );
        if (rootId !== nodeId) finishGenerationRequest(rootId, controller);
        if (controller.signal.aborted) {
            setNodes((prev) => prev.map((node) => (node.id === nodeId && isConfigNode && node.metadata?.status === NODE_STATUS_LOADING ? { ...node, metadata: { ...node.metadata, status: NODE_STATUS_IDLE, errorDetails: undefined } } : node)));
            return;
        }
        if (hasFailure) {
            toast.error(hasSuccess ? t("canvas.runtime.partialFailed") : firstError || t("canvas.runtime.generationFailed"));
        }
        setNodes((prev) =>
            prev.map((node) =>
                node.id === nodeId && isConfigNode
                    ? { ...node, metadata: { ...node.metadata, status: hasSuccess ? NODE_STATUS_SUCCESS : NODE_STATUS_ERROR, errorDetails: hasSuccess ? undefined : t("canvas.runtime.generationFailed") } }
                    : node.id === rootId
                      ? { ...node, metadata: { ...node.metadata, status: hasSuccess ? NODE_STATUS_SUCCESS : NODE_STATUS_ERROR, errorDetails: hasSuccess ? undefined : t("canvas.runtime.allFailed") } }
                        : node,
            ),
        );
    }, [finishGenerationRequest, startGenerationRequest, t]);

    const runVideoGeneration = useCallback(async (run: GenerationRun) => {
        const { nodeId, sourceNode, effectivePrompt, generationConfig, generationContext, overwriteExisting, runController } = run;
        const spec = nodeSizeFromRatio(generationConfig.size, NODE_DEFAULT_SIZE[CanvasNodeType.Video].width, NODE_DEFAULT_SIZE[CanvasNodeType.Video].height) || NODE_DEFAULT_SIZE[CanvasNodeType.Video];
        const isEmptyVideoNode = sourceNode?.type === CanvasNodeType.Video && !sourceNode.metadata?.content;
        const reuseVideoNode = sourceNode?.type === CanvasNodeType.Video && (overwriteExisting || isEmptyVideoNode);
        const videoId = reuseVideoNode ? nodeId : nanoid();
        const parent = sourceNode?.position || { x: 0, y: 0 };
        const videoNode: CanvasNodeData = {
            id: videoId,
            type: CanvasNodeType.Video,
            title: effectivePrompt.slice(0, 32) || "Generated Video",
            position: reuseVideoNode ? sourceNode.position : { x: parent.x + (sourceNode?.width || spec.width) + 96, y: parent.y },
            width: reuseVideoNode ? sourceNode.width : spec.width,
            height: reuseVideoNode ? sourceNode.height : spec.height,
            metadata: {
                prompt: effectivePrompt,
                status: NODE_STATUS_LOADING,
                model: generationConfig.model,
                size: generationConfig.size,
                seconds: generationConfig.videoSeconds,
                vquality: generationConfig.vquality,
                generateAudio: generationConfig.videoGenerateAudio,
                watermark: generationConfig.videoWatermark,
                videoMode: generationConfig.videoMode,
                references: generationReferenceUrls(generationContext),
            },
        };
        run.pendingChildIds = [videoId];
        setNodes((prev) =>
            reuseVideoNode
                ? prev.map((node) => (node.id === nodeId ? { ...node, ...videoNode } : node))
                : [...prev.map((node) => (node.id === nodeId ? { ...node, metadata: { ...node.metadata, status: NODE_STATUS_SUCCESS } } : node)), videoNode],
        );
        if (!reuseVideoNode) setConnections((prev) => [...prev, { id: nanoid(), fromNodeId: nodeId, toNodeId: videoId }]);
        const controller = startGenerationRequest(videoId, nodeId, nodeId, runController);
        try {
            await completeVideoNodeTask(videoId, generationConfig, effectivePrompt, generationContext.referenceImages, controller.signal, {
                size: generationConfig.size,
                seconds: generationConfig.videoSeconds,
                vquality: generationConfig.vquality,
                generateAudio: generationConfig.videoGenerateAudio,
                watermark: generationConfig.videoWatermark,
                videoMode: generationConfig.videoMode,
                references: generationReferenceUrls(generationContext),
            }, generationContext.referenceVideos, generationContext.referenceAudios);
        } finally {
            finishGenerationRequest(videoId, controller);
        }
        return;
    }, [completeVideoNodeTask, finishGenerationRequest, startGenerationRequest, t]);

    const runAudioGeneration = useCallback(async (run: GenerationRun) => {
        const { nodeId, sourceNode, effectivePrompt, generationConfig, overwriteExisting, runController } = run;
        const spec = NODE_DEFAULT_SIZE[CanvasNodeType.Audio];
        const isEmptyAudioNode = sourceNode?.type === CanvasNodeType.Audio && !sourceNode.metadata?.content;
        const reuseAudioNode = sourceNode?.type === CanvasNodeType.Audio && (overwriteExisting || isEmptyAudioNode);
        const audioId = reuseAudioNode ? nodeId : nanoid();
        const parent = sourceNode?.position || { x: 0, y: 0 };
        const audioNode: CanvasNodeData = {
            id: audioId,
            type: CanvasNodeType.Audio,
            title: effectivePrompt.slice(0, 32) || "Generated Audio",
            position: reuseAudioNode ? sourceNode.position : { x: parent.x + (sourceNode?.width || spec.width) + 96, y: parent.y + ((sourceNode?.height || spec.height) - spec.height) / 2 },
            width: reuseAudioNode ? sourceNode.width : spec.width,
            height: reuseAudioNode ? sourceNode.height : spec.height,
            metadata: { prompt: effectivePrompt, status: NODE_STATUS_LOADING, ...buildAudioGenerationMetadata(generationConfig) },
        };
        run.pendingChildIds = [audioId];
        setNodes((prev) =>
            reuseAudioNode
                ? prev.map((node) => (node.id === nodeId ? { ...node, ...audioNode } : node))
                : [...prev.map((node) => (node.id === nodeId ? { ...node, metadata: { ...node.metadata, status: NODE_STATUS_SUCCESS } } : node)), audioNode],
        );
        if (!reuseAudioNode) setConnections((prev) => [...prev, { id: nanoid(), fromNodeId: nodeId, toNodeId: audioId }]);
        const controller = startGenerationRequest(audioId, nodeId, nodeId, runController);
        try {
            const audio = await storeGeneratedAudio(await requestAudioGeneration(generationConfig, effectivePrompt, { signal: controller.signal }), generationConfig.audioFormat);
            setNodes((prev) => prev.map((node) => (node.id === audioId ? { ...node, metadata: { ...node.metadata, ...audioMetadata(audio), prompt: effectivePrompt, ...buildAudioGenerationMetadata(generationConfig) } } : node)));
        } finally {
            finishGenerationRequest(audioId, controller);
        }
        return;
    }, [finishGenerationRequest, startGenerationRequest, t]);

    const runTextGeneration = useCallback(async (run: GenerationRun) => {
        const { nodeId, sourceNode, sourceTextContent, effectivePrompt, generationConfig, generationContext, overwriteExisting, runController } = run;
        const isConfigNode = sourceNode?.type === CanvasNodeType.Config;
        const textCount = getGenerationCount(String(sourceNode?.metadata?.textCount || 1));
        const parentConfig = NODE_DEFAULT_SIZE[isConfigNode ? CanvasNodeType.Config : CanvasNodeType.Text];
        const textConfig = NODE_DEFAULT_SIZE[CanvasNodeType.Text];
        const parentPosition = sourceNode?.position || { x: 0, y: 0 };
        const isEmptyTextNode = sourceNode?.type === CanvasNodeType.Text && !sourceTextContent;
        const reuseTextNode = sourceNode?.type === CanvasNodeType.Text && (overwriteExisting || isEmptyTextNode);
        const rootId = reuseTextNode ? nodeId : nanoid();
        const textIds = Array.from({ length: textCount }, () => nanoid());
        const rootNode: CanvasNodeData = {
            id: rootId,
            type: CanvasNodeType.Text,
            title: effectivePrompt.slice(0, 32) || "Generated Text",
            position: reuseTextNode ? sourceNode.position : { x: parentPosition.x + parentConfig.width + 96, y: parentPosition.y + parentConfig.height / 2 - textConfig.height / 2 },
            width: reuseTextNode ? sourceNode.width : textConfig.width,
            height: reuseTextNode ? sourceNode.height : textConfig.height,
            metadata: {
                prompt: effectivePrompt,
                status: NODE_STATUS_LOADING,
                fontSize: 14,
                model: generationConfig.model,
                reasoningEffort: generationConfig.reasoningEffort,
                textCount,
                texts: textIds.map((id) => ({ id, status: NODE_STATUS_LOADING, content: "" })),
                primaryTextId: textIds[0],
            },
        };
        run.pendingChildIds = [rootId];
        setNodes((prev) =>
            reuseTextNode
                ? prev.map((node) => (node.id === nodeId ? { ...node, ...rootNode } : node))
                : [...prev.map((node) => (node.id === nodeId && isConfigNode ? { ...node, metadata: { ...node.metadata, status: NODE_STATUS_LOADING, errorDetails: undefined } } : node)), rootNode],
        );
        if (!reuseTextNode) setConnections((prev) => [...prev, { id: nanoid(), fromNodeId: nodeId, toNodeId: rootId }]);
        setSelectedNodeIds(new Set([nodeId]));
        setSelectedConnectionId(null);
        setDialogNodeId(nodeId);

        const controller = rootId === nodeId ? runController : startGenerationRequest(rootId, nodeId, nodeId, runController);
        const results = await Promise.all(
            textIds.map(async (textId): Promise<CanvasNodeText | null> => {
                let streamed = "";
                try {
                    const answer = await requestImageQuestion(
                        generationConfig,
                        buildNodeResponseMessages({ ...generationContext, prompt: effectivePrompt }),
                        (text) => {
                            streamed = text;
                            setNodes((prev) =>
                                prev.map((node) =>
                                    node.id === rootId
                                        ? {
                                              ...node,
                                              metadata: {
                                                  ...node.metadata,
                                                  ...(node.metadata?.primaryTextId === textId ? { content: text } : {}),
                                                  texts: node.metadata?.texts?.map((item) => (item.id === textId ? { ...item, content: text } : item)),
                                              },
                                          }
                                        : node,
                                ),
                            );
                        },
                        { signal: controller.signal },
                    );
                    const content = answer || streamed;
                    setNodes((prev) =>
                        prev.map((node) =>
                            node.id === rootId
                                ? {
                                      ...node,
                                      metadata: {
                                          ...node.metadata,
                                          ...(node.metadata?.primaryTextId === textId ? { content } : {}),
                                          texts: node.metadata?.texts?.map((item) => (item.id === textId ? { ...item, content, status: NODE_STATUS_SUCCESS } : item)),
                                      },
                                  }
                                : node,
                        ),
                    );
                    return { id: textId, status: NODE_STATUS_SUCCESS, content } satisfies CanvasNodeText;
                } catch (error) {
                    if (isGenerationCanceled(error)) return null;
                    const errorDetails = error instanceof Error ? error.message : t("canvas.runtime.generationFailed");
                    setNodes((prev) => prev.map((node) => (node.id === rootId ? { ...node, metadata: { ...node.metadata, texts: node.metadata?.texts?.map((item) => (item.id === textId ? { ...item, status: NODE_STATUS_ERROR, errorDetails } : item)) } } : node)));
                    return { id: textId, status: NODE_STATUS_ERROR, content: "", errorDetails } satisfies CanvasNodeText;
                }
            }),
        );
        if (rootId !== nodeId) finishGenerationRequest(rootId, controller);
        if (controller.signal.aborted) return;
        const completedTexts = results.flatMap((item) => (item?.status === NODE_STATUS_SUCCESS ? [item] : []));
        const failedTexts = results.filter((item) => item?.status === NODE_STATUS_ERROR);
        const firstText = completedTexts[0];
        if (completedTexts.length <= 1) setExpandedBatchNodeIds((current) => new Set([...current].filter((id) => id !== rootId)));
        if (failedTexts.length) toast.error(firstText ? t("canvas.runtime.partialTextFailed") : failedTexts[0]?.errorDetails || t("canvas.runtime.generationFailed"));
        setNodes((prev) =>
            prev.map((node) => {
                if (node.id === rootId) {
                    const primaryText = completedTexts.find((text) => text.id === node.metadata?.primaryTextId) || firstText;
                    return {
                        ...node,
                        metadata: {
                            ...node.metadata,
                            content: primaryText?.content || "",
                            texts: completedTexts,
                            primaryTextId: primaryText?.id,
                            status: primaryText ? NODE_STATUS_SUCCESS : NODE_STATUS_ERROR,
                            errorDetails: primaryText ? undefined : t("canvas.runtime.generationFailed"),
                        },
                    };
                }
                return node.id === nodeId && isConfigNode ? { ...node, metadata: { ...node.metadata, status: firstText ? NODE_STATUS_SUCCESS : NODE_STATUS_ERROR, errorDetails: firstText ? undefined : t("canvas.runtime.generationFailed") } } : node;
            }),
        );
    }, [finishGenerationRequest, setExpandedBatchNodeIds, startGenerationRequest, t]);

    return { runImageGeneration, runVideoGeneration, runAudioGeneration, runTextGeneration };
}
