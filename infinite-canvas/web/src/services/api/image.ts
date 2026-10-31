import i18n from "@/i18n";
import { LOCAL_GATEWAY_BASE_URL, isLocalGatewayUrl } from "@/constant/app-mode";
import { buildApiUrl, channelModelOf, modelImageInputs, modelOptionName, resolveModelRequestConfig, type AiConfig, type ModelChannel } from "@/stores/use-config-store";
import { nanoid } from "nanoid";
import { dataUrlToFile, readFileAsDataUrl } from "@/lib/image-utils";
import { buildImageReferencePromptText } from "@/lib/image-reference-prompt";
import { IMAGE_ANGLE_WORKFLOW } from "@/lib/canvas/tool-workflows";
import { imageToDataUrl } from "@/services/image-storage";
import { imageSizePresets } from "@/lib/media-size";
import { apiErrorMessage, httpJson } from "@/services/api/http";
import { createRun, downloadRunOutputs, uploadMedia, waitForRun, type Run } from "@/services/api/runs";
import { requestWorkflows } from "@/services/api/workflows";
import { publishStepProgress } from "@/services/api/step-progress";
import type { ReferenceImage } from "@/types/image";

const apiText = (key: string, options?: Record<string, unknown>) => i18n.t(`apiErrors.${key}`, options);

export type AiTextMessage = {
    role: "system" | "user" | "assistant";
    content: string | Array<{ type: "text"; text: string } | { type: "image_url"; image_url: { url: string } }>;
};

type ResponseToolCall = {
    id: string;
    type: "function";
    function: { name: string; arguments: string };
    thoughtSignature?: string;
};

type ResponseInputMessage = AiTextMessage | { type: "function_call"; call_id: string; name: string; arguments: string; thoughtSignature?: string } | { role: "tool"; tool_call_id: string; content: string };

type ToolResponseResult = {
    content: string;
    toolCalls: ResponseToolCall[];
};

type ResponseMessageContent = AiTextMessage["content"] | string;
type ResponseInputContent = { type: "input_text"; text: string } | { type: "input_image"; image_url: string };
type ResponseInputItem = { role: "system" | "user" | "assistant"; content: string | ResponseInputContent[] } | { type: "function_call"; call_id: string; name: string; arguments: string } | { type: "function_call_output"; call_id: string; output: string };
type ResponseApiOutputItem = { type?: "message"; content?: Array<{ type?: string; text?: string }> } | { type?: "function_call"; id?: string; call_id?: string; name?: string; arguments?: string };
type ResponseApiPayload = {
    id?: string;
    output?: ResponseApiOutputItem[];
    output_text?: string;
    error?: { message?: string };
    code?: number;
    msg?: string;
};
type ResponseStreamState = { buffer: string; text: string; payload?: ResponseApiPayload; error?: string };

type ImageApiResponse = {
    data?: Array<Record<string, unknown>>;
    error?: { message?: string };
    code?: number;
    msg?: string;
};
type RequestOptions = {
    signal?: AbortSignal;
    params?: Record<string, unknown>;
    /** Reported as soon as the gateway accepts a run, so the node can resume it after a reload. */
    onRun?: (runId: string) => void;
    onProgress?: (run: Run) => void;
};

/**
 * Whether this channel is the local gateway, and therefore uses runs.
 *
 * It used to also require an `image-` prefix, on the theory that workflow ids follow the registry's
 * naming rule. They do not all: a base-model variant is `img-zit-int8`, and a run's id is the workflow
 * whatever it is called. The channel is the only thing that decides which contract a request speaks.
 */
export function isGatewayImageModel(config: Pick<AiConfig, "baseUrl">) {
    return isLocalGatewayUrl(config.baseUrl);
}

const DEFAULT_IMAGE_SHORT_SIDE = 1024;
const IMAGE_SIZE_STEP = 16;
const IMAGE_MIN_PIXELS = 655360;
const IMAGE_MAX_PIXELS = 8294400;
const IMAGE_MAX_EDGE = 3840;
const IMAGE_MAX_RATIO = 3;
const IMAGE_OUTPUT_FORMAT = "png";
// Matches the image-storage download timeout, so a hung endpoint does not leave the node stuck in "generating".
const IMAGE_REQUEST_TIMEOUT_MS = 10 * 60_000;

