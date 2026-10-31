import { useCallback, useRef } from "react";
import type { Dispatch, MutableRefObject, SetStateAction } from "react";
import { useTranslation } from "react-i18next";
import { nanoid } from "nanoid";

import { toast } from "@/components/kit/toast";
import { requestAudioGeneration, storeGeneratedAudio } from "@/services/api/audio";
import { isGatewayImageModel, requestEdit, requestGeneration } from "@/services/api/image";
import { requestPromptChat } from "@/services/api/prompts";
import { downloadRunOutput, downloadRunOutputs, waitForRun, type Run, type RunProgress } from "@/services/api/runs";
import { isVideoTaskFailed } from "@/services/api/video";
import { resolveModelRequestConfig, useConfigStore, useEffectiveConfig } from "@/stores/use-config-store";
import { readFileAsDataUrl } from "@/lib/image-utils";
import { uploadImage } from "@/services/image-storage";
import { NODE_DEFAULT_SIZE } from "@/constant/canvas";
import { fitNodeSize, nodeSizeFromRatio } from "@/lib/canvas/canvas-node-size";
import { audioMetadata, buildAudioGenerationMetadata, buildImageGenerationMetadata, imageMetadata } from "@/lib/canvas/canvas-node-factory";
import { buildGenerationConfig, findRetrySourceNode, generationReferenceUrls, getGenerationCount, hasResumableVideoTask, isGenerationCanceled, resolveMetadataReferences, sourceNodeReferenceImages } from "@/lib/canvas/canvas-generation-helpers";
import { NODE_STATUS_ERROR, NODE_STATUS_IDLE, NODE_STATUS_LOADING, NODE_STATUS_SUCCESS } from "@/lib/canvas/canvas-node-status";
import { buildNodeGenerationContext, hydrateNodeGenerationContext } from "@/components/canvas/canvas-node-generation";
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
    nodesRef: MutableRefObject<CanvasNodeData[]>;
    connectionsRef: MutableRefObject<CanvasConnection[]>;
    setRunningNodeId: Dispatch<SetStateAction<string | null>>;
    pollVideoNodeTask: (node: CanvasNodeData, silent?: boolean) => Promise<void>;
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
    const {
        setNodes,
        setConnections,
        setSelectedNodeIds,
        setSelectedConnectionId,
        setDialogNodeId,
        setExpandedBatchNodeIds,
        nodesRef,
        connectionsRef,
        setRunningNodeId,
        pollVideoNodeTask,
        startGenerationRequest,
        finishGenerationRequest,
        completeVideoNodeTask,
    } = deps;
    const { t } = useTranslation();
    const effectiveConfig = useEffectiveConfig();
    const isAiConfigReady = useConfigStore((state) => state.isAiConfigReady);
    const openConfigDialog = useConfigStore((state) => state.openConfigDialog);

    /** Node ids whose run is currently being polled, so a resume and a live run cannot both poll. */
    const audioPollIdsRef = useRef(new Set<string>());
    /** The same guard for image batches, which resume from the same kind of run. */
    const imagePollIdsRef = useRef(new Set<string>());

    /**
     * Run progress is the only source of the step ring for audio: the node is not a video task, so
     * there is no preview stream to read steps from.
     */
    const setRunStepProgress = useCallback(
        (nodeId: string, progress?: RunProgress) => {
            const total = progress?.total && progress.total > 0 ? progress.total : 0;
            const step = Math.max(0, progress?.step || 0);
            setNodes((prev) => prev.map((node) => (node.id === nodeId ? { ...node, metadata: { ...node.metadata, stepProgress: total > 0 ? { step: Math.min(total, step), total } : undefined } } : node)));
        },
        [setNodes],
    );

    /**
     * Turn one artifact of a run into a canvas image slot.
     *
     * Called by a live batch and by the resume path alike, which is what makes a reloaded batch
     * indistinguishable from one that never stopped: same upload, same node resize, same primary
     * image choice.
     */
    const fillImageSlot = useCallback(
        async (rootId: string, imageId: string, dataUrl: string, signal: AbortSignal) => {
            const uploaded = await uploadImage(dataUrl, { signal });
            const imageConfig = NODE_DEFAULT_SIZE[CanvasNodeType.Image];
            const imageSize = fitNodeSize(uploaded.width, uploaded.height, imageConfig.width, imageConfig.height);
            const item: CanvasNodeImage = {
                id: imageId,
                status: NODE_STATUS_SUCCESS,
                content: uploaded.url,
                storageKey: uploaded.storageKey,
                naturalWidth: uploaded.width,
                naturalHeight: uploaded.height,
                bytes: uploaded.bytes,
                mimeType: uploaded.mimeType,
            };
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
        },
        [setNodes],
    );

    const failImageSlotOn = useCallback(
        (rootId: string, imageId: string, errorDetails: string) => {
            setNodes((prev) =>
                prev.map((node) => (node.id === rootId ? { ...node, metadata: { ...node.metadata, images: node.metadata?.images?.map((image) => (image.id === imageId ? { ...image, status: NODE_STATUS_ERROR, errorDetails } : image)) } } : node)),
            );
        },
        [setNodes],
    );

    const runImageGeneration = useCallback(
        async (run: GenerationRun) => {
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
            const fillSlot = (imageId: string, dataUrl: string) => fillImageSlot(rootId, imageId, dataUrl, controller.signal);
            const failImageSlot = (imageId: string, errorDetails: string) => failImageSlotOn(rootId, imageId, errorDetails);
            const markConfigSuccess = () => {
                if (!isConfigNode) return;
                setNodes((prev) => prev.map((node) => (node.id === nodeId ? { ...node, metadata: { ...node.metadata, status: NODE_STATUS_SUCCESS, errorDetails: undefined } } : node)));
            };

            if (isGatewayImageModel(generationConfig)) {
                // One run for the whole batch: the gateway returns every artifact of a run, so asking
                // for `count` once avoids loading the model once per image.
                try {
                    const requestOptions = {
                        signal: controller.signal,
                        onProgress: (state: Run) => setRunStepProgress(rootId, state.progress),
                        // The id arrives at submission, before a single artifact exists: it is what a
                        // reload reattaches to.
                        onRun: (runId: string) => setNodes((prev) => prev.map((node) => (node.id === rootId ? { ...node, metadata: { ...node.metadata, imageRunId: runId } } : node))),
                    };
                    const batchConfig = { ...generationConfig, count: String(count) };
                    const results = referenceImages.length ? await requestEdit(batchConfig, effectivePrompt, referenceImages, requestOptions) : await requestGeneration(batchConfig, effectivePrompt, requestOptions);
                    for (let index = 0; index < imageIds.length; index += 1) {
                        const result = results[index];
                        if (!result) {
                            const errorDetails = t("canvas.runtime.generationFailed");
                            if (!firstError) firstError = errorDetails;
                            hasFailure = true;
                            failImageSlot(imageIds[index], errorDetails);
                            continue;
                        }
                        try {
                            await fillSlot(imageIds[index], result.dataUrl);
                            hasSuccess = true;
                        } catch (error) {
                            const errorDetails = error instanceof Error ? error.message : t("canvas.runtime.generationFailed");
                            if (!firstError) firstError = errorDetails;
                            hasFailure = true;
                            failImageSlot(imageIds[index], errorDetails);
                        }
                    }
                    if (hasSuccess) markConfigSuccess();
                } catch (error) {
                    if (isGenerationCanceled(error)) return;
                    const errorDetails = error instanceof Error ? error.message : t("canvas.runtime.generationFailed");
                    if (!firstError) firstError = errorDetails;
                    hasFailure = true;
                    imageIds.forEach((imageId) => failImageSlot(imageId, errorDetails));
                } finally {
                    setRunStepProgress(rootId, undefined);
                }
            } else {
                await Promise.all(
                    imageIds.map(async (imageId) => {
                        try {
                            const image = referenceImages.length
                                ? await requestEdit({ ...generationConfig, count: "1" }, effectivePrompt, referenceImages, { signal: controller.signal }).then((items) => items[0])
                                : await requestGeneration({ ...generationConfig, count: "1" }, effectivePrompt, { signal: controller.signal }).then((items) => items[0]);
                            await fillSlot(imageId, image.dataUrl);
                            hasSuccess = true;
                            markConfigSuccess();
                            return true;
                        } catch (error) {
                            if (isGenerationCanceled(error)) return false;
                            const errorDetails = error instanceof Error ? error.message : t("canvas.runtime.generationFailed");
                            if (!firstError) firstError = errorDetails;
                            hasFailure = true;
                            failImageSlot(imageId, errorDetails);
                        }
                        return false;
                    }),
                );
            }
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
                          ? {
                                ...node,
                                metadata: {
                                    ...node.metadata,
                                    status: hasSuccess ? NODE_STATUS_SUCCESS : NODE_STATUS_ERROR,
                                    errorDetails: hasSuccess ? undefined : t("canvas.runtime.allFailed"),
                                    imageRunId: undefined,
                                },
                            }
                          : node,
                ),
            );
        },
        [finishGenerationRequest, startGenerationRequest, t],
    );

    const runVideoGeneration = useCallback(
        async (run: GenerationRun) => {
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
                reuseVideoNode ? prev.map((node) => (node.id === nodeId ? { ...node, ...videoNode } : node)) : [...prev.map((node) => (node.id === nodeId ? { ...node, metadata: { ...node.metadata, status: NODE_STATUS_SUCCESS } } : node)), videoNode],
            );
            if (!reuseVideoNode) setConnections((prev) => [...prev, { id: nanoid(), fromNodeId: nodeId, toNodeId: videoId }]);
            const controller = startGenerationRequest(videoId, nodeId, nodeId, runController);
            try {
                await completeVideoNodeTask(
                    videoId,
                    generationConfig,
                    effectivePrompt,
                    generationContext.referenceImages,
                    controller.signal,
                    {
                        size: generationConfig.size,
                        seconds: generationConfig.videoSeconds,
                        vquality: generationConfig.vquality,
                        generateAudio: generationConfig.videoGenerateAudio,
                        watermark: generationConfig.videoWatermark,
                        videoMode: generationConfig.videoMode,
                        references: generationReferenceUrls(generationContext),
                    },
                    generationContext.referenceVideos,
                    generationContext.referenceAudios,
                );
            } finally {
                finishGenerationRequest(videoId, controller);
            }
            return;
        },
        [completeVideoNodeTask, finishGenerationRequest, startGenerationRequest, t],
    );

    const runAudioGeneration = useCallback(
        async (run: GenerationRun) => {
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
                reuseAudioNode ? prev.map((node) => (node.id === nodeId ? { ...node, ...audioNode } : node)) : [...prev.map((node) => (node.id === nodeId ? { ...node, metadata: { ...node.metadata, status: NODE_STATUS_SUCCESS } } : node)), audioNode],
            );
            if (!reuseAudioNode) setConnections((prev) => [...prev, { id: nanoid(), fromNodeId: nodeId, toNodeId: audioId }]);
            const controller = startGenerationRequest(audioId, nodeId, nodeId, runController);
            try {
                const audio = await storeGeneratedAudio(
                    await requestAudioGeneration(generationConfig, effectivePrompt, {
                        signal: controller.signal,
                        // The id lands on the node before the first poll, so a reload can resume it.
                        onRun: (runId) => setNodes((prev) => prev.map((node) => (node.id === audioId ? { ...node, metadata: { ...node.metadata, audioRunId: runId } } : node))),
                        onProgress: (run) => setRunStepProgress(audioId, run.progress),
                    }),
                    generationConfig.audioFormat,
                );
                setNodes((prev) =>
                    prev.map((node) =>
                        node.id === audioId ? { ...node, metadata: { ...node.metadata, ...audioMetadata(audio), prompt: effectivePrompt, ...buildAudioGenerationMetadata(generationConfig), audioRunId: undefined, stepProgress: undefined } } : node,
                    ),
                );
            } finally {
                finishGenerationRequest(audioId, controller);
            }
            return;
        },
        [finishGenerationRequest, setNodes, setRunStepProgress, startGenerationRequest],
    );

    const runTextGeneration = useCallback(
        async (run: GenerationRun) => {
            const { nodeId, sourceNode, sourceTextContent, effectivePrompt, generationConfig, generationContext, overwriteExisting, runController } = run;
            const isConfigNode = sourceNode?.type === CanvasNodeType.Config;
            // One prompt, one answer: a connected agent answers exactly one question, so a batch of texts
            // is no longer a thing (the count control went with the old text settings).
            const textCount = 1;
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
                    try {
                        // The agent answers as it goes, so the node fills in while it talks.
                        const content = await requestPromptChat(
                            generationConfig,
                            { prompt: effectivePrompt, images: generationContext.referenceImages },
                            {
                                signal: controller.signal,
                                onDelta: (text) =>
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
                                    ),
                            },
                        );
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
                        setNodes((prev) =>
                            prev.map((node) => (node.id === rootId ? { ...node, metadata: { ...node.metadata, texts: node.metadata?.texts?.map((item) => (item.id === textId ? { ...item, status: NODE_STATUS_ERROR, errorDetails } : item)) } } : node)),
                        );
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
        },
        [finishGenerationRequest, setExpandedBatchNodeIds, startGenerationRequest, t],
    );

    /**
     * Retry is not generate. It has no mode and spawns nothing: it re-runs one request against the
     * node that already exists, into the image slot that failed if there is one. The branches below
     * are the same four types, but each writes back in place, which is why they are short and why
     * this could not simply be folded into the dispatch above.
     */
    const retryNode = useCallback(
        async (node: CanvasNodeData, imageId?: string) => {
            if (hasResumableVideoTask(node)) {
                await pollVideoNodeTask(node);
                return;
            }
            const sourceNode = findRetrySourceNode(node.id, nodesRef.current, connectionsRef.current) || node;
            const savedImageMetadata = node.type === CanvasNodeType.Image ? node.metadata : undefined;
            const hasSavedImageMetadata = Boolean(savedImageMetadata?.generationType);
            const generationConfig =
                hasSavedImageMetadata && savedImageMetadata
                    ? {
                          ...effectiveConfig,
                          model: savedImageMetadata.model || effectiveConfig.imageModel || effectiveConfig.model,
                          quality: savedImageMetadata.quality || effectiveConfig.quality,
                          size: savedImageMetadata.size || effectiveConfig.size,
                          background: savedImageMetadata.background ?? effectiveConfig.background,
                          params: savedImageMetadata.params,
                          count: "1",
                      }
                    : { ...buildGenerationConfig(effectiveConfig, sourceNode, node.type === CanvasNodeType.Text ? "text" : node.type === CanvasNodeType.Video ? "video" : node.type === CanvasNodeType.Audio ? "audio" : "image"), count: "1" };
            // A text node retries against the agent, which needs no configuration here.
            if (node.type !== CanvasNodeType.Text && !isAiConfigReady(generationConfig, generationConfig.model)) {
                openConfigDialog(true);
                return;
            }

            const context = hasSavedImageMetadata ? null : await hydrateNodeGenerationContext(buildNodeGenerationContext(sourceNode.id, nodesRef.current, connectionsRef.current, sourceNode.metadata?.prompt || node.metadata?.prompt || ""));
            if (context?.loras.length) generationConfig.params = { ...(generationConfig.params || {}), loras: context.loras };
            const prompt = (savedImageMetadata?.prompt || context?.prompt || "").trim();
            if (!prompt) {
                toast.warning(t("canvas.runtime.retryPromptMissing"));
                return;
            }
            const generationType = savedImageMetadata?.generationType;
            const useReferenceImages = generationType ? generationType === "edit" : Boolean(context?.referenceImages.length);
            const retryReferenceImages =
                hasSavedImageMetadata && savedImageMetadata ? await resolveMetadataReferences(savedImageMetadata) : useReferenceImages ? (context?.referenceImages.length ? context.referenceImages : sourceNodeReferenceImages(sourceNode)) : [];
            if (useReferenceImages && !retryReferenceImages) {
                toast.error(t("canvas.runtime.referenceMissing"));
                setNodes((prev) =>
                    prev.map((item) =>
                        item.id === node.id
                            ? {
                                  ...item,
                                  metadata: {
                                      ...item.metadata,
                                      status: item.metadata?.content ? NODE_STATUS_SUCCESS : NODE_STATUS_ERROR,
                                      errorDetails: item.metadata?.content ? undefined : t("canvas.runtime.referenceMissing"),
                                      images: item.metadata?.images?.map((image) => (image.id === imageId ? { ...image, status: NODE_STATUS_ERROR, errorDetails: t("canvas.runtime.referenceMissing") } : image)),
                                  },
                              }
                            : item,
                    ),
                );
                return;
            }
            const retryImages = retryReferenceImages || [];

            setRunningNodeId(node.id);
            setNodes((prev) =>
                prev.map((item) =>
                    item.id === node.id
                        ? {
                              ...item,
                              metadata: {
                                  ...item.metadata,
                                  status: NODE_STATUS_LOADING,
                                  errorDetails: undefined,
                                  images: item.metadata?.images?.map((image) => (image.id === imageId ? { ...image, status: NODE_STATUS_LOADING, errorDetails: undefined } : image)),
                              },
                          }
                        : item,
                ),
            );
            const controller = startGenerationRequest(node.id, sourceNode.id, node.id);

            try {
                if (node.type === CanvasNodeType.Text) {
                    if (!context) return;
                    const answer = await requestPromptChat(
                        generationConfig,
                        { prompt, images: context.referenceImages },
                        {
                            signal: controller.signal,
                            onDelta: (text) => setNodes((prev) => prev.map((item) => (item.id === node.id ? { ...item, type: CanvasNodeType.Text, metadata: { ...item.metadata, content: text, status: NODE_STATUS_LOADING } } : item))),
                        },
                    );
                    setNodes((prev) => prev.map((item) => (item.id === node.id ? { ...item, type: CanvasNodeType.Text, metadata: { ...item.metadata, content: answer, prompt, status: NODE_STATUS_SUCCESS } } : item)));
                    return;
                }
                if (node.type === CanvasNodeType.Video) {
                    await completeVideoNodeTask(
                        node.id,
                        generationConfig,
                        prompt,
                        retryImages,
                        controller.signal,
                        {
                            size: generationConfig.size,
                            seconds: generationConfig.videoSeconds,
                            vquality: generationConfig.vquality,
                            generateAudio: generationConfig.videoGenerateAudio,
                            watermark: generationConfig.videoWatermark,
                            videoMode: generationConfig.videoMode,
                        },
                        context?.referenceVideos || [],
                        context?.referenceAudios || [],
                    );
                    return;
                }
                if (node.type === CanvasNodeType.Audio) {
                    const audio = await storeGeneratedAudio(
                        await requestAudioGeneration(generationConfig, prompt, {
                            signal: controller.signal,
                            onRun: (runId) => setNodes((prev) => prev.map((item) => (item.id === node.id ? { ...item, metadata: { ...item.metadata, audioRunId: runId } } : item))),
                            onProgress: (run) => setRunStepProgress(node.id, run.progress),
                        }),
                        generationConfig.audioFormat,
                    );
                    setNodes((prev) =>
                        prev.map((item) => (item.id === node.id ? { ...item, metadata: { ...item.metadata, ...audioMetadata(audio), prompt, ...buildAudioGenerationMetadata(generationConfig), audioRunId: undefined, stepProgress: undefined } } : item)),
                    );
                    return;
                }

                const image = useReferenceImages
                    ? await requestEdit(generationConfig, prompt, retryImages, { signal: controller.signal }).then((items) => items[0])
                    : await requestGeneration(generationConfig, prompt, { signal: controller.signal }).then((items) => items[0]);
                const uploadedImage = await uploadImage(image.dataUrl, { signal: controller.signal });
                const imageConfig = NODE_DEFAULT_SIZE[CanvasNodeType.Image];
                const retryImage: CanvasNodeImage = {
                    id: imageId || node.metadata?.primaryImageId || nanoid(),
                    status: NODE_STATUS_SUCCESS,
                    content: uploadedImage.url,
                    storageKey: uploadedImage.storageKey,
                    naturalWidth: uploadedImage.width,
                    naturalHeight: uploadedImage.height,
                    bytes: uploadedImage.bytes,
                    mimeType: uploadedImage.mimeType,
                };
                const generationMetadata = savedImageMetadata?.generationType
                    ? {
                          generationType: savedImageMetadata.generationType,
                          model: generationConfig.model,
                          size: generationConfig.size,
                          quality: generationConfig.quality,
                          ...(generationConfig.background ? { background: generationConfig.background } : {}),
                          count: savedImageMetadata.count || 1,
                          references: savedImageMetadata.references,
                      }
                    : buildImageGenerationMetadata(useReferenceImages ? "edit" : "generation", generationConfig, 1, retryImages);
                setNodes((prev) =>
                    prev.map((item) => {
                        if (item.id !== node.id) return item;
                        const makePrimary = !imageId || !item.metadata?.content;
                        const edge = imageId ? Math.max(item.width, item.height) : 0;
                        const imageSize =
                            imageId && item.metadata?.freeResize
                                ? { width: item.width, height: item.height }
                                : imageId
                                  ? fitNodeSize(uploadedImage.width, uploadedImage.height, edge, edge)
                                  : fitNodeSize(uploadedImage.width, uploadedImage.height, imageConfig.width, imageConfig.height);
                        return {
                            ...item,
                            type: CanvasNodeType.Image,
                            ...(makePrimary
                                ? { width: imageSize.width, height: imageSize.height, ...(imageId ? { position: { x: item.position.x + item.width / 2 - imageSize.width / 2, y: item.position.y + item.height / 2 - imageSize.height / 2 } } : {}) }
                                : {}),
                            metadata: {
                                ...item.metadata,
                                ...(makePrimary ? imageMetadata(uploadedImage) : { status: NODE_STATUS_SUCCESS }),
                                images: item.metadata?.images?.map((current) => (current.id === retryImage.id ? retryImage : current)),
                                primaryImageId: makePrimary ? retryImage.id : item.metadata?.primaryImageId,
                                prompt,
                                ...generationMetadata,
                            },
                        };
                    }),
                );
            } catch (error) {
                if (isGenerationCanceled(error)) return;
                const errorDetails = error instanceof Error ? error.message : t("canvas.runtime.generationFailed");
                toast.error(errorDetails);
                setNodes((prev) =>
                    prev.map((item) =>
                        item.id === node.id
                            ? {
                                  ...item,
                                  metadata: {
                                      ...item.metadata,
                                      status: item.metadata?.content ? NODE_STATUS_SUCCESS : NODE_STATUS_ERROR,
                                      errorDetails: item.metadata?.content ? undefined : errorDetails,
                                      images: item.metadata?.images?.map((image) => (image.id === imageId ? { ...image, status: NODE_STATUS_ERROR, errorDetails } : image)),
                                      ...(isVideoTaskFailed(error) && item.type === CanvasNodeType.Video ? { videoTaskId: undefined } : {}),
                                  },
                              }
                            : item,
                    ),
                );
            } finally {
                finishGenerationRequest(node.id, controller);
                setRunningNodeId(null);
            }
        },
        [completeVideoNodeTask, effectiveConfig, finishGenerationRequest, isAiConfigReady, openConfigDialog, pollVideoNodeTask, setRunStepProgress, startGenerationRequest, t],
    );

    /**
     * Resume the run an audio node was waiting on when the page went away.
     *
     * This is the reason audio was the first node migrated: the gateway keeps the run, so the canvas
     * only has to reattach to it. Nothing is re-submitted, and the same step ring is driven from the
     * run's own progress.
     */
    const pollAudioRun = useCallback(
        async (node: CanvasNodeData, silent = false) => {
            const runId = node.metadata?.audioRunId;
            if (!runId || node.metadata?.content || audioPollIdsRef.current.has(node.id)) return;
            audioPollIdsRef.current.add(node.id);
            let controller: AbortController | undefined;
            try {
                const generationConfig = buildGenerationConfig(effectiveConfig, node, "audio");
                if (!isAiConfigReady(generationConfig, generationConfig.model)) {
                    if (silent) {
                        setNodes((prev) => prev.map((item) => (item.id === node.id ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, errorDetails: t("workbench.configFirst") } } : item)));
                        return;
                    }
                    openConfigDialog(true);
                    return;
                }
                setRunningNodeId(node.id);
                setNodes((prev) => prev.map((item) => (item.id === node.id ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_LOADING, errorDetails: undefined } } : item)));
                controller = startGenerationRequest(node.id, node.id, node.id);
                const finished = await waitForRun(generationConfig, runId, {
                    signal: controller.signal,
                    onProgress: (run) => setRunStepProgress(node.id, run.progress),
                    // Watching a run must not kill it: the page leaving is exactly the case this exists for.
                    cancelOnAbort: false,
                });
                const audio = await storeGeneratedAudio(await downloadRunOutput(generationConfig, finished, 0, { signal: controller.signal }), generationConfig.audioFormat);
                setNodes((prev) =>
                    prev.map((item) =>
                        item.id === node.id ? { ...item, metadata: { ...item.metadata, ...audioMetadata(audio), prompt: item.metadata?.prompt, ...buildAudioGenerationMetadata(generationConfig), audioRunId: undefined, stepProgress: undefined } } : item,
                    ),
                );
            } catch (error) {
                if (isGenerationCanceled(error)) return;
                const errorDetails = error instanceof Error ? error.message : t("canvas.runtime.generationFailed");
                if (!silent) toast.error(errorDetails);
                setNodes((prev) => prev.map((item) => (item.id === node.id ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, errorDetails, audioRunId: undefined, stepProgress: undefined } } : item)));
            } finally {
                if (controller) finishGenerationRequest(node.id, controller);
                setRunningNodeId((current) => (current === node.id ? null : current));
                audioPollIdsRef.current.delete(node.id);
            }
        },
        [effectiveConfig, finishGenerationRequest, isAiConfigReady, openConfigDialog, setNodes, setRunStepProgress, setRunningNodeId, startGenerationRequest, t],
    );

    /**
     * Resume the batch run an image node was waiting on when the page went away.
     *
     * The placeholder slots were written before submission, so this is the same fill the live request
     * would have done — with the artifacts read from a run that kept going.
     */
    const pollImageRun = useCallback(
        async (node: CanvasNodeData, silent = false) => {
            const runId = node.metadata?.imageRunId;
            const slotIds = (node.metadata?.images || []).map((image) => image.id).filter(Boolean);
            if (!runId || !slotIds.length || node.metadata?.content || imagePollIdsRef.current.has(node.id)) return;
            imagePollIdsRef.current.add(node.id);
            let controller: AbortController | undefined;
            try {
                const generationConfig = buildGenerationConfig(effectiveConfig, node, "image");
                if (!isAiConfigReady(generationConfig, generationConfig.model)) {
                    if (silent) {
                        setNodes((prev) => prev.map((item) => (item.id === node.id ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, errorDetails: t("workbench.configFirst") } } : item)));
                        return;
                    }
                    openConfigDialog(true);
                    return;
                }
                const requestConfig = resolveModelRequestConfig(generationConfig, generationConfig.model);
                setRunningNodeId(node.id);
                setNodes((prev) => prev.map((item) => (item.id === node.id ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_LOADING, errorDetails: undefined } } : item)));
                controller = startGenerationRequest(node.id, node.id, node.id, undefined, slotIds.length);
                const finished = await waitForRun(requestConfig, runId, {
                    signal: controller.signal,
                    onProgress: (run) => setRunStepProgress(node.id, run.progress),
                    // Watching a run must not kill it: the page leaving is exactly the case this exists for.
                    cancelOnAbort: false,
                });
                const files = await downloadRunOutputs(requestConfig, finished, { signal: controller.signal });
                let filled = 0;
                for (const [index, slotId] of slotIds.entries()) {
                    const file = files[index];
                    if (!file) break;
                    const dataUrl = await readFileAsDataUrl(new File([file.blob], `image-${index + 1}.png`, { type: file.mime || file.blob.type || "image/png" }));
                    await fillImageSlot(node.id, slotId, dataUrl, controller.signal);
                    filled += 1;
                }
                for (const slotId of slotIds.slice(filled)) failImageSlotOn(node.id, slotId, t("canvas.runtime.generationFailed"));
                if (!filled) throw new Error(t("canvas.runtime.allFailed"));
                setNodes((prev) => prev.map((item) => (item.id === node.id ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_SUCCESS, errorDetails: undefined, imageRunId: undefined, stepProgress: undefined } } : item)));
            } catch (error) {
                if (isGenerationCanceled(error)) return;
                const errorDetails = error instanceof Error ? error.message : t("canvas.runtime.generationFailed");
                if (!silent) toast.error(errorDetails);
                setNodes((prev) => prev.map((item) => (item.id === node.id ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, errorDetails, imageRunId: undefined, stepProgress: undefined } } : item)));
            } finally {
                if (controller) finishGenerationRequest(node.id, controller);
                setRunningNodeId((current) => (current === node.id ? null : current));
                imagePollIdsRef.current.delete(node.id);
            }
        },
        [effectiveConfig, failImageSlotOn, fillImageSlot, finishGenerationRequest, isAiConfigReady, openConfigDialog, setNodes, setRunStepProgress, setRunningNodeId, startGenerationRequest, t],
    );

    return { runImageGeneration, runVideoGeneration, runAudioGeneration, runTextGeneration, retryNode, pollAudioRun, pollImageRun };
}
