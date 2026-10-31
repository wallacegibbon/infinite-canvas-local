import i18n from "@/i18n";
import { isLocalGatewayUrl } from "@/constant/app-mode";
import { apiErrorMessage, httpBlob, httpJson, httpVoid, isCancelled, readApiErrorMessage } from "@/services/api/http";
import { dataUrlToFile, readFileAsDataUrl } from "@/lib/image-utils";
import { clampVideoSeconds, computeVideoSize, inferVideoRatio } from "@/lib/media-size";
import { VIDEO_INTERPOLATE_WORKFLOW, VIDEO_UPSCALE_WORKFLOW } from "@/lib/canvas/tool-workflows";
import { getMediaBlob, resolveMediaUrl, uploadMediaFile, type UploadedFile } from "@/services/file-storage";
import { imageToDataUrl } from "@/services/image-storage";
import { cancelRun, createRun, downloadRunOutput, uploadMedia, waitForRun, RunFailedError, type Run } from "@/services/api/runs";
import { boolConfig, buildApiUrl, modelOptionName, resolveModelRequestConfig, type AiConfig } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";
import type { ReferenceAudio, ReferenceVideo } from "@/types/media";

type VideoResponse = {
    id: string;
    status?: string;
    error?: { message?: string };
    url?: string;
    result_url?: string;
    video_url?: string;
    content?: { video_url?: string; url?: string } | null;
    preview?: { url?: string; mime?: string; step?: number; total?: number };
    steps?: { step?: number; total?: number };
};
type ApiVideoResponse = VideoResponse | { code?: number | string; data?: VideoResponse | null; msg?: string; message?: string; error?: { message?: string } };
type ApiEnvelope<T> = T | { code?: number | string; data?: T | null; msg?: string; message?: string; error?: { message?: string } };
type RequestOptions = { signal?: AbortSignal; params?: Record<string, unknown> };
type VideoMediaOptions = RequestOptions & { videos?: ReferenceVideo[]; audios?: ReferenceAudio[] };
const apiText = (key: string, options?: Record<string, unknown>) => i18n.t(`apiErrors.${key}`, options);

type VideoGenerationResult = { blob?: Blob; url?: string; mimeType?: string };
/**
 * A generation in flight. `provider` says how to watch it: `run` is the gateway's run protocol (the
 * id is a run id), the other two are the `/v1` task contracts those channels expose.
 */
type VideoGenerationTask = { id: string; provider: "openai" | "gemini" | "run"; model: string };
type VideoUpscaleSource = { name: string; type?: string; url?: string; storageKey?: string };
type VideoUpscaleParams = { scale: number; resolution: number; fps?: number; seed?: number; batchSize?: number; colorCorrection?: string };
type VideoInterpolateSource = VideoUpscaleSource;
type VideoInterpolateParams = { multiplier: 2 | 3 };
export type VideoPreview = { url: string; mime?: string; step?: number; total?: number };
type WaitOptions = RequestOptions & { onPreview?: (preview: VideoPreview | null) => void; onSteps?: (steps: { step: number; total: number }) => void };
type GeminiInlineData = { bytesBase64Encoded: string; mimeType: string };
type GeminiVideoOperation = {
    name?: string;
    done?: boolean;
    error?: { message?: string };
    response?: { generateVideoResponse?: { generatedSamples?: Array<{ video?: { uri?: string } }> } };
};
type VideoGenerationTaskState = { status: "pending"; preview?: VideoPreview; steps?: { step: number; total: number } } | { status: "completed"; result: VideoGenerationResult } | { status: "failed"; error: string };

/**
 * A video run renders for minutes, so it is polled slower than an image: the step count and the
 * preview frame are the only things a poll adds, and both change slowly.
 */
const VIDEO_RUN_INTERVAL_MS = 1500;

/**
 * Whether this channel is the local gateway, and therefore runs on the run protocol.
 *
 * The channel decides this, not the model's name: a workflow id has no fixed prefix (a base-model
 * variant is `img-zit-int8`), and the gateway is the only endpoint that answers with run ids.
 */
export function isGatewayVideoModel(config: Pick<AiConfig, "baseUrl">) {
    return isLocalGatewayUrl(config.baseUrl);
}

/**
 * The provider a node stored, in the vocabulary the task API uses.
 *
 * A resumed watch has to read the id the same way the original request did, so the provider travels
 * with the node instead of being guessed from the model when polling.
 */
