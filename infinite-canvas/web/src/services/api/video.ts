import i18n from "@/i18n";
import { apiErrorMessage, isCancelled } from "@/services/api/http";
import { dataUrlToFile } from "@/lib/image-utils";
import { clampVideoSeconds, computeVideoSize, inferVideoRatio } from "@/lib/media-size";
import { VIDEO_INTERPOLATE_WORKFLOW } from "@/lib/canvas/tool-workflows";
import { getMediaBlob, resolveMediaUrl, uploadMediaFile, type UploadedFile } from "@/services/file-storage";
import { imageToDataUrl } from "@/services/image-storage";
import { cancelRun, createRun, downloadRunOutput, uploadMedia, waitForRun, RunFailedError, type Run } from "@/services/api/runs";
import { resolveModelRequestConfig, type AiConfig } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";
import type { ReferenceAudio, ReferenceVideo } from "@/types/media";

type RequestOptions = { signal?: AbortSignal; params?: Record<string, unknown> };
type VideoMediaOptions = RequestOptions & { videos?: ReferenceVideo[]; audios?: ReferenceAudio[] };
const apiText = (key: string, options?: Record<string, unknown>) => i18n.t(`apiErrors.${key}`, options);

type VideoGenerationResult = { blob?: Blob; url?: string; mimeType?: string };
/** A video task is a run: the id is the run id, and the file is read off the run. */
type VideoGenerationTask = { id: string; model: string };
type VideoUpscaleSource = { name: string; type?: string; url?: string; storageKey?: string };
type VideoInterpolateSource = VideoUpscaleSource;
type VideoInterpolateParams = { multiplier: 2 | 3 };
export type VideoPreview = { url: string; mime?: string; step?: number; total?: number };
type WaitOptions = RequestOptions & { onPreview?: (preview: VideoPreview | null) => void; onSteps?: (steps: { step: number; total: number }) => void };
/**
 * A video run renders for minutes, so it is polled slower than an image: the step count and the
 * preview frame are the only things a poll adds, and both change slowly.
 */
const VIDEO_RUN_INTERVAL_MS = 1500;

export async function requestVideoGeneration(config: AiConfig, prompt: string, references: ReferenceImage[] = [], options?: VideoMediaOptions): Promise<VideoGenerationResult> {
    return waitForVideoGenerationTask(config, await createVideoGenerationTask(config, prompt, references, options), options);
}

export async function waitForVideoGenerationTask(config: AiConfig, task: VideoGenerationTask, options?: WaitOptions): Promise<VideoGenerationResult> {
    return waitForVideoRun(config, task, options);
}

export function isVideoTaskFailed(error: unknown) {
    return error instanceof Error && error.name === "VideoTaskFailed";
}

function videoTaskFailed(message: string) {
    const error = new Error(message);
    error.name = "VideoTaskFailed";
    return error;
}

export async function createVideoGenerationTask(config: AiConfig, prompt: string, references: ReferenceImage[] = [], options?: VideoMediaOptions): Promise<VideoGenerationTask> {
    const selectedModel = (config.model || config.videoModel).trim();
    const requestConfig = resolveModelRequestConfig(config, selectedModel);
    assertVideoConfig(requestConfig, requestConfig.model);
    return createGatewayVideoTask(requestConfig, prompt, references, options);
}

/**
 * Interpolate a clip: the RIFE workflow with the clip in the `video` role.
 *
 * The output frame rate is derived inside the workflow (source rate × multiplier), so the only input
 * is the multiplier the caller asked for.
 */
export async function createVideoInterpolateTask(config: AiConfig, source: VideoInterpolateSource, params: VideoInterpolateParams, options?: RequestOptions): Promise<VideoGenerationTask> {
    const requestConfig = resolveModelRequestConfig(config, VIDEO_INTERPOLATE_WORKFLOW);
    const file = await referenceMediaToFile(source, "source.mp4", "invalidReferenceVideo", options);
    try {
        const uploaded = await uploadMedia(requestConfig, file, file.name, { signal: options?.signal });
        const run = await createRun(requestConfig, {
            workflow: VIDEO_INTERPOLATE_WORKFLOW,
            inputs: { multiplier: params.multiplier },
            media: { video: uploaded.id },
        });
        return { id: run.id, model: VIDEO_INTERPOLATE_WORKFLOW };
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("videoTaskCreateFailed")));
    }
}

