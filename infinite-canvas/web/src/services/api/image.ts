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
type GeminiPart = {
    text?: string;
    inlineData?: { mimeType?: string; data?: string };
    inline_data?: { mime_type?: string; mimeType?: string; data?: string };
    fileData?: { mimeType?: string; fileUri?: string };
    functionCall?: { id?: string; name?: string; args?: Record<string, unknown> };
    functionResponse?: { id?: string; name?: string; response?: Record<string, unknown> };
    thoughtSignature?: string;
    thought_signature?: string;
};
type GeminiContent = { role?: "user" | "model"; parts: GeminiPart[] };
type GeminiPayload = {
    candidates?: Array<{ content?: { parts?: GeminiPart[] }; finishReason?: string }>;
    models?: Array<{ name?: string }>;
    error?: { message?: string };
    promptFeedback?: { blockReason?: string };
};
type GeminiStreamState = { buffer: string; text: string; toolCalls: ResponseToolCall[]; error?: string };
type RequestOptions = {
    signal?: AbortSignal;
    params?: Record<string, unknown>;
    /** Reported as soon as the gateway accepts a run, so the node can resume it after a reload. */
    onRun?: (runId: string) => void;
    onProgress?: (run: Run) => void;
};

/** Gateway image workflow ids are `image-<slug>` (the registry's naming rule). */
const GATEWAY_IMAGE_WORKFLOW = /^image-/;