export function localGatewayRoot(baseUrl: string) {
    const url = String(baseUrl || "")
        .trim()
        .replace(/\/+$/, "");
    if (!isLocalGatewayUrl(url)) return "";
    try {
        return new URL(url).origin;
    } catch {
        return "";
    }
}

// Explicit cancellation against the local gateway: it does not rely on when the browser tears down the XHR.
/** Only "transparent" is forwarded; any other value (incl. empty) means keep the default opaque background. */
function normalizeBackground(background: string | undefined) {
    return background?.trim().toLowerCase() === "transparent" ? "transparent" : undefined;
}

/** Map a bare ratio such as "1:1" to an explicit pixel dimension like "1024x1024". */
function resolveSize(ratio: string): string {
    const parsedRatio = parseImageRatio(ratio);
    const preset = imageSizePresets["1k"][ratio];
    if (preset) return preset;
    const isLandscape = parsedRatio.width >= parsedRatio.height;
    const longRatio = isLandscape ? parsedRatio.width / parsedRatio.height : parsedRatio.height / parsedRatio.width;
    const shortSide = DEFAULT_IMAGE_SHORT_SIDE;
    const longSide = Math.round((shortSide * longRatio) / IMAGE_SIZE_STEP) * IMAGE_SIZE_STEP;
    const width = isLandscape ? longSide : shortSide;
    const height = isLandscape ? shortSide : longSide;
    validateImageSize(width, height);
    return `${width}x${height}`;
}

function parseRatioValue(value: string) {
    const parts = value.split(":");
    if (parts.length !== 2) throw new Error(apiText("invalidImageSizeFormat"));
    const w = Number(parts[0]);
    const h = Number(parts[1]);
    if (!Number.isFinite(w) || !Number.isFinite(h) || w <= 0 || h <= 0) throw new Error(apiText("positiveImageRatio"));
    return { width: w, height: h };
}

function parseImageRatio(value: string) {
    const ratio = parseRatioValue(value);
    if (Math.max(ratio.width, ratio.height) / Math.min(ratio.width, ratio.height) > IMAGE_MAX_RATIO) throw new Error(apiText("imageRatioLimit"));
    return ratio;
}

function parseImageDimensions(value: string) {
    const match = value.match(/^(\d+)x(\d+)$/i);
    if (!match) return null;
    return { width: Number(match[1]), height: Number(match[2]) };
}

function validateImageSize(width: number, height: number) {
    if (!Number.isInteger(width) || !Number.isInteger(height) || width <= 0 || height <= 0) throw new Error(apiText("positiveImageDimensions"));
    if (width % IMAGE_SIZE_STEP !== 0 || height % IMAGE_SIZE_STEP !== 0) throw new Error(apiText("imageDimensionStep"));
    if (Math.max(width, height) > IMAGE_MAX_EDGE) throw new Error(apiText("imageEdgeLimit"));
    if (Math.max(width, height) / Math.min(width, height) > IMAGE_MAX_RATIO) throw new Error(apiText("imageRatioLimit"));
    const pixels = width * height;
    if (pixels < IMAGE_MIN_PIXELS || pixels > IMAGE_MAX_PIXELS) throw new Error(apiText("imagePixelLimit"));
}

function alignImageDimensions(width: number, height: number) {
    return {
        width: Math.ceil(width / IMAGE_SIZE_STEP) * IMAGE_SIZE_STEP,
        height: Math.ceil(height / IMAGE_SIZE_STEP) * IMAGE_SIZE_STEP,
    };
}

function resolveRequestSize(size: string) {
    const value = size.trim();
    if (!value || value.toLowerCase() === "auto") return undefined;
    const dimensions = parseImageDimensions(value);
    if (dimensions) {
        const aligned = alignImageDimensions(dimensions.width, dimensions.height);
        validateImageSize(aligned.width, aligned.height);
        return `${aligned.width}x${aligned.height}`;
    }
    if (value.includes(":")) return resolveSize(value);
    throw new Error(apiText("invalidImageSizeFormat"));
}