export function videoTaskProviderOf(value: string | undefined): VideoGenerationTask["provider"] {
    return value === "gemini" || value === "run" ? value : "openai";
}

function aiApiUrl(config: AiConfig, path: string) {
    return buildApiUrl(config.baseUrl, path);
}

function aiHeaders(config: AiConfig, contentType?: string) {
    return {
        Authorization: `Bearer ${config.apiKey}`,
        ...(contentType ? { "Content-Type": contentType } : {}),
    };
}

export async function requestVideoGeneration(config: AiConfig, prompt: string, references: ReferenceImage[] = [], options?: VideoMediaOptions): Promise<VideoGenerationResult> {
    return waitForVideoGenerationTask(config, await createVideoGenerationTask(config, prompt, references, options), options);
}

export async function waitForVideoGenerationTask(config: AiConfig, task: VideoGenerationTask, options?: WaitOptions): Promise<VideoGenerationResult> {
    if (task.provider === "run") return waitForVideoRun(config, task, options);
    try {
        for (;;) {
            if (options?.signal?.aborted) throw new DOMException("Aborted", "AbortError");
            const state = await pollVideoGenerationTask(config, task, options);
            if (state.status === "completed") {
                options?.onPreview?.(null);
                return state.result;
            }
            if (state.status === "failed") {
                options?.onPreview?.(null);
                throw videoTaskFailed(state.error);
            }
            if (state.preview) options?.onPreview?.(state.preview);
            if (state.steps) options?.onSteps?.(state.steps);
            await delay(2500, options?.signal);
        }
    } catch (error) {
        // Only an explicit user stop cancels the remote task; unloading the page or switching nodes keeps it resumable.
        if (options?.signal?.aborted && options.signal.reason === "user-stop" && task.provider === "openai") {
            await cancelVideoGenerationTask(config, task).catch(() => undefined);
        }
        throw error;
    }
}

/** Cancel a queued/running gateway video task; safe to call after completion. */
async function cancelVideoGenerationTask(config: AiConfig, task: Pick<VideoGenerationTask, "id" | "model">) {
    const requestConfig = resolveModelRequestConfig(config, task.model);
    await httpVoid(aiApiUrl(requestConfig, `/videos/${task.id}/cancel`), {
        method: "POST",
        headers: aiHeaders(requestConfig, "application/json"),
        body: "{}",
        timeout: 5000,
    });
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
    if (requestConfig.apiFormat === "gemini") return createGeminiVideoTask(requestConfig, selectedModel, prompt, references, options);
    if (isGatewayVideoModel(requestConfig)) return createGatewayVideoTask(requestConfig, prompt, references, options);
    return createOpenAIVideoTask(requestConfig, selectedModel, prompt, references, options);
}

/**
 * Upscale a clip: one uploaded video, one run, one artifact.
 *
 * `scale` and `fps` are dialog values the graph derives itself (the target short edge and the source
 * frame rate), so only the inputs the workflow declares are sent — the gateway rejects the rest.
 */
export async function createVideoUpscaleTask(config: AiConfig, source: VideoUpscaleSource, params: VideoUpscaleParams, options?: RequestOptions): Promise<VideoGenerationTask> {
    const requestConfig = resolveModelRequestConfig(config, VIDEO_UPSCALE_WORKFLOW);
    const file = await referenceMediaToFile(source, "source.mp4", "invalidReferenceVideo", options);
    const inputs: Record<string, unknown> = { resolution: params.resolution };
    if (params.seed !== undefined) inputs.seed = params.seed;
    if (params.batchSize !== undefined) inputs.batch_size = params.batchSize;
    if (params.colorCorrection) inputs.color_correction = params.colorCorrection;
    try {
        const uploaded = await uploadMedia(requestConfig, file, file.name, { signal: options?.signal });
        const run = await createRun(requestConfig, { workflow: VIDEO_UPSCALE_WORKFLOW, inputs, media: { video: uploaded.id } });
        return { id: run.id, provider: "run", model: VIDEO_UPSCALE_WORKFLOW };
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("videoTaskCreateFailed")));
    }
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
        return { id: run.id, provider: "run", model: VIDEO_INTERPOLATE_WORKFLOW };
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("videoTaskCreateFailed")));
    }
}