export async function storeGeneratedVideo(result: VideoGenerationResult): Promise<UploadedFile> {
    if (result.blob) return uploadMediaFile(result.blob, "video");
    if (result.url) {
        try {
            return await uploadMediaFile(result.url, "video");
        } catch {
            return { url: result.url, storageKey: "", bytes: 0, mimeType: result.mimeType || "video/mp4" };
        }
    }
    throw new Error(apiText("noPlayableVideo"));
}

type GatewayVideoPlan = {
    mode: string;
    frameMode: string;
    images: ReferenceImage[];
    videos: ReferenceVideo[];
    audios: ReferenceAudio[];
};

function planGatewayVideo(config: AiConfig, references: ReferenceImage[], options?: VideoMediaOptions): GatewayVideoPlan {
    const mode = resolveVideoMode(config.videoMode, references.length);
    return {
        mode,
        frameMode: config.frameMode || (references.length >= 2 ? "both" : "first"),
        images: mode === "text" ? [] : references,
        videos: mode === "reference" ? options?.videos || [] : [],
        audios: mode === "reference" ? options?.audios || [] : [],
    };
}

/**
 * Submit one video run.
 *
 * Media goes in by role — `first_frame`/`last_frame` for a frame workflow, `reference` plus `video`
 * and `audio` for multi-reference — and the knobs are the workflow's own declared inputs. Nothing
 * here renames a field to a legacy multipart name, so a request that the gateway would reject is
 * rejected at submission rather than minutes into the render.
 */
async function createGatewayVideoTask(config: AiConfig, prompt: string, references: ReferenceImage[], options?: VideoMediaOptions): Promise<VideoGenerationTask> {
    const plan = planGatewayVideo(config, references, options);
    const inputs: Record<string, unknown> = { prompt, mode: plan.mode };
    const seconds = normalizeVideoSeconds(config.videoSeconds);
    if (Number(seconds) > 0) inputs.seconds = Number(seconds);
    const size = normalizeVideoSize(config.size, config.vquality);
    if (size) inputs.size = size;
    if (plan.mode === "frames") inputs.frameMode = plan.frameMode;
    Object.assign(inputs, { transition_step: config.videoSelfLiftMode === "quality" ? 5 : 6 }, options?.params || config.params || {});

    try {
        const media = await gatewayVideoMedia(config, plan, options);
        const run = await createRun(config, {
            workflow: config.model,
            inputs,
            ...(Object.keys(media).length ? { media } : {}),
        });
        return { id: run.id, model: config.model };
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("videoTaskCreateFailed")));
    }
}

/** Upload the planned media and return the role → upload id map a run request takes. */
async function gatewayVideoMedia(config: AiConfig, plan: GatewayVideoPlan, options?: VideoMediaOptions): Promise<Record<string, string | string[]>> {
    const signal = options?.signal;
    const media: Record<string, string | string[]> = {};
    const uploadImage = async (image: ReferenceImage, index: number) => {
        const dataUrl = await imageToDataUrl(image, { signal });
        if (!dataUrl) throw new Error(apiText("referenceImageReadFailed"));
        return (await uploadMedia(config, dataUrlToFile({ ...image, dataUrl }), image.name || `reference-${index + 1}.png`, { signal })).id;
    };
    const uploadClip = async (item: ReferenceVideo | ReferenceAudio, name: string, errorKey: "invalidReferenceVideo" | "invalidReferenceAudio") => {
        const file = await referenceMediaToFile(item, name, errorKey, options);
        return (await uploadMedia(config, file, file.name, { signal })).id;
    };

    if (plan.mode === "frames") {
        const first = plan.frameMode === "first" || plan.frameMode === "both" ? plan.images[0] : undefined;
        const last = plan.frameMode === "last" ? plan.images[0] : plan.frameMode === "both" ? plan.images[1] : undefined;
        if (first) media.first_frame = await uploadImage(first, 0);
        if (last) media.last_frame = await uploadImage(last, first ? 1 : 0);
        return media;
    }
    if (plan.mode !== "reference") return media;

    if (plan.images.length) media.reference = await Promise.all(plan.images.map((image, index) => uploadImage(image, index)));
    if (plan.videos.length) media.video = await Promise.all(plan.videos.map((video, index) => uploadClip(video, `reference-${index + 1}.mp4`, "invalidReferenceVideo")));
    if (plan.audios.length) media.audio = await Promise.all(plan.audios.map((audio, index) => uploadClip(audio, `reference-${index + 1}.mp3`, "invalidReferenceAudio")));
    return media;
}