function readStatusError(status: number | undefined, fallback: string) {
    if (status === 401 || status === 403) return apiText("authenticationFailed");
    if (status === 429) return apiText("rateLimited");
    if (status === 404) return apiText("notFound");
    if (status === 502) return apiText("badGateway");
    if (status === 503) return apiText("serviceBusy");
    return status ? apiText("httpFailed", { status }) : fallback;
}

function withSystemPrompt(config: AiConfig, prompt: string) {
    const systemPrompt = config.systemPrompt.trim();
    return systemPrompt ? `${systemPrompt}\n\n${prompt}` : prompt;
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

function withSystemMessage<T extends ResponseInputMessage>(config: AiConfig, messages: T[]): ResponseInputMessage[] {
    const systemPrompt = config.systemPrompt.trim();
    return systemPrompt ? [{ role: "system" as const, content: systemPrompt }, ...messages] : messages;
}

function toResponseInput(messages: ResponseInputMessage[]): ResponseInputItem[] {
    return messages.flatMap((message): ResponseInputItem[] => {
        if ("type" in message) return [message];
        if (message.role === "tool") return [{ type: "function_call_output", call_id: message.tool_call_id, output: message.content }];
        return [{ role: message.role, content: toResponseContent(message.content || "") }];
    });
}

function toResponseContent(content: ResponseMessageContent): string | ResponseInputContent[] {
    if (!Array.isArray(content)) return String(content || "");
    return content.map((item) => (item.type === "text" ? { type: "input_text" as const, text: item.text } : { type: "input_image" as const, image_url: item.image_url.url }));
}

function parseToolResponse(payload: ResponseApiPayload): ToolResponseResult {
    const output = payload.output || [];
    const content =
        payload.output_text ||
        output
            .flatMap((item) => (item.type === "message" ? item.content || [] : []))
            .map((item) => item.text || "")
            .join("");
    const toolCalls = output
        .filter((item): item is Extract<ResponseApiOutputItem, { type?: "function_call" }> => item.type === "function_call")
        .map((item) => ({
            id: item.call_id || item.id || "",
            type: "function" as const,
            function: { name: item.name || "", arguments: item.arguments || "{}" },
        }))
        .filter((item) => item.id && item.function.name);
    return { content, toolCalls };
}

function isRecord(value: unknown): value is Record<string, unknown> {
    return Boolean(value && typeof value === "object" && !Array.isArray(value));
}

function responseErrorMessage(value: unknown) {
    if (!isRecord(value)) return "";
    const error = isRecord(value.error) ? value.error : undefined;
    const response = isRecord(value.response) ? value.response : undefined;
    const responseError = response && isRecord(response.error) ? response.error : undefined;
    return stringValue(value.msg) || stringValue(error?.message) || stringValue(responseError?.message);
}

function stringValue(value: unknown) {
    return typeof value === "string" ? value : "";
}

function validateResponsePayload(payload: ResponseApiPayload) {
    if (typeof payload.code === "number" && payload.code !== 0) throw new Error(payload.msg || apiText("requestFailed"));
    if (payload.error?.message) throw new Error(payload.error.message);
}

async function readFetchError(response: Response, fallback: string) {
    const text = await response.text();
    if (!text) return readStatusError(response.status, fallback);
    try {
        return responseErrorMessage(JSON.parse(text)) || readStatusError(response.status, fallback);
    } catch {
        return text.slice(0, 300) || readStatusError(response.status, fallback);
    }
}

function consumeResponseStreamBlock(block: string, state: ResponseStreamState, onDelta?: (text: string) => void) {
    const data = block
        .split(/\r?\n/)
        .filter((line) => line.startsWith("data:"))
        .map((line) => line.slice(5).replace(/^ /, ""))
        .join("\n")
        .trim();
    if (!data || data === "[DONE]") return;
    const event = JSON.parse(data) as Record<string, unknown>;
    const type = stringValue(event.type);
    const errorMessage = responseErrorMessage(event);
    if (errorMessage) state.error = errorMessage;
    if (type === "response.output_text.delta" && typeof event.delta === "string") {
        state.text += event.delta;
        onDelta?.(state.text);
    }
    if (type === "response.output_text.done" && !state.text && typeof event.text === "string") {
        state.text = event.text;
        onDelta?.(state.text);
    }
    if (type === "response.completed" && isRecord(event.response)) {
        state.payload = event.response as ResponseApiPayload;
    } else if (Array.isArray(event.output)) {
        state.payload = event as ResponseApiPayload;
    }
}

function consumeResponseStreamText(state: ResponseStreamState, text: string, onDelta?: (text: string) => void, flush = false) {
    state.buffer += text;
    for (;;) {
        const match = state.buffer.match(/\r?\n\r?\n/);
        if (!match) break;
        const index = match.index ?? 0;
        consumeResponseStreamBlock(state.buffer.slice(0, index), state, onDelta);
        state.buffer = state.buffer.slice(index + match[0].length);
    }
    if (flush && state.buffer.trim()) {
        consumeResponseStreamBlock(state.buffer, state, onDelta);
        state.buffer = "";
    }
}

async function requestStreamingResponse(config: AiConfig, body: Record<string, unknown>, onDelta?: (text: string) => void, options?: RequestOptions): Promise<ToolResponseResult> {
    const response = await fetch(aiApiUrl(config, "/responses"), {
        method: "POST",
        headers: { ...aiHeaders(config, "application/json"), Accept: "text/event-stream" },
        body: JSON.stringify({ ...body, stream: true }),
        signal: options?.signal,
    });
    if (!response.ok) throw new Error(await readFetchError(response, apiText("requestFailed")));
    if (!response.body) {
        const payload = (await response.json()) as ResponseApiPayload;
        validateResponsePayload(payload);
        return parseToolResponse(payload);
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    const state: ResponseStreamState = { buffer: "", text: "" };
    for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        consumeResponseStreamText(state, decoder.decode(value, { stream: true }), onDelta);
        if (state.error) throw new Error(state.error);
    }
    consumeResponseStreamText(state, decoder.decode(), onDelta, true);
    if (state.error) throw new Error(state.error);
    if (!state.payload) return { content: state.text, toolCalls: [] };
    validateResponsePayload(state.payload);
    const result = parseToolResponse(state.payload);
    return { ...result, content: state.text || result.content };
}

async function requestGatewayImages(input: {
    config: AiConfig;
    prompt: string;
    references: ReferenceImage[];
    count: number;
    size?: string;
    background?: string;
    imageMode?: string;
    params: Record<string, unknown>;
    options?: RequestOptions;
}): Promise<Array<{ id: string; dataUrl: string }>> {
    const { config, prompt, references, count, size, background, imageMode, params, options } = input;
    const inputs: Record<string, unknown> = { prompt, count };
    if (size) inputs.size = size;
    if (background) inputs.background = background;
    // "layout" is a MOSS mode expressed entirely by params.layout, not a workflow variant.
    if (imageMode && imageMode !== "layout") inputs.mode = imageMode;
    if (config.quality?.trim()) inputs.quality = config.quality.trim();
    Object.assign(inputs, params);

    const capacity = modelImageInputs(config, config.model);
    const usable = typeof capacity === "number" ? references.slice(0, Math.max(0, capacity)) : references;
    const media: Record<string, string[]> = {};
    for (const [index, reference] of usable.entries()) {
        const dataUrl = await imageToDataUrl(reference, { signal: options?.signal });
        if (!dataUrl) continue;
        const uploaded = await uploadMedia(config, dataUrlToFile({ ...reference, dataUrl }), reference.name || `reference-${index + 1}.png`, { signal: options?.signal });
        const role = imageMode === "inpaint" ? (index === 0 ? "source" : "mask") : imageMode === "outpaint" ? "source" : "reference";
        media[role] = [...(media[role] || []), uploaded.id];
    }

    try {
        const run = await createRun(config, { workflow: config.model, inputs, ...(Object.keys(media).length ? { media } : {}) });
        options?.onRun?.(run.id);
        const finished = await waitForRun(config, run.id, { signal: options?.signal, onProgress: options?.onProgress, cancelOnAbort: true });
        const files = await downloadRunOutputs(config, finished, { signal: options?.signal });
        return await Promise.all(
            files.map(async (file, index) => ({
                id: nanoid(),
                dataUrl: await readFileAsDataUrl(new File([file.blob], `image-${index + 1}.png`, { type: file.mime || file.blob.type || "image/png" })),
            })),
        );
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
    }
}

export async function requestGeneration(config: AiConfig, prompt: string, options?: RequestOptions) {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.imageModel);
    const n = Math.max(1, Math.min(15, Math.floor(Math.abs(Number(config.count)) || 1)));
    const requestSize = resolveRequestSize(config.size);
    const background = normalizeBackground(config.background);
    const imageMode = config.imageMode || (channelModelOf(config, requestConfig.model)?.model?.workflowId === "image-qwen21" ? "t2i" : "");
    const requestParams = { ...(options?.params ?? requestConfig.params ?? {}) };
    if (imageMode === "layout" && config.layout) {
        requestParams.layout = config.layout;
        requestParams.layoutEnabled = true;
    }
    return requestGatewayImages({
        config: requestConfig,
        prompt: withSystemPrompt(requestConfig, prompt),
        references: [],
        count: n,
        size: requestSize,
        background,
        imageMode,
        params: requestParams,
        options,
    });
}