async function pollVideoGenerationTask(config: AiConfig, task: VideoGenerationTask, options?: RequestOptions): Promise<VideoGenerationTaskState> {
    const requestConfig = resolveModelRequestConfig(config, task.model);
    assertVideoConfig(requestConfig, requestConfig.model);
    if (task.provider === "gemini") return pollGeminiVideoTask(requestConfig, task, options);
    return pollOpenAIVideoTask(requestConfig, task, options);
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

async function createOpenAIVideoTask(config: AiConfig, model: string, prompt: string, references: ReferenceImage[], options?: VideoMediaOptions): Promise<VideoGenerationTask> {
    const mode = resolveVideoMode(config.videoMode, references.length);
    const frameMode = config.frameMode || (references.length >= 2 ? "both" : "first");
    const selectedImages = references.slice(0, mode === "text" ? 0 : mode === "frames" ? 2 : 6);
    const selectedVideos = mode === "reference" ? (options?.videos || []).slice(0, 1) : [];
    const selectedAudios = mode === "reference" ? (options?.audios || []).slice(0, 2) : [];
    const images = await Promise.all(selectedImages.map(async (image) => dataUrlToFile({ ...image, dataUrl: await imageToDataUrl(image) })));
    const videos = await Promise.all(selectedVideos.map((video) => referenceMediaToFile(video, "ref.mp4", "invalidReferenceVideo", options)));
    const audios = await Promise.all(selectedAudios.map((audio) => referenceMediaToFile(audio, "ref.mp3", "invalidReferenceAudio", options)));
    const ratio = inferVideoRatio(config.size || "16:9");
    const body = new FormData();
    body.append("model", modelOptionName(model));
    body.append("prompt", prompt);
    body.append("seconds", normalizeVideoSeconds(config.videoSeconds));
    body.append("size", normalizeVideoSize(config.size, config.vquality) || "1280x720");
    if (ratio && ratio !== "auto") {
        body.append("ratio", ratio);
    }
    body.append("resolution_name", normalizeVideoResolution(config.vquality));
    body.append("generate_audio", String(boolConfig(config.videoGenerateAudio, true)));
    body.append("watermark", String(boolConfig(config.videoWatermark, false)));
    body.append("mode", mode);
    const requestParams = {
        ...(options?.params || config.params || {}),
        transition_step: config.videoSelfLiftMode === "quality" ? 5 : 6,
        ...(mode === "frames" ? { frameMode } : {}),
    };
    if (Object.keys(requestParams).length) {
        body.append("params", JSON.stringify(requestParams));
    }
    if (mode === "frames") {
        if ((frameMode === "first" || frameMode === "both") && images[0]) {
            body.append("first_frame", images[0], "first.png");
            body.append("images", images[0], "first.png");
        }
        if (frameMode === "last" || frameMode === "both") {
            const last = frameMode === "both" ? images[1] : images[0];
            if (last) {
                body.append("last_frame", last, "last.png");
                body.append("images", last, "last.png");
            }
        }
    } else if (mode === "reference") {
        images.forEach((file, idx) => {
            body.append("image[]", file, `ref_${idx + 1}.png`);
            body.append("images", file, `ref_${idx + 1}.png`);
        });
    }
    videos.forEach((file) => body.append("video[]", file));
    audios.forEach((file) => body.append("audio[]", file));
    try {
        const created = unwrapVideoResponse(await httpJson<ApiVideoResponse>(aiApiUrl(config, "/videos"), { method: "POST", headers: aiHeaders(config), body, signal: options?.signal }));
        if (!created.id) throw new Error(apiText("noVideoTaskId"));
        return { id: created.id, provider: "openai", model };
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("videoTaskCreateFailed")));
    }
}

async function pollOpenAIVideoTask(config: AiConfig, task: VideoGenerationTask, options?: RequestOptions): Promise<VideoGenerationTaskState> {
    try {
        const video = unwrapVideoResponse(await httpJson<ApiVideoResponse>(aiApiUrl(config, `/videos/${task.id}`), { headers: aiHeaders(config), signal: options?.signal }));
        const url = videoResultUrl(video);
        if (url) return { status: "completed", result: await videoResultFromUrl(url, options) };
        if (video.status === "completed") {
            const content = await httpBlob(aiApiUrl(config, `/videos/${task.id}/content`), { headers: aiHeaders(config), signal: options?.signal });
            await assertVideoBlob(content);
            return { status: "completed", result: { blob: content } };
        }
        if (video.status === "failed" || video.status === "cancelled") return { status: "failed", error: readApiErrorMessage(video.error?.message) || apiText("videoGenerationFailed") };
        const preview = video.preview?.url
            ? {
                  url: resolvePreviewUrl(config, video.preview.url),
                  mime: video.preview.mime,
                  step: video.preview.step,
                  total: video.preview.total,
              }
            : undefined;
        const totalSteps = Number(video.steps?.total) || 0;
        const steps = totalSteps > 0 ? { step: Math.min(totalSteps, Math.max(0, Number(video.steps?.step) || 0)), total: totalSteps } : undefined;
        return { status: "pending", preview, steps };
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("videoTaskQueryFailed")));
    }
}

