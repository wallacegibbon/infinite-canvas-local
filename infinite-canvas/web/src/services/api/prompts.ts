import i18n from "@/i18n";
import { dataUrlToFile } from "@/lib/image-utils";
import { requestAgentStream } from "@/services/api/agent";
import { localGatewayRoot } from "@/services/api/image";
import { apiErrorMessage, httpJson } from "@/services/api/http";
import { imageToDataUrl } from "@/services/image-storage";
import { resolveModelRequestConfig, type AiConfig } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";

const apiText = (key: string, options?: Record<string, unknown>) => i18n.t(`apiErrors.${key}`, options);

/**
 * The prompt services: `/api/prompts/*`.
 *
 * All four live on the gateway and all four end up in one `POST <base URL>/chat/completions` — the
 * endpoint, key and model travel with each request because the browser never contacts the model
 * itself. There is exactly one prompt endpoint in the settings, so these functions read it rather
 * than take it as an argument.
 */
function promptEndpoint(config: AiConfig) {
    return {
        baseUrl: (config.promptBaseUrl || "").trim(),
        apiKey: (config.promptApiKey || "").trim(),
        model: (config.promptModel || "").trim(),
    };
}

function setPromptEndpoint(formData: FormData, config: AiConfig) {
    const { baseUrl, apiKey, model } = promptEndpoint(config);
    if (baseUrl) formData.set("promptBaseUrl", baseUrl);
    if (apiKey) formData.set("promptApiKey", apiKey);
    if (model) formData.set("promptModel", model);
}

/** A prompt call needs the gateway: that is where the endpoint's credentials are used. */
function promptRoot(config: AiConfig) {
    const root = localGatewayRoot(config.baseUrl);
    if (!root) throw new Error(apiText("promptOptimizeLocalOnly"));
    return root;
}

async function appendImages(formData: FormData, images: ReferenceImage[]) {
    const files = await Promise.all(images.map(async (image) => dataUrlToFile({ ...image, dataUrl: await imageToDataUrl(image) })));
    files.forEach((file, index) => formData.append(index === 0 ? "image" : `image${index + 1}`, file));
}

async function postPrompt<T>(url: string, config: AiConfig, formData: FormData, signal?: AbortSignal): Promise<T> {
    setPromptEndpoint(formData, config);
    try {
        const payload = await httpJson<T>(url, {
            method: "POST",
            headers: { Authorization: `Bearer ${config.apiKey}` },
            body: formData,
            signal,
            timeout: 240000,
        });
        if (!payload) throw new Error(apiText("requestFailed"));
        return payload;
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
    }
}

export type PromptOptimizeMode = "全参考模式(Reference to Video)" | "文生视频(T2VA)" | "首帧图生视频(I2VA)" | "首尾帧视频(FL2VA)" | "尾帧图生视频(L2VA)";

type PromptOptimizeResult = { prompt: string; mode: string; duration: number };

export async function requestPromptOptimize(
    config: AiConfig,
    params: { prompt: string; mode: PromptOptimizeMode; duration?: number; referenceNote?: string; language?: "zh" | "en"; images?: ReferenceImage[] },
    options?: { signal?: AbortSignal },
): Promise<PromptOptimizeResult> {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.videoModel);
    const formData = new FormData();
    formData.set("prompt", params.prompt);
    formData.set("mode", params.mode);
    formData.set("duration", String(params.duration || 5));
    if (params.referenceNote) formData.set("referenceNote", params.referenceNote);
    if (params.language) formData.set("language", params.language);
    await appendImages(formData, params.images || []);
    const payload = await postPrompt<PromptOptimizeResult>(`${promptRoot(requestConfig)}/api/prompts/optimize`, requestConfig, formData, options?.signal);
    if (!payload.prompt) throw new Error(apiText("promptOptimizeEmpty"));
    return payload;
}

type ImagePromptOptimizeResult = { prompt: string; mode: string };

/**
 * Rewrite a prompt for image generation.
 *
 * Text only, by design: the gateway answers this one from the text alone, so no image is attached —
 * `imageCount` is what tells it how many references the prompt is about.
 */
export async function requestImagePromptOptimize(config: AiConfig, params: { prompt: string; mode?: "text" | "image"; language?: "zh" | "en"; imageCount?: number }, options?: { signal?: AbortSignal }): Promise<ImagePromptOptimizeResult> {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.imageModel);
    const formData = new FormData();
    formData.set("prompt", params.prompt);
    if (params.mode) formData.set("mode", params.mode);
    if (params.language) formData.set("language", params.language);
    formData.set("imageCount", String(params.imageCount ?? 0));
    const payload = await postPrompt<ImagePromptOptimizeResult>(`${promptRoot(requestConfig)}/api/prompts/optimize-image`, requestConfig, formData, options?.signal);
    if (!payload.prompt) throw new Error(apiText("promptOptimizeEmpty"));
    return payload;
}

type ImageCaptionResult = { prompt: string; promptId?: string; elapsedMs?: number };

export async function requestImageCaption(config: AiConfig, params: { images: ReferenceImage[]; language?: "zh" | "en"; instruction?: string }, options?: { signal?: AbortSignal }): Promise<ImageCaptionResult> {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.imageModel);
    const image = params.images[0];
    if (!image) throw new Error(apiText("requestFailed"));
    const formData = new FormData();
    if (params.language) formData.set("language", params.language);
    if (params.instruction) formData.set("instruction", params.instruction);
    await appendImages(formData, [image]);
    const payload = await postPrompt<ImageCaptionResult>(`${promptRoot(requestConfig)}/api/prompts/caption`, requestConfig, formData, options?.signal);
    if (!payload.prompt) throw new Error(apiText("promptOptimizeEmpty"));
    return payload;
}

/**
 * Ask the agent for words: one prompt in, one answer out, read as it arrives.
 *
 * The model and the reasoning level are the agent's own settings, not fields of a prompt — they are
 * changed with `command` (see requestAgentStream), never attached to a question.
 */
export async function requestPromptChat(config: AiConfig, params: { prompt: string; images?: ReferenceImage[] }, options?: { signal?: AbortSignal; onDelta?: (text: string) => void }): Promise<string> {
    let streamed = "";
    const answer = await requestAgentStream(
        config,
        { prompt: params.prompt, images: params.images },
        {
            signal: options?.signal,
            onEvent: (event) => {
                if (event.type !== "delta" || !event.text) return;
                streamed += event.text;
                options?.onDelta?.(streamed);
            },
        },
    );
    return answer || streamed;
}