export async function requestEdit(config: AiConfig, prompt: string, references: ReferenceImage[], options?: RequestOptions) {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.imageModel);
    const n = Math.max(1, Math.min(15, Math.floor(Math.abs(Number(config.count)) || 1)));
    const requestPrompt = buildImageReferencePromptText(prompt, references);
    const requestSize = resolveRequestSize(config.size);
    const background = normalizeBackground(config.background);
    const imageMode = String(config.imageMode || requestConfig.imageMode || "").trim();
    const requestParams = { ...(options?.params ?? requestConfig.params ?? {}) };
    if (config.imageMode === "outpaint" && config.outpaint) {
        const { left, top, right, bottom, width, height } = config.outpaint;
        // ComfyUI's ImageScaleToTotalPixels counts 1 MP as 1024², not 1e6.
        const megapixels = Math.min(16, Math.max(0.1, (width * height) / (1024 * 1024)));
        Object.assign(requestParams, { left, top, right, bottom, megapixels, outpaintWidth: width, outpaintHeight: height });
    }
    if (config.imageMode === "layout" && config.layout) {
        requestParams.layout = config.layout;
        requestParams.layoutEnabled = true;
    }
    return requestGatewayImages({
        config: requestConfig,
        prompt: withSystemPrompt(requestConfig, requestPrompt),
        references,
        count: n,
        size: requestSize,
        background,
        imageMode,
        params: requestParams,
        options,
    });
}

