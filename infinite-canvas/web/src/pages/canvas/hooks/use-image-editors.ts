import { useCallback } from "react";
import { useTranslation } from "react-i18next";
import { nanoid } from "nanoid";

import { toast } from "@/components/kit/toast";
import { requestEdit, requestGeneration, requestImageAngle, requestUpscale } from "@/services/api/image";
import { uploadImage } from "@/services/image-storage";
import { channelModelOf, useConfigStore, useEffectiveConfig } from "@/stores/use-config-store";
import { cropDataUrl, splitDataUrl } from "@/lib/canvas/canvas-image-data";
import { fitNodeSize } from "@/lib/canvas/canvas-node-size";
import { NODE_DEFAULT_SIZE } from "@/constant/canvas";
import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";
import type { CanvasImageCropRect } from "@/components/canvas/canvas-node-crop-dialog";
import type { CanvasImageMaskEditPayload } from "@/components/canvas/canvas-node-mask-edit-dialog";
import type { CanvasOutpaintPayload } from "@/components/canvas/canvas-node-outpaint-dialog";
import type { CanvasMossLayoutPayload } from "@/components/canvas/canvas-node-moss-layout-dialog";
import type { CanvasImageSplitParams } from "@/components/canvas/canvas-node-split-dialog";
import type { CanvasSuperResolveParams } from "@/components/canvas/canvas-node-super-resolve-dialog";
import type { CanvasImageAngleParams } from "@/components/canvas/canvas-node-angle-dialog";
import { buildImageGenerationMetadata, imageMetadata } from "@/lib/canvas/canvas-node-factory";
import { buildAngleLabel, buildAnglePrompt, buildGenerationConfig, isGenerationCanceled } from "@/lib/canvas/canvas-generation-helpers";
import { imageReferenceLabel } from "@/lib/image-reference-prompt";
import { NODE_STATUS_ERROR, NODE_STATUS_LOADING, NODE_STATUS_SUCCESS } from "@/lib/canvas/canvas-node-status";
import type { NodeEditorDeps } from "@/pages/canvas/hooks/node-editor-deps";

/**
 * The editors that make a new image node out of an existing one: crop, split, mask, outpaint,
 * MOSS layout, upscale and multi-angle. Each one appends children, wires connections, opens the
 * composer on the result and (where it generates) drives one generation request.
 */