/**
 * What one gateway video request carries, resolved before anything is uploaded.
 *
 * The mode decides which media a workflow even looks at (a text-to-video run takes none, a
 * first/last-frame run takes frames, a multi-reference run takes references), so it is settled once
 * and then used for both the uploads and the run's inputs.
 */
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
        return { id: run.id, provider: "run", model: config.model };
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
async function videoResultFromUrl(url: string, options?: RequestOptions): Promise<VideoGenerationResult> {
    try {
        const blob = await httpBlob(url, { signal: options?.signal });
        await assertVideoBlob(blob);
        return { blob };
    } catch (error) {
        if (isCancelled(error) || options?.signal?.aborted) throw error;
        return { url, mimeType: "video/mp4" };
    }
}

async function createGeminiVideoTask(config: AiConfig, model: string, prompt: string, references: ReferenceImage[], options?: VideoMediaOptions): Promise<VideoGenerationTask> {
    const images = await Promise.all(references.map((image) => imageToDataUrl(image)));
    const videos = await Promise.all((options?.videos || []).map((video) => referenceMediaToFile(video, "ref.mp4", "invalidReferenceVideo", options)));
    const audios = await Promise.all((options?.audios || []).map((audio) => referenceMediaToFile(audio, "ref.mp3", "invalidReferenceAudio", options)));
    const mode = resolveVideoMode(config.videoMode, images.length);
    const instance: Record<string, unknown> = { prompt };
    if (mode === "frames") {
        if (images[0]) instance.image = parseDataUrlInline(images[0]);
        if (images[1]) instance.lastFrame = parseDataUrlInline(images[1]);
    } else {
        instance.referenceImages = images.map((dataUrl) => ({ image: parseDataUrlInline(dataUrl), referenceType: "asset" }));
    }
    if (videos[0]) instance.video = await fileToGeminiInline(videos[0]);
    if (audios[0]) instance.audio = await fileToGeminiInline(audios[0]);
    try {
        const created = unwrapEnvelope(
            await httpJson<ApiEnvelope<GeminiVideoOperation>>(geminiVideoUrl(config, model, "predictLongRunning"), {
                method: "POST",
                body: JSON.stringify({
                    instances: [instance],
                    parameters: {
                        aspectRatio: videoAspectRatio(config.size),
                        durationSeconds: Number(normalizeVideoSeconds(config.videoSeconds)) || 8,
                        resolution: normalizeVideoResolution(config.vquality),
                        generateAudio: boolConfig(config.videoGenerateAudio, true),
                        addWatermark: boolConfig(config.videoWatermark, false),
                    },
                }),
                headers: geminiVideoHeaders(config),
                signal: options?.signal,
            }),
            apiText("noVideoTask"),
        );
        if (!created.name) throw new Error(apiText("noVideoTaskId"));
        return { id: created.name, provider: "gemini", model };
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("videoTaskCreateFailed")));
    }
}

async function pollGeminiVideoTask(config: AiConfig, task: VideoGenerationTask, options?: RequestOptions): Promise<VideoGenerationTaskState> {
    try {
        const state = unwrapEnvelope(await httpJson<ApiEnvelope<GeminiVideoOperation>>(geminiOperationUrl(config, task.id), { headers: geminiVideoHeaders(config), signal: options?.signal }), apiText("videoTaskQueryFailed"));
        if (state.error) return { status: "failed", error: readApiErrorMessage(state.error.message) || apiText("videoGenerationFailed") };
        if (!state.done) return { status: "pending" };
        const uri = state.response?.generateVideoResponse?.generatedSamples?.[0]?.video?.uri;
        if (!uri) return { status: "failed", error: apiText("noPlayableVideo") };
        const url = uri.includes("key=") ? uri : `${uri}${uri.includes("?") ? "&" : "?"}key=${config.apiKey}`;
        return { status: "completed", result: await videoResultFromUrl(url, options) };
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("videoTaskQueryFailed")));
    }
}