export type PromptOptimizeMode = "全参考模式(Reference to Video)" | "文生视频(T2VA)" | "首帧图生视频(I2VA)" | "首尾帧视频(FL2VA)" | "尾帧图生视频(L2VA)";

type ImageAngleParams = {
    horizontalAngle: number;
    pitchAngle: number;
    cameraDistance: number;
    wideAngle: boolean;
};

/**
 * Re-render one image from a new camera angle.
 *
 * The angle knobs are the workflow's own inputs (`horizontalAngle`, `pitchAngle`, `cameraDistance`),
 * so this is one run with the source image as its reference — the same path every other image edit
 * takes. `wideAngle` is a UI toggle the graph has no counterpart for, so it is not sent: the gateway
 * rejects inputs a workflow does not declare, and a binding that reaches nothing would be worse than
 * leaving the knob out until the model can honour it.
 */
export async function requestImageAngle(config: AiConfig, prompt: string, params: ImageAngleParams, references: ReferenceImage[], options?: RequestOptions): Promise<Array<{ id: string; dataUrl: string }>> {
    const requestConfig = { ...resolveModelRequestConfig(config, config.imageModel || config.model), baseUrl: LOCAL_GATEWAY_BASE_URL, apiKey: "local", model: IMAGE_ANGLE_WORKFLOW };
    return requestGatewayImages({
        config: requestConfig,
        prompt,
        references,
        count: 1,
        params: {
            horizontalAngle: params.horizontalAngle,
            pitchAngle: params.pitchAngle,
            cameraDistance: params.cameraDistance,
        },
        options,
    });
}

type PromptOptimizeResult = { prompt: string; mode: string; duration: number };