export function useImageEditors(deps: NodeEditorDeps) {
    const { setNodes, setConnections, setSelectedNodeIds, setSelectedConnectionId, setDialogNodeId, setRunningNodeId, closeNodeTool, startGenerationRequest, finishGenerationRequest } = deps;
    const { t } = useTranslation();
    const effectiveConfig = useEffectiveConfig();
    const isAiConfigReady = useConfigStore((state) => state.isAiConfigReady);
    const openConfigDialog = useConfigStore((state) => state.openConfigDialog);

    const cropImageNode = useCallback(async (node: CanvasNodeData, crop: CanvasImageCropRect) => {
        if (!node.metadata?.content) return;
        const cropped = await cropDataUrl(node.metadata.content, crop);
        const image = await uploadImage(cropped);
        const width = Math.min(node.width, Math.max(220, image.width));
        const childId = nanoid();
        const child: CanvasNodeData = {
            id: childId,
            type: CanvasNodeType.Image,
            title: t("canvas.runtime.cropNodeTitle"),
            titleKey: "canvas.runtime.cropNodeTitle",
            position: { x: node.position.x + node.width + 96, y: node.position.y },
            width,
            height: width * (image.height / image.width),
            metadata: {
                ...imageMetadata(image),
                prompt: node.metadata?.prompt,
            },
        };
        setNodes((prev) => [...prev, child]);
        setConnections((prev) => [...prev, { id: nanoid(), fromNodeId: node.id, toNodeId: childId }]);
        setSelectedNodeIds(new Set([childId]));
        setDialogNodeId(childId);
        closeNodeTool();
    }, []);

    const splitImageNode = useCallback(
        async (node: CanvasNodeData, params: CanvasImageSplitParams) => {
            if (!node.metadata?.content) return;
            closeNodeTool();
            const pieces = await splitDataUrl(node.metadata.content, params);
            const gap = 16;
            const cellWidth = node.width / params.columns;
            const cellHeight = node.height / params.rows;
            const startX = node.position.x + node.width + 96;
            const startY = node.position.y;
            const childNodes = await Promise.all(
                pieces.map(async (piece) => {
                    const image = await uploadImage(piece.dataUrl);
                    const id = nanoid();
                    return {
                        id,
                        type: CanvasNodeType.Image,
                        title: t("canvas.runtime.splitTitle", { name: node.title || t("assets.kinds.image"), row: piece.row + 1, column: piece.column + 1 }),
                        titleKey: "canvas.runtime.splitTitle",
                        titleParams: { name: node.title || t("assets.kinds.image"), row: piece.row + 1, column: piece.column + 1 },
                        position: { x: startX + piece.column * (cellWidth + gap), y: startY + piece.row * (cellHeight + gap) },
                        width: cellWidth,
                        height: cellHeight,
                        metadata: {
                            ...imageMetadata(image),
                            prompt: node.metadata?.prompt,
                        },
                    } satisfies CanvasNodeData;
                }),
            );
            setNodes((prev) => [...prev, ...childNodes]);
            setConnections((prev) => [...prev, ...childNodes.map((child) => ({ id: nanoid(), fromNodeId: node.id, toNodeId: child.id }))]);
            setSelectedNodeIds(new Set(childNodes.map((child) => child.id)));
            setSelectedConnectionId(null);
            setDialogNodeId(null);
            toast.success(t("canvas.runtime.splitSuccess", { count: childNodes.length }));
        },
        [t],
    );

    const maskEditImageNode = useCallback(
        async (node: CanvasNodeData, payload: CanvasImageMaskEditPayload) => {
            if (!node.metadata?.content) return;
            const isQwenImage = channelModelOf(effectiveConfig, buildGenerationConfig(effectiveConfig, node, "image").model)?.model.workflowId === "image-qwen21";
            const generationConfig = {
                ...buildGenerationConfig(effectiveConfig, node, "image"),
                count: "1",
                size: node.metadata?.size || "auto",
                ...(isQwenImage ? { imageMode: "inpaint" } : {}),
            };
            if (payload.generate && !isAiConfigReady(generationConfig, generationConfig.model)) {
                openConfigDialog(true);
                return;
            }
            const userPrompt = payload.prompt.trim();
            const prompt = isQwenImage ? userPrompt : t("canvas.runtime.maskPrompt", { source: imageReferenceLabel(0), mask: imageReferenceLabel(1), prompt: userPrompt });
            closeNodeTool();
            const maskImage = await uploadImage(payload.maskDataUrl);
            const generationMask = isQwenImage ? await uploadImage(payload.binaryMaskDataUrl) : null;
            const maskNodeId = nanoid();
            const childId = nanoid();
            const source = { id: node.id, name: `${node.title || node.id}.png`, type: node.metadata.mimeType || "image/png", dataUrl: node.metadata.content, storageKey: node.metadata.storageKey };
            const maskSource = { id: maskNodeId, name: "mask.png", type: maskImage.mimeType || "image/png", dataUrl: maskImage.url, storageKey: maskImage.storageKey };
            const references = [source, generationMask ? { ...maskSource, dataUrl: generationMask.url, storageKey: generationMask.storageKey } : maskSource];
            const generationMetadata = buildImageGenerationMetadata("edit", generationConfig, 1, references);
            const childMetadata = payload.generate ? { prompt, status: NODE_STATUS_LOADING, ...generationMetadata } : { prompt };
            setNodes((prev) => [
                ...prev,
                {
                    id: maskNodeId,
                    type: CanvasNodeType.Image,
                    title: t("canvas.runtime.maskNodeTitle"),
                    titleKey: "canvas.runtime.maskNodeTitle",
                    position: { x: node.position.x, y: node.position.y + node.height + 96 },
                    width: node.width,
                    height: node.height,
                    metadata: imageMetadata(maskImage),
                },
                {
                    id: childId,
                    type: CanvasNodeType.Image,
                    title: userPrompt.slice(0, 32) || t("canvas.runtime.maskResult"),
                    // A prompt the user wrote is data; only the fallback is a translation.
                    ...(userPrompt.slice(0, 32) ? {} : { titleKey: "canvas.runtime.maskResult" }),
                    position: { x: node.position.x + node.width + 96, y: node.position.y },
                    width: node.width,
                    height: node.height,
                    metadata: childMetadata,
                },
            ]);
            setConnections((prev) => [...prev, { id: nanoid(), fromNodeId: node.id, toNodeId: childId }, { id: nanoid(), fromNodeId: maskNodeId, toNodeId: childId }]);
            setSelectedNodeIds(new Set([childId]));
            setSelectedConnectionId(null);
            setDialogNodeId(childId);
            if (!payload.generate) return;
            setRunningNodeId(childId);
            const controller = startGenerationRequest(childId, node.id, childId);
            try {
                const image = await requestEdit(generationConfig, prompt, references, { signal: controller.signal }).then((items) => items[0]);
                const uploaded = await uploadImage(image.dataUrl, { signal: controller.signal });
                const size = fitNodeSize(uploaded.width, uploaded.height, node.width, node.height);
                setNodes((prev) => prev.map((item) => (item.id === childId ? { ...item, width: size.width, height: size.height, metadata: { ...item.metadata, ...imageMetadata(uploaded), prompt, ...generationMetadata } } : item)));
            } catch (error) {
                if (isGenerationCanceled(error)) return;
                const errorDetails = error instanceof Error ? error.message : t("canvas.runtime.maskFailed");
                toast.error(errorDetails);
                setNodes((prev) => prev.map((item) => (item.id === childId ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, errorDetails } } : item)));
            } finally {
                finishGenerationRequest(childId, controller);
                setRunningNodeId(null);
            }
        },
        [effectiveConfig, finishGenerationRequest, isAiConfigReady, openConfigDialog, startGenerationRequest, t],
    );

    const outpaintImageNode = useCallback(
        async (node: CanvasNodeData, payload: CanvasOutpaintPayload) => {
            if (!node.metadata?.content) return;
            const generationConfig = {
                ...buildGenerationConfig(effectiveConfig, node, "image"),
                count: "1",
                size: "auto",
                imageMode: "outpaint",
                outpaint: { left: payload.left, top: payload.top, right: payload.right, bottom: payload.bottom, width: payload.width, height: payload.height },
            };
            if (!isAiConfigReady(generationConfig, generationConfig.model)) {
                openConfigDialog(true);
                return;
            }
            closeNodeTool();
            const childId = nanoid();
            const source = { id: node.id, name: `${node.title || node.id}.png`, type: node.metadata.mimeType || "image/png", dataUrl: node.metadata.content, storageKey: node.metadata.storageKey };
            const references = [source];
            const generationMetadata = buildImageGenerationMetadata("edit", generationConfig, 1, references);
            setNodes((prev) => [
                ...prev,
                {
                    id: childId,
                    type: CanvasNodeType.Image,
                    title: payload.prompt.slice(0, 32) || t("canvas.outpaint.resultTitle"),
                    ...(payload.prompt.slice(0, 32) ? {} : { titleKey: "canvas.outpaint.resultTitle" }),
                    position: { x: node.position.x + node.width + 96, y: node.position.y },
                    width: node.width,
                    height: node.height,
                    metadata: { prompt: payload.prompt, status: NODE_STATUS_LOADING, ...generationMetadata },
                },
            ]);
            setConnections((prev) => [...prev, { id: nanoid(), fromNodeId: node.id, toNodeId: childId }]);
            setSelectedNodeIds(new Set([childId]));
            setSelectedConnectionId(null);
            setDialogNodeId(childId);
            setRunningNodeId(childId);
            const controller = startGenerationRequest(childId, node.id, childId);
            try {
                const image = await requestEdit(generationConfig, payload.prompt, references, { signal: controller.signal }).then((items) => items[0]);
                const uploaded = await uploadImage(image.dataUrl, { signal: controller.signal });
                const size = fitNodeSize(uploaded.width, uploaded.height, node.width, node.height);
                setNodes((prev) => prev.map((item) => (item.id === childId ? { ...item, width: size.width, height: size.height, metadata: { ...item.metadata, ...imageMetadata(uploaded), prompt: payload.prompt, ...generationMetadata } } : item)));
            } catch (error) {
                if (isGenerationCanceled(error)) return;
                const errorDetails = error instanceof Error ? error.message : t("canvas.outpaint.failed");
                toast.error(errorDetails);
                setNodes((prev) => prev.map((item) => (item.id === childId ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, errorDetails } } : item)));
            } finally {
                finishGenerationRequest(childId, controller);
                setRunningNodeId(null);
            }
        },
        [effectiveConfig, finishGenerationRequest, isAiConfigReady, openConfigDialog, startGenerationRequest],
    );

    const mossLayoutImageNode = useCallback(
        async (node: CanvasNodeData, payload: CanvasMossLayoutPayload) => {
            const generationConfig = {
                ...buildGenerationConfig(effectiveConfig, node, "image"),
                count: "1",
                size: `${payload.width || 1024}x${payload.height || 1024}`,
                imageMode: "layout",
                layout: payload,
            };
            if (!isAiConfigReady(generationConfig, generationConfig.model)) {
                openConfigDialog(true);
                return;
            }
            closeNodeTool();
            setNodes((prev) => prev.map((item) => (item.id === node.id ? { ...item, metadata: { ...item.metadata, imageMode: "layout", layout: payload } } : item)));
            const childId = nanoid();
            const generationMetadata = buildImageGenerationMetadata("generation", generationConfig, 1, []);
            setNodes((prev) => [
                ...prev,
                {
                    id: childId,
                    type: CanvasNodeType.Image,
                    title: payload.summary.slice(0, 32) || t("canvas.mossLayout.resultTitle"),
                    ...(payload.summary.slice(0, 32) ? {} : { titleKey: "canvas.mossLayout.resultTitle" }),
                    position: { x: node.position.x + node.width + 96, y: node.position.y },
                    width: node.width,
                    height: node.height,
                    metadata: { prompt: payload.summary, status: NODE_STATUS_LOADING, layout: payload, ...generationMetadata },
                },
            ]);
            setSelectedNodeIds(new Set([childId]));
            setSelectedConnectionId(null);
            setDialogNodeId(childId);
            setRunningNodeId(childId);
            const controller = startGenerationRequest(childId, node.id, childId);
            try {
                const image = await requestGeneration(generationConfig, payload.summary, { signal: controller.signal }).then((items) => items[0]);
                if (!image) throw new Error(t("canvas.mossLayout.noImage"));
                const uploaded = await uploadImage(image.dataUrl, { signal: controller.signal });
                const size = fitNodeSize(uploaded.width, uploaded.height, node.width, node.height);
                setNodes((prev) =>
                    prev.map((item) => (item.id === childId ? { ...item, width: size.width, height: size.height, metadata: { ...item.metadata, ...imageMetadata(uploaded), prompt: payload.summary, layout: payload, ...generationMetadata } } : item)),
                );
            } catch (error) {
                if (isGenerationCanceled(error)) return;
                const errorDetails = error instanceof Error ? error.message : t("canvas.mossLayout.failed");
                toast.error(errorDetails);
                setNodes((prev) => prev.map((item) => (item.id === childId ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, errorDetails } } : item)));
            } finally {
                finishGenerationRequest(childId, controller);
                setRunningNodeId(null);
            }
        },
        [effectiveConfig, finishGenerationRequest, isAiConfigReady, openConfigDialog, startGenerationRequest],
    );

    const superResolveImageNode = useCallback(
        async (node: CanvasNodeData, params: CanvasSuperResolveParams, titleKey: string) => {
            if (!node.metadata?.content) return;
            const childId = nanoid();
            const imageConfig = NODE_DEFAULT_SIZE[CanvasNodeType.Image];
            setRunningNodeId(childId);
            setNodes((prev) => [
                ...prev,
                {
                    id: childId,
                    type: CanvasNodeType.Image,
                    title: t(titleKey),
                    titleKey,
                    position: { x: node.position.x + node.width + 96, y: node.position.y },
                    width: imageConfig.width,
                    height: imageConfig.height,
                    metadata: { prompt: node.metadata?.prompt, status: NODE_STATUS_LOADING },
                },
            ]);
            setConnections((prev) => [...prev, { id: nanoid(), fromNodeId: node.id, toNodeId: childId }]);
            setSelectedNodeIds(new Set([childId]));
            setDialogNodeId(childId);
            const controller = startGenerationRequest(childId, node.id, childId);
            try {
                const image = await requestUpscale(
                    effectiveConfig,
                    {
                        id: node.id,
                        name: `${node.title || node.id}.png`,
                        type: node.metadata.mimeType || "image/png",
                        dataUrl: node.metadata.content,
                        storageKey: node.metadata.storageKey,
                    },
                    { workflowId: params.workflowId, params: params.params, signal: controller.signal },
                ).then((items) => items[0]);
                const uploaded = await uploadImage(image.dataUrl, { signal: controller.signal });
                const size = fitNodeSize(uploaded.width, uploaded.height, imageConfig.width, imageConfig.height);
                setNodes((prev) => prev.map((item) => (item.id === childId ? { ...item, width: size.width, height: size.height, metadata: { ...item.metadata, ...imageMetadata(uploaded), status: NODE_STATUS_SUCCESS } } : item)));
            } catch (error) {
                if (isGenerationCanceled(error)) return;
                const errorDetails = error instanceof Error ? error.message : t("canvas.runtime.generationFailed");
                setNodes((prev) => prev.map((item) => (item.id === childId ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, errorDetails } } : item)));
            } finally {
                finishGenerationRequest(childId, controller);
                setRunningNodeId(null);
            }
        },
        [effectiveConfig, finishGenerationRequest, startGenerationRequest, t],
    );

    const runSuperResolve = useCallback((node: CanvasNodeData, params: CanvasSuperResolveParams) => superResolveImageNode(node, params, "canvas.superResolve.title"), [superResolveImageNode]);
    const runFastUpscale = useCallback((node: CanvasNodeData, params: CanvasSuperResolveParams) => superResolveImageNode(node, params, "canvas.superResolve.fastTitle"), [superResolveImageNode]);
    const generateAngleNode = useCallback(
        async (node: CanvasNodeData, params: CanvasImageAngleParams) => {
            if (!node.metadata?.content) return;
            const generationConfig = { ...buildGenerationConfig(effectiveConfig, node, "image"), count: "1" };
            if (!isAiConfigReady(generationConfig, generationConfig.model)) {
                openConfigDialog(true);
                return;
            }
            const childId = nanoid();
            const imageConfig = NODE_DEFAULT_SIZE[CanvasNodeType.Image];
            const title = buildAngleLabel(params);
            const prompt = buildAnglePrompt(params);
            const sourceReference = {
                id: node.id,
                name: `${node.title || node.id}.png`,
                type: node.metadata.mimeType || "image/png",
                dataUrl: node.metadata.content,
                storageKey: node.metadata.storageKey,
            };
            const generationMetadata = buildImageGenerationMetadata("edit", generationConfig, 1, [sourceReference]);
            closeNodeTool();
            setRunningNodeId(childId);
            setNodes((prev) => [
                ...prev,
                {
                    id: childId,
                    type: CanvasNodeType.Image,
                    title,
                    position: { x: node.position.x + node.width + 96, y: node.position.y },
                    width: imageConfig.width,
                    height: imageConfig.height,
                    metadata: { prompt, status: NODE_STATUS_LOADING, ...generationMetadata },
                },
            ]);
            setConnections((prev) => [...prev, { id: nanoid(), fromNodeId: node.id, toNodeId: childId }]);
            setSelectedNodeIds(new Set([childId]));
            setDialogNodeId(childId);
            const controller = startGenerationRequest(childId, node.id, childId);
            try {
                const image = await requestImageAngle(generationConfig, prompt, params, [sourceReference], { signal: controller.signal }).then((items) => items[0]);
                const uploaded = await uploadImage(image.dataUrl, { signal: controller.signal });
                const size = fitNodeSize(uploaded.width, uploaded.height, imageConfig.width, imageConfig.height);
                setNodes((prev) => prev.map((item) => (item.id === childId ? { ...item, width: size.width, height: size.height, metadata: { ...item.metadata, ...imageMetadata(uploaded), prompt, ...generationMetadata } } : item)));
            } catch (error) {
                if (isGenerationCanceled(error)) return;
                const errorDetails = error instanceof Error ? error.message : t("canvas.runtime.generationFailed");
                setNodes((prev) => prev.map((item) => (item.id === childId ? { ...item, metadata: { ...item.metadata, status: NODE_STATUS_ERROR, errorDetails } } : item)));
            } finally {
                finishGenerationRequest(childId, controller);
                setRunningNodeId(null);
            }
        },
        [effectiveConfig, finishGenerationRequest, openConfigDialog, startGenerationRequest, t],
    );

    return { cropImageNode, splitImageNode, maskEditImageNode, outpaintImageNode, mossLayoutImageNode, runSuperResolve, runFastUpscale, generateAngleNode };
}