/**
 * Watch the run that is producing this video.
 *
 * The run belongs to the gateway, so its id survives a reload: nothing is submitted again here, and
 * the step count and preview frames come from the run itself. Starting the request and watching it
 * are separate calls for exactly this reason.
 */
async function waitForVideoRun(config: AiConfig, task: VideoGenerationTask, options?: WaitOptions): Promise<VideoGenerationResult> {
    const requestConfig = resolveModelRequestConfig(config, task.model);
    try {
        const run: Run = await waitForRun(requestConfig, task.id, {
            signal: options?.signal,
            intervalMs: VIDEO_RUN_INTERVAL_MS,
            onProgress: (current) => {
                const preview = current.preview;
                if (preview?.url) {
                    options?.onPreview?.({
                        url: resolvePreviewUrl(requestConfig, preview.url),
                        mime: preview.mime,
                        step: current.progress?.step,
                        total: current.progress?.total,
                    });
                }
            },
            onSteps: options?.onSteps,
        });
        options?.onPreview?.(null);
        const blob = await downloadRunOutput(requestConfig, run, 0, { signal: options?.signal });
        return { blob };
    } catch (error) {
        // Only an explicit stop cancels the run; a reload or a node switch leaves it rendering so
        // the node can reattach to it later.
        if (options?.signal?.aborted && options.signal.reason === "user-stop") {
            await cancelRun(requestConfig, task.id).catch(() => undefined);
        }
        options?.onPreview?.(null);
        if (isCancelled(error) || options?.signal?.aborted) throw error;
        // The canvas clears a failed task id from this error, which is what stops it polling a run
        // that will never answer differently.
        if (error instanceof RunFailedError) throw videoTaskFailed(error.message);
        throw new Error(apiErrorMessage(error, apiText("videoTaskQueryFailed")));
    }
}

function resolvePreviewUrl(config: AiConfig, url: string) {
    try {
        return new URL(url, config.baseUrl).toString();
    } catch {
        return url;
    }
}
async function referenceMediaToFile(item: { name: string; type?: string; url?: string; storageKey?: string }, fallbackName: string, errorKey: "invalidReferenceVideo" | "invalidReferenceAudio", options?: RequestOptions) {
    let blob = item.storageKey ? await getMediaBlob(item.storageKey) : null;
    if (!blob) {
        const url = item.storageKey ? await resolveMediaUrl(item.storageKey, item.url || "") : item.url || "";
        if (!url) throw new Error(apiText(errorKey));
        try {
            blob = await (await fetch(url, { signal: options?.signal })).blob();
        } catch (error) {
            if (error instanceof DOMException && error.name === "AbortError") throw error;
            throw new Error(apiText(errorKey));
        }
    }
    if (!blob.size) throw new Error(apiText(errorKey));
    return new File([blob], item.name || fallbackName, { type: item.type || blob.type || "application/octet-stream" });
}

function assertVideoConfig(config: AiConfig, model: string) {
    if (!model) throw new Error(apiText("videoModelRequired"));
    if (!config.baseUrl.trim()) throw new Error(apiText("baseUrlRequired"));
    if (!config.apiKey.trim()) throw new Error(apiText("apiKeyRequired"));
}

function normalizeVideoSeconds(value: string) {
    return clampVideoSeconds(value);
}

function resolveVideoMode(mode: string | undefined, imageCount: number) {
    if (mode === "text" || mode === "reference" || mode === "frames") return mode;
    return imageCount > 2 ? "reference" : "frames";
}

function normalizeVideoSize(value: string, resolution?: string) {
    const selectedResolution = resolution || "720";
    if (!value || value === "auto") return computeVideoSize(selectedResolution, "16:9");
    // A stale pixel size must not override the selected 480/720/1080 tier.
    const ratio = inferVideoRatio(value);
    return computeVideoSize(selectedResolution, ratio === "auto" ? "16:9" : ratio);
}