export async function requestPromptOptimize(
    config: AiConfig,
    params: { prompt: string; mode: PromptOptimizeMode; duration?: number; referenceNote?: string; language?: "zh" | "en"; images?: ReferenceImage[]; engine?: "local" | "cloud"; cloud?: { baseUrl?: string; apiKey?: string; model?: string } },
    options?: RequestOptions,
): Promise<PromptOptimizeResult> {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.videoModel);
    const root = localGatewayRoot(requestConfig.baseUrl);
    if (!root) throw new Error(apiText("promptOptimizeLocalOnly"));
    const formData = new FormData();
    formData.set("prompt", params.prompt);
    formData.set("mode", params.mode);
    formData.set("duration", String(params.duration || 5));
    if (params.referenceNote) formData.set("referenceNote", params.referenceNote);
    if (params.language) formData.set("language", params.language);
    if (params.engine) formData.set("engine", params.engine);
    if (params.cloud?.baseUrl) formData.set("cloudBaseUrl", params.cloud.baseUrl);
    if (params.cloud?.apiKey) formData.set("cloudApiKey", params.cloud.apiKey);
    if (params.cloud?.model) formData.set("cloudModel", params.cloud.model);
    const images = params.images || [];
    const files = await Promise.all(images.map(async (image) => dataUrlToFile({ ...image, dataUrl: await imageToDataUrl(image) })));
    files.forEach((file, index) => formData.append(index === 0 ? "image" : `image${index + 1}`, file));
    try {
        const payload = await httpJson<PromptOptimizeResult>(`${root}/api/prompts/optimize`, {
            method: "POST",
            headers: aiHeaders(requestConfig),
            body: formData,
            signal: options?.signal,
            timeout: 240000,
        });
        if (!payload?.prompt) throw new Error(apiText("promptOptimizeEmpty"));
        return payload;
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
    }
}

type ImagePromptOptimizeResult = { prompt: string; mode: string };

export async function requestImagePromptOptimize(
    config: AiConfig,
    params: { prompt: string; mode?: "text" | "image"; language?: "zh" | "en"; images?: ReferenceImage[]; imageCount?: number; engine?: "local" | "cloud"; cloud?: { baseUrl?: string; apiKey?: string; model?: string } },
    options?: RequestOptions,
): Promise<ImagePromptOptimizeResult> {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.imageModel);
    const root = localGatewayRoot(requestConfig.baseUrl);
    if (!root) throw new Error(apiText("promptOptimizeLocalOnly"));
    const formData = new FormData();
    formData.set("prompt", params.prompt);
    if (params.mode) formData.set("mode", params.mode);
    if (params.language) formData.set("language", params.language);
    if (params.engine) formData.set("engine", params.engine);
    if (params.cloud?.baseUrl) formData.set("cloudBaseUrl", params.cloud.baseUrl);
    if (params.cloud?.apiKey) formData.set("cloudApiKey", params.cloud.apiKey);
    if (params.cloud?.model) formData.set("cloudModel", params.cloud.model);
    const images = params.images || [];
    formData.set("imageCount", String(params.imageCount ?? images.length));
    if (params.engine !== "cloud") {
        const files = await Promise.all(images.map(async (image) => dataUrlToFile({ ...image, dataUrl: await imageToDataUrl(image) })));
        files.forEach((file, index) => formData.append(index === 0 ? "image" : `image${index + 1}`, file));
    }
    try {
        const payload = await httpJson<ImagePromptOptimizeResult>(`${root}/api/prompts/optimize-image`, {
            method: "POST",
            headers: aiHeaders(requestConfig),
            body: formData,
            signal: options?.signal,
            timeout: 240000,
        });
        if (!payload?.prompt) throw new Error(apiText("promptOptimizeEmpty"));
        return payload;
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
    }
}

export type ImageCaptionEngine = "qwen35" | "florence" | "cloud";
type ImageCaptionResult = { prompt: string; engine?: ImageCaptionEngine; promptId?: string; elapsedMs?: number };