/** Whether this channel+model is served by the local gateway and therefore uses runs. */
export function isGatewayImageModel(config: Pick<AiConfig, "baseUrl" | "model">) {
    // The selection may be the encoded `channel::model` form; the workflow id is the bare name.
    return isLocalGatewayUrl(config.baseUrl) && GATEWAY_IMAGE_WORKFLOW.test(modelOptionName(config.model));
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
function attachLocalCancel(config: AiConfig, requestId: string, signal?: AbortSignal) {
    const root = localGatewayRoot(config.baseUrl);
    if (!root || !signal) return null;
    const onAbort = () => {
        void fetch(`${root}/api/requests/${encodeURIComponent(requestId)}/cancel`, {
            method: "POST",
            headers: { "content-type": "application/json" },
            body: "{}",
        }).catch(() => undefined);
    };
    signal.addEventListener("abort", onAbort, { once: true });
    return () => signal.removeEventListener("abort", onAbort);
}

// The gateway only learns prompt_id once ComfyUI accepts the request, so progress is polled by requestId.
function attachLocalProgress(config: AiConfig, requestId: string, signal?: AbortSignal) {
    const root = localGatewayRoot(config.baseUrl);
    if (!root || !signal) return null;
    let stopped = false;
    let timer = 0;
    const tick = async () => {
        if (stopped) return;
        try {
            const progress = await httpJson<{ step?: number; total?: number }>(`${root}/api/progress/${encodeURIComponent(requestId)}`, { timeout: 3000 });
            if (stopped) return;
            const total = Math.floor(Number(progress?.total) || 0);
            const step = Math.floor(Number(progress?.step) || 0);
            if (total > 0) publishStepProgress(signal, { step: Math.min(total, Math.max(0, step)), total, requestId });
        } catch {
            // Progress is best-effort and must never fail the generation itself.
        }
        if (!stopped) timer = window.setTimeout(tick, 250);
    };
    void tick();
    return () => {
        stopped = true;
        window.clearTimeout(timer);
    };
}

const GEMINI_SUPPORTED_RATIOS = ["1:1", "1:4", "1:8", "2:3", "3:2", "3:4", "4:1", "4:3", "4:5", "5:4", "8:1", "9:16", "16:9", "21:9"];

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

function resolveGeminiImageConfig(config: AiConfig) {
    const value = config.size.trim();
    const dimensions = parseImageDimensions(value);
    const ratio = dimensions ? `${dimensions.width}:${dimensions.height}` : value;
    const aspectRatio = value && value.toLowerCase() !== "auto" ? closestGeminiAspectRatio(ratio) : undefined;
    const imageSize = resolveGeminiImageSize(dimensions);
    const image = { ...(aspectRatio ? { aspectRatio } : {}), ...(imageSize ? { imageSize } : {}) };
    return Object.keys(image).length ? { imageConfig: image } : {};
}

function closestGeminiAspectRatio(value: string) {
    const ratio = parseImageRatio(value);
    const target = ratio.width / ratio.height;
    return GEMINI_SUPPORTED_RATIOS.reduce((best, item) => {
        const current = parseRatioValue(item);
        const bestRatio = parseRatioValue(best);
        return Math.abs(current.width / current.height - target) < Math.abs(bestRatio.width / bestRatio.height - target) ? item : best;
    });
}

/** Gemini takes the resolution tier and the ratio separately; the tier comes from the size presets. */
function resolveGeminiImageSize(dimensions: { width: number; height: number } | null) {
    if (!dimensions) return undefined;
    const size = `${dimensions.width}x${dimensions.height}`;
    const scale = Object.keys(imageSizePresets).find((key) => Object.values(imageSizePresets[key]).includes(size));
    if (scale) return scale.toUpperCase();
    const edge = Math.max(dimensions.width, dimensions.height);
    if (edge <= 768) return "512";
    if (edge <= 1536) return "1K";
    if (edge <= 3072) return "2K";
    return "4K";
}

function resolveImageSource(item: Record<string, unknown>) {
    if (typeof item.b64_json === "string" && item.b64_json) {
        return `data:image/png;base64,${item.b64_json}`;
    }
    if (typeof item.url === "string" && item.url) {
        return item.url;
    }
    return null;
}

function parseImagePayload(payload: ImageApiResponse) {
    if (typeof payload.code === "number" && payload.code !== 0) {
        throw new Error(payload.msg || apiText("requestFailed"));
    }
    // Support data, images, and results response fields used by different APIs.
    const imageList = payload.data || ((payload as Record<string, unknown>).images as Array<Record<string, unknown>> | undefined) || ((payload as Record<string, unknown>).results as Array<Record<string, unknown>> | undefined) || [];
    const images = imageList
        .map(resolveImageSource)
        .filter((value): value is string => Boolean(value))
        .map((dataUrl) => ({ id: nanoid(), dataUrl }));

    if (images.length === 0) {
        // Check whether the response contains data in an unrecognized format.
        const rawKeys = Object.keys(payload).filter((k) => k !== "code" && k !== "msg" && k !== "error");
        throw new Error(rawKeys.length > 0 ? apiText("unknownImageResponse", { fields: rawKeys.join(", ") }) : apiText("noImageReturned"));
    }

    return images;
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

function geminiBaseUrl(config: Pick<AiConfig, "baseUrl">) {
    const normalizedBaseUrl = config.baseUrl.trim().replace(/\/+$/, "");
    const lowerBaseUrl = normalizedBaseUrl.toLowerCase();
    return lowerBaseUrl.endsWith("/v1") || lowerBaseUrl.endsWith("/v1beta") ? normalizedBaseUrl : `${normalizedBaseUrl}/v1beta`;
}

function geminiModelName(model: string) {
    return model.trim().replace(/^models\//, "");
}

function geminiApiUrl(config: Pick<AiConfig, "baseUrl" | "model">, action?: "generateContent" | "streamGenerateContent") {
    const baseUrl = geminiBaseUrl(config);
    if (!action) return `${baseUrl}/models`;
    return `${baseUrl}/models/${encodeURIComponent(geminiModelName(config.model))}:${action}`;
}

function geminiHeaders(config: Pick<AiConfig, "apiKey">) {
    return {
        "x-goog-api-key": config.apiKey,
        "Content-Type": "application/json",
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

function validateGeminiPayload(payload: GeminiPayload) {
    if (payload.error?.message) throw new Error(payload.error.message);
    if (payload.promptFeedback?.blockReason) throw new Error(apiText("geminiRejected", { reason: payload.promptFeedback.blockReason }));
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

function toGeminiBody(config: AiConfig, messages: ResponseInputMessage[], extra?: Record<string, unknown>) {
    const systemText = [config.systemPrompt.trim(), ...messages.flatMap((message) => (!("type" in message) && message.role === "system" ? [geminiTextContent(message.content)] : []))].filter(Boolean).join("\n\n");
    const contents = toGeminiContents(messages.filter((message) => ("type" in message ? true : message.role !== "system")));
    return {
        contents,
        ...(systemText ? { systemInstruction: { parts: [{ text: systemText }] } } : {}),
        ...extra,
    };
}

function toGeminiContents(messages: ResponseInputMessage[]): GeminiContent[] {
    const callNameById = new Map<string, string>();
    return messages.flatMap((message): GeminiContent[] => {
        if ("type" in message) {
            callNameById.set(message.call_id, message.name);
            return [{ role: "model", parts: [{ functionCall: { id: message.call_id, name: message.name, args: jsonObject(message.arguments) }, ...(message.thoughtSignature ? { thoughtSignature: message.thoughtSignature } : {}) }] }];
        }
        if (message.role === "tool") {
            const name = callNameById.get(message.tool_call_id) || "tool_result";
            return [{ role: "user", parts: [{ functionResponse: { id: message.tool_call_id, name, response: { result: jsonValue(message.content) } } }] }];
        }
        return [{ role: message.role === "assistant" ? "model" : "user", parts: toGeminiParts(message.content) }];
    });
}

function toGeminiParts(content: ResponseMessageContent): GeminiPart[] {
    if (!Array.isArray(content)) return [{ text: String(content || "") }];
    return content.map((item) => (item.type === "text" ? { text: item.text } : toGeminiImagePart(item.image_url.url)));
}

function toGeminiImagePart(url: string): GeminiPart {
    const match = url.match(/^data:([^;,]+);base64,(.+)$/);
    if (match) return { inlineData: { mimeType: match[1], data: match[2] } };
    return { fileData: { fileUri: url, mimeType: "image/png" } };
}

function geminiTextContent(content: ResponseMessageContent) {
    if (!Array.isArray(content)) return String(content || "");
    return content.map((item) => (item.type === "text" ? item.text : item.image_url.url)).join("\n");
}

function jsonObject(value: string): Record<string, unknown> {
    const parsed = jsonValue(value);
    return isRecord(parsed) ? parsed : {};
}

function jsonValue(value: string): unknown {
    try {
        return JSON.parse(value);
    } catch {
        return value;
    }
}

async function requestGeminiStreamingResponse(config: AiConfig, body: Record<string, unknown>, onDelta?: (text: string) => void, options?: RequestOptions): Promise<ToolResponseResult> {
    const response = await fetch(`${geminiApiUrl(config, "streamGenerateContent")}?alt=sse`, {
        method: "POST",
        headers: geminiHeaders(config),
        body: JSON.stringify(body),
        signal: options?.signal,
    });
    if (!response.ok) throw new Error(await readFetchError(response, apiText("requestFailed")));
    if (!response.body) {
        const payload = (await response.json()) as GeminiPayload;
        return parseGeminiToolResponse(payload);
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    const state: GeminiStreamState = { buffer: "", text: "", toolCalls: [] };
    for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        consumeGeminiStreamText(state, decoder.decode(value, { stream: true }), onDelta);
        if (state.error) throw new Error(state.error);
    }
    consumeGeminiStreamText(state, decoder.decode(), onDelta, true);
    if (state.error) throw new Error(state.error);
    return { content: state.text, toolCalls: state.toolCalls };
}

function consumeGeminiStreamText(state: GeminiStreamState, text: string, onDelta?: (text: string) => void, flush = false) {
    state.buffer += text;
    for (;;) {
        const match = state.buffer.match(/\r?\n\r?\n/);
        if (!match) break;
        const index = match.index ?? 0;
        consumeGeminiStreamBlock(state.buffer.slice(0, index), state, onDelta);
        state.buffer = state.buffer.slice(index + match[0].length);
    }
    if (flush && state.buffer.trim()) {
        consumeGeminiStreamBlock(state.buffer, state, onDelta);
        state.buffer = "";
    }
}

function consumeGeminiStreamBlock(block: string, state: GeminiStreamState, onDelta?: (text: string) => void) {
    const data = block
        .split(/\r?\n/)
        .filter((line) => line.startsWith("data:"))
        .map((line) => line.slice(5).replace(/^ /, ""))
        .join("\n")
        .trim();
    if (!data || data === "[DONE]") return;
    const result = parseGeminiToolResponse(JSON.parse(data) as GeminiPayload);
    if (result.content) {
        state.text += result.content;
        onDelta?.(state.text);
    }
    state.toolCalls.push(...result.toolCalls);
}

function parseGeminiToolResponse(payload: GeminiPayload): ToolResponseResult {
    validateGeminiPayload(payload);
    const parts = payload.candidates?.flatMap((candidate) => candidate.content?.parts || []) || [];
    const content = parts.map((part) => part.text || "").join("");
    const toolCalls = parts
        .map((part) => part.functionCall)
        .filter((call): call is NonNullable<GeminiPart["functionCall"]> => Boolean(call?.name))
        .map((call) => {
            const part = parts.find((item) => item.functionCall === call);
            const thoughtSignature = part?.thoughtSignature || part?.thought_signature;
            return {
                id: call.id || nanoid(),
                type: "function" as const,
                function: { name: call.name || "", arguments: JSON.stringify(call.args || {}) },
                ...(thoughtSignature ? { thoughtSignature } : {}),
            };
        });
    return { content, toolCalls };
}

async function requestGeminiImages(config: AiConfig, prompt: string, references: ReferenceImage[], count: number, options?: RequestOptions) {
    const requests = Array.from({ length: count }, () => requestGeminiImagesOnce(config, prompt, references, options));
    return (await Promise.all(requests)).flat();
}

async function requestGeminiImagesOnce(config: AiConfig, prompt: string, references: ReferenceImage[], options?: RequestOptions) {
    const parts: GeminiPart[] = [{ text: prompt }];
    for (const image of references) {
        parts.push(toGeminiImagePart(await imageToDataUrl(image)));
    }
    const payload = await httpJson<GeminiPayload>(geminiApiUrl(config, "generateContent"), {
        method: "POST",
        headers: geminiHeaders(config),
        body: JSON.stringify({
            ...toGeminiBody(config, [{ role: "user", content: prompt }], { generationConfig: { responseModalities: ["TEXT", "IMAGE"], ...resolveGeminiImageConfig(config) } }),
            contents: [{ role: "user", parts }],
        }),
        signal: options?.signal,
        timeout: IMAGE_REQUEST_TIMEOUT_MS,
    });
    return parseGeminiImagePayload(payload);
}

function parseGeminiImagePayload(payload: GeminiPayload) {
    validateGeminiPayload(payload);
    const images =
        payload.candidates
            ?.flatMap((candidate) => candidate.content?.parts || [])
            .map((part) => {
                const inlineData = part.inlineData || (part.inline_data ? { mimeType: part.inline_data.mimeType || part.inline_data.mime_type, data: part.inline_data.data } : undefined);
                if (inlineData?.data) return `data:${inlineData.mimeType || "image/png"};base64,${inlineData.data}`;
                return part.fileData?.fileUri || null;
            })
            .filter((value): value is string => Boolean(value))
            .map((dataUrl) => ({ id: nanoid(), dataUrl })) || [];
    if (!images.length) throw new Error(apiText("geminiNoImage"));
    return images;
}

/**
 * Generate through the gateway's run protocol.
 *
 * The reference images are uploaded first and named by role, the run carries the semantic inputs
 * (prompt, size, quality, mode) plus the workflow's own params, and each output is returned as a
 * data URL — the shape every image caller already expects.
 *
 * Reference capacity comes from the channel metadata (`imageInputs`): a workflow that takes no
 * images ignores extras today, and sending them would now be a `400` from the typed input contract.
 */
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
    if (requestConfig.apiFormat === "gemini") {
        try {
            return await requestGeminiImages(requestConfig, prompt, [], n, options);
        } catch (error) {
            throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
        }
    }
    const requestSize = resolveRequestSize(config.size);
    const background = normalizeBackground(config.background);
    const imageMode = config.imageMode || (channelModelOf(config, requestConfig.model)?.model?.workflowId === "image-qwen21" ? "t2i" : "");
    const requestParams = { ...(options?.params ?? requestConfig.params ?? {}) };
    if (imageMode === "layout" && config.layout) {
        requestParams.layout = config.layout;
        requestParams.layoutEnabled = true;
    }
    if (isGatewayImageModel(requestConfig)) {
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

    const requestId = `img_${nanoid()}`;
    const detachCancel = attachLocalCancel(requestConfig, requestId, options?.signal);
    const detachProgress = attachLocalProgress(requestConfig, requestId, options?.signal);
    try {
        const payload = await httpJson<ImageApiResponse>(aiApiUrl(requestConfig, "/images/generations"), {
            method: "POST",
            body: JSON.stringify({
                model: requestConfig.model,
                prompt: withSystemPrompt(requestConfig, prompt),
                n,
                ...(requestSize ? { size: requestSize } : {}),
                ...(background ? { background } : {}),
                ...(imageMode ? { mode: imageMode } : {}),
                ...(Object.keys(requestParams).length ? { params: requestParams } : {}),
                // gpt-image models reject response_format; they always return b64.
                ...(/gpt-image/.test(requestConfig.model) ? {} : { response_format: "b64_json" }),
                output_format: IMAGE_OUTPUT_FORMAT,
            }),
            headers: { ...aiHeaders(requestConfig, "application/json"), ...(detachCancel ? { "X-Request-Id": requestId } : {}) },
            signal: options?.signal,
            timeout: IMAGE_REQUEST_TIMEOUT_MS,
        });
        const images = await parseImagePayload(payload);
        return images;
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
    } finally {
        detachCancel?.();
        detachProgress?.();
    }
}

export async function requestEdit(config: AiConfig, prompt: string, references: ReferenceImage[], options?: RequestOptions) {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.imageModel);
    const n = Math.max(1, Math.min(15, Math.floor(Math.abs(Number(config.count)) || 1)));
    const requestPrompt = buildImageReferencePromptText(prompt, references);
    if (requestConfig.apiFormat === "gemini") {
        try {
            return await requestGeminiImages(requestConfig, requestPrompt, references, n, options);
        } catch (error) {
            throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
        }
    }

    const requestSize = resolveRequestSize(config.size);
    const background = normalizeBackground(config.background);
    const formData = new FormData();
    formData.set("model", requestConfig.model);
    formData.set("prompt", withSystemPrompt(requestConfig, requestPrompt));
    formData.set("n", String(n));
    const imageMode = String(config.imageMode || requestConfig.imageMode || "").trim();
    if (imageMode) formData.set("mode", imageMode);
    // gpt-image models reject response_format; they always return b64.
    if (!/gpt-image/.test(requestConfig.model)) {
        formData.set("response_format", "b64_json");
    }
    formData.set("output_format", IMAGE_OUTPUT_FORMAT);
    if (requestSize) {
        formData.set("size", requestSize);
    }
    if (background) {
        formData.set("background", background);
    }
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
    if (Object.keys(requestParams).length) {
        formData.set("params", JSON.stringify(requestParams));
    }
    if (isGatewayImageModel(requestConfig)) {
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

    const files = await Promise.all(references.map(async (image) => dataUrlToFile({ ...image, dataUrl: await imageToDataUrl(image) })));
    const imageField = files.length > 1 ? "image[]" : "image";
    files.forEach((file) => formData.append(imageField, file));

    const requestId = `img_${nanoid()}`;
    const detachCancel = attachLocalCancel(requestConfig, requestId, options?.signal);
    const detachProgress = attachLocalProgress(requestConfig, requestId, options?.signal);
    try {
        const payload = await httpJson<ImageApiResponse>(aiApiUrl(requestConfig, "/images/edits"), {
            method: "POST",
            headers: { ...aiHeaders(requestConfig), ...(detachCancel ? { "X-Request-Id": requestId } : {}) },
            body: formData,
            signal: options?.signal,
            timeout: IMAGE_REQUEST_TIMEOUT_MS,
        });
        const images = await parseImagePayload(payload);
        return images;
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
    } finally {
        detachCancel?.();
        detachProgress?.();
    }
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
    const requestConfig = { ...resolveModelRequestConfig(config, config.imageModel || config.model), baseUrl: LOCAL_GATEWAY_BASE_URL, apiKey: "local", apiFormat: "openai" as const, model: IMAGE_ANGLE_WORKFLOW };
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
    const requestConfig = { ...resolveModelRequestConfig(config, config.imageModel || config.model), baseUrl: LOCAL_GATEWAY_BASE_URL, apiKey: "local", apiFormat: "openai" as const, model: options.workflowId };
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

export async function requestImageQuestion(config: AiConfig, messages: AiTextMessage[], onDelta: (text: string) => void, options?: RequestOptions) {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.textModel);
    try {
        if (requestConfig.apiFormat === "gemini") {
            const answer = (await requestGeminiStreamingResponse(requestConfig, toGeminiBody(requestConfig, messages), onDelta, options)).content || apiText("noContent");
            if (answer === apiText("noContent")) onDelta(answer);
            return answer;
        }
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

async function fetchImageModels(config: Pick<AiConfig, "baseUrl" | "apiKey" | "apiFormat">) {
    try {
        if (config.apiFormat === "gemini") {
            const payload = await httpJson<GeminiPayload>(geminiApiUrl({ ...defaultGeminiConfig, ...config }), { headers: geminiHeaders({ ...defaultGeminiConfig, ...config }) });
            validateGeminiPayload(payload);
            return (payload.models || [])
                .map((model) => model.name?.replace(/^models\//, ""))
                .filter((id): id is string => Boolean(id))
                .sort((a, b) => a.localeCompare(b));
        }
        const payload = await httpJson<{ data?: Array<{ id?: string }>; error?: { message?: string } }>(buildApiUrl(config.baseUrl, "/models"), {
            headers: {
                Authorization: `Bearer ${config.apiKey}`,
            },
        });
        return (payload.data || [])
            .map((model) => model.id)
            .filter((id): id is string => Boolean(id))
            .sort((a, b) => a.localeCompare(b));
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("modelReadFailed"), "imageTimeout"));
    }
}

export async function fetchChannelModels(channel: ModelChannel) {
    return fetchImageModels({ baseUrl: channel.baseUrl, apiKey: channel.apiKey, apiFormat: channel.apiFormat });
}

const defaultGeminiConfig: Pick<AiConfig, "baseUrl" | "apiKey" | "apiFormat" | "model" | "systemPrompt"> = {
    baseUrl: "https://generativelanguage.googleapis.com",
    apiKey: "",
    apiFormat: "gemini",
    model: "",
    systemPrompt: "",
};