function assertVideoConfig(config: AiConfig, model: string) {
    if (!model) throw new Error(apiText("videoModelRequired"));
    if (!config.baseUrl.trim()) throw new Error(apiText("baseUrlRequired"));
    if (!config.apiKey.trim()) throw new Error(apiText("apiKeyRequired"));
}

function geminiVideoBaseUrl(config: Pick<AiConfig, "baseUrl">) {
    const normalizedBaseUrl = config.baseUrl.trim().replace(/\/+$/, "");
    const lowerBaseUrl = normalizedBaseUrl.toLowerCase();
    return lowerBaseUrl.endsWith("/v1") || lowerBaseUrl.endsWith("/v1beta") ? normalizedBaseUrl : `${normalizedBaseUrl}/v1beta`;
}

function geminiVideoUrl(config: Pick<AiConfig, "baseUrl">, model: string, action: string) {
    return `${geminiVideoBaseUrl(config)}/models/${encodeURIComponent(modelOptionName(model).replace(/^models\//, ""))}:${action}`;
}

function geminiOperationUrl(config: Pick<AiConfig, "baseUrl">, name: string) {
    return `${geminiVideoBaseUrl(config)}/${name.replace(/^\//, "")}`;
}

function geminiVideoHeaders(config: Pick<AiConfig, "apiKey">) {
    return { "x-goog-api-key": config.apiKey, "Content-Type": "application/json" };
}

function videoAspectRatio(size: string) {
    const ratio = inferVideoRatio(size);
    return ratio === "auto" ? "16:9" : ratio;
}

function parseDataUrlInline(dataUrl: string, fallbackType = "image/png"): GeminiInlineData {
    const match = dataUrl.match(/^data:([^;]+);base64,(.*)$/);
    return { bytesBase64Encoded: match?.[2] || "", mimeType: match?.[1] || fallbackType };
}

async function fileToGeminiInline(file: File): Promise<GeminiInlineData> {
    return parseDataUrlInline(await readFileAsDataUrl(file), file.type || "application/octet-stream");
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

function normalizeVideoResolution(value: string) {
    if (value === "low") return "480p";
    if (value === "auto" || value === "high" || value === "medium") return "720p";
    const resolution = value.replace(/p$/i, "") || "720";
    return `${resolution}p`;
}

function unwrapVideoResponse(payload: ApiVideoResponse) {
    return unwrapEnvelope(payload, apiText("noVideoTask"));
}

function unwrapEnvelope<T>(payload: ApiEnvelope<T>, emptyMessage: string): T {
    if (!payload) throw new Error(emptyMessage);
    if (typeof payload === "object" && "code" in payload && payload.code !== undefined) {
        if (payload.code !== 0 && payload.code !== "0") throw new Error(readApiErrorMessage(payload) || apiText("requestFailed"));
        if (!payload.data) throw new Error(emptyMessage);
        return payload.data;
    }
    return payload as T;
}

function videoResultUrl(payload: VideoResponse) {
    return [payload.video_url, payload.result_url, payload.url, payload.content?.video_url, payload.content?.url].find((url) => typeof url === "string" && (isPublicMediaUrl(url) || /\.mp4(\?|#|$)/i.test(url)));
}

async function assertVideoBlob(blob: Blob) {
    if (!blob.type.includes("json")) return;
    let payload: { code?: number; msg?: string; error?: { message?: string } };
    try {
        payload = JSON.parse(await blob.text()) as { code?: number; msg?: string; error?: { message?: string } };
    } catch {
        return;
    }
    if (typeof payload.code === "number" && payload.code !== 0) throw new Error(readApiErrorMessage(payload) || apiText("videoDownloadFailed"));
    if (payload.error?.message) throw new Error(readApiErrorMessage(payload.error.message) || payload.error.message);
}

function isPublicMediaUrl(value: string) {
    return /^https?:\/\//i.test(value || "");
}

function delay(ms: number, signal?: AbortSignal) {
    return new Promise<void>((resolve, reject) => {
        if (signal?.aborted) {
            reject(new DOMException("Aborted", "AbortError"));
            return;
        }
        const timer = setTimeout(resolve, ms);
        signal?.addEventListener(
            "abort",
            () => {
                clearTimeout(timer);
                reject(new DOMException("Aborted", "AbortError"));
            },
            { once: true },
        );
    });
}