export async function requestImageCaption(
    config: AiConfig,
    params: { images: ReferenceImage[]; language?: "zh" | "en"; instruction?: string; engine?: ImageCaptionEngine; cloud?: { baseUrl: string; apiKey: string; model: string } },
    options?: RequestOptions,
): Promise<ImageCaptionResult> {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.imageModel);
    const root = localGatewayRoot(requestConfig.baseUrl);
    if (!root) throw new Error(apiText("promptOptimizeLocalOnly"));
    const image = params.images[0];
    if (!image) throw new Error(apiText("requestFailed"));
    const formData = new FormData();
    if (params.language) formData.set("language", params.language);
    if (params.instruction) formData.set("instruction", params.instruction);
    if (params.engine) formData.set("engine", params.engine);
    if (params.cloud?.baseUrl) formData.set("cloudBaseUrl", params.cloud.baseUrl);
    if (params.cloud?.apiKey) formData.set("cloudApiKey", params.cloud.apiKey);
    if (params.cloud?.model) formData.set("cloudModel", params.cloud.model);
    const file = await dataUrlToFile({ ...image, dataUrl: await imageToDataUrl(image) });
    formData.append("image", file);
    try {
        const payload = await httpJson<ImageCaptionResult>(`${root}/api/prompts/caption`, {
            method: "POST",
            headers: aiHeaders(requestConfig),
            body: formData,
            signal: options?.signal,
            timeout: 240000,
        });
        if (!payload?.prompt) throw new Error(apiText("promptOptimizeEmpty"));
        return payload;
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
    }
}

type UpscaleOptions = { workflowId: string; params?: Record<string, unknown>; signal?: AbortSignal };

/**
 * Upscale one image with a tool workflow, as a run.
 *
 * The chosen upscaler is a workflow id, its knobs (`upscale_model`, `resolution`) are that
 * workflow's own inputs and the source image is its media, so this is one run — the same path every
 * other image edit takes. There is no prompt: the workflow's prompt binding carries its own
 * default, and sending an undeclared key would be rejected by the typed input contract.
 */
export async function requestUpscale(config: AiConfig, image: ReferenceImage, options: UpscaleOptions) {
    if (!localGatewayRoot(config.baseUrl)) throw new Error(apiText("upscaleLocalOnly"));
    const requestConfig = { ...resolveModelRequestConfig(config, config.imageModel || config.model), baseUrl: LOCAL_GATEWAY_BASE_URL, apiKey: "local", model: options.workflowId };
    try {
        const dataUrl = await imageToDataUrl(image);
        const uploaded = await uploadMedia(requestConfig, dataUrlToFile({ ...image, dataUrl }), image.name || `${image.id}.png`, { signal: options.signal });
        const run = await createRun(requestConfig, {
            workflow: options.workflowId,
            inputs: { ...(options.params ?? {}) },
            media: { image: [uploaded.id] },
        });
        const finished = await waitForRun(requestConfig, run.id, { signal: options.signal, cancelOnAbort: true });
        const files = await downloadRunOutputs(requestConfig, finished, { signal: options.signal });
        return await Promise.all(
            files.map(async (file, index) => ({
                id: nanoid(),
                dataUrl: await readFileAsDataUrl(new File([file.blob], `upscaled-${index + 1}.png`, { type: file.mime || file.blob.type || "image/png" })),
            })),
        );
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
    }
}

/**
 * Stream a text answer from a chat model (OpenAI Responses shape).
 *
 * The gateway has no text endpoint, so in this build the only models a text node can reach are the
 * ones the channel lists — image/video/audio workflows, which do not answer here. It is kept because
 * the canvas's text node still offers it; see docs/known-issues.md.
 */
export async function requestImageQuestion(config: AiConfig, messages: AiTextMessage[], onDelta: (text: string) => void, options?: RequestOptions) {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.textModel);
    try {
        const answer =
            (
                await requestStreamingResponse(
                    requestConfig,
                    {
                        model: requestConfig.model,
                        input: toResponseInput(withSystemMessage(requestConfig, messages)),
                        ...(requestConfig.reasoningEffort === "auto" ? {} : { reasoning: { effort: requestConfig.reasoningEffort } }),
                    },
                    onDelta,
                    options,
                )
            ).content || apiText("noContent");
        if (answer === apiText("noContent")) onDelta(answer);
        return answer;
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
    }
}
