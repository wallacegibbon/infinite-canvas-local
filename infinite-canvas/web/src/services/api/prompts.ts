import i18n from "@/i18n";
import { dataUrlToFile } from "@/lib/image-utils";
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
 * endpoint, key and model travel with each request (`cloudBaseUrl`/`cloudApiKey`/`cloudModel`) because
 * the browser never contacts the model directly. Only the gateway does, and it returns plain JSON; the
 * canvas streams nothing, so one request is one answer.
 */
type PromptCloud = { baseUrl?: string; apiKey?: string; model?: string };

/** Reads the prompt endpoint the settings hold. Every request here uses the same one. */
function cloudOf(config: AiConfig): PromptCloud {
    return {
        baseUrl: (config.promptCloudBaseUrl || "").trim(),
        apiKey: (config.promptCloudApiKey || "").trim(),
        model: (config.promptCloudModel || "").trim(),
    };
}

function setCloud(formData: FormData, cloud: PromptCloud) {
    if (cloud.baseUrl) formData.set("cloudBaseUrl", cloud.baseUrl);
    if (cloud.apiKey) formData.set("cloudApiKey", cloud.apiKey);
    if (cloud.model) formData.set("cloudModel", cloud.model);
}

function gatewayHeaders(config: AiConfig) {
    return { Authorization: `Bearer ${config.apiKey}` };
}

/** A prompt-API call needs the gateway: that is where the endpoint's credentials are used. */
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
    try {
        const payload = await httpJson<T>(url, {
            method: "POST",
            headers: gatewayHeaders(config),
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
    params: { prompt: string; mode: PromptOptimizeMode; duration?: number; referenceNote?: string; language?: "zh" | "en"; images?: ReferenceImage[]; engine?: "local" | "cloud"; cloud?: PromptCloud },
    options?: { signal?: AbortSignal },
): Promise<PromptOptimizeResult> {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.videoModel);
    const root = promptRoot(requestConfig);
    const formData = new FormData();
    formData.set("prompt", params.prompt);
    formData.set("mode", params.mode);
    formData.set("duration", String(params.duration || 5));
    if (params.referenceNote) formData.set("referenceNote", params.referenceNote);
    if (params.language) formData.set("language", params.language);
    if (params.engine) formData.set("engine", params.engine);
    setCloud(formData, params.cloud ?? cloudOf(requestConfig));
    await appendImages(formData, params.images || []);
    const payload = await postPrompt<PromptOptimizeResult>(`${root}/api/prompts/optimize`, requestConfig, formData, options?.signal);
    if (!payload.prompt) throw new Error(apiText("promptOptimizeEmpty"));
    return payload;
}

type ImagePromptOptimizeResult = { prompt: string; mode: string };

export async function requestImagePromptOptimize(
    config: AiConfig,
    params: { prompt: string; mode?: "text" | "image"; language?: "zh" | "en"; images?: ReferenceImage[]; imageCount?: number; engine?: "local" | "cloud"; cloud?: PromptCloud },
    options?: { signal?: AbortSignal },
): Promise<ImagePromptOptimizeResult> {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.imageModel);
    const root = promptRoot(requestConfig);
    const formData = new FormData();
    formData.set("prompt", params.prompt);
    if (params.mode) formData.set("mode", params.mode);
    if (params.language) formData.set("language", params.language);
    if (params.engine) formData.set("engine", params.engine);
    setCloud(formData, params.cloud ?? cloudOf(requestConfig));
    const images = params.images || [];
    formData.set("imageCount", String(params.imageCount ?? images.length));
    if (params.engine !== "cloud") await appendImages(formData, images);
    const payload = await postPrompt<ImagePromptOptimizeResult>(`${root}/api/prompts/optimize-image`, requestConfig, formData, options?.signal);
    if (!payload.prompt) throw new Error(apiText("promptOptimizeEmpty"));
    return payload;
}

export type ImageCaptionEngine = "qwen35" | "florence" | "cloud";
type ImageCaptionResult = { prompt: string; engine?: ImageCaptionEngine; promptId?: string; elapsedMs?: number };

export async function requestImageCaption(
    config: AiConfig,
    params: { images: ReferenceImage[]; language?: "zh" | "en"; instruction?: string; engine?: ImageCaptionEngine; cloud?: PromptCloud },
    options?: { signal?: AbortSignal },
): Promise<ImageCaptionResult> {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.imageModel);
    const root = promptRoot(requestConfig);
    const image = params.images[0];
    if (!image) throw new Error(apiText("requestFailed"));
    const formData = new FormData();
    if (params.language) formData.set("language", params.language);
    if (params.instruction) formData.set("instruction", params.instruction);
    if (params.engine) formData.set("engine", params.engine);
    setCloud(formData, params.cloud ?? cloudOf(requestConfig));
    await appendImages(formData, [image]);
    const payload = await postPrompt<ImageCaptionResult>(`${root}/api/prompts/caption`, requestConfig, formData, options?.signal);
    if (!payload.prompt) throw new Error(apiText("promptOptimizeEmpty"));
    return payload;
}

/**
 * Ask the configured model for words: one prompt in, one answer out.
 *
 * The gateway contributes no wording of its own here (there is no skill and no sanitiser), so the
 * `system` message is the caller's. The settings' system prompt always applies, as it does for image
 * requests; a caller that has its own is appended after it. Images are attached when the caller has
 * them, which is what makes a vision model usable from a text node.
 */
export async function requestPromptChat(config: AiConfig, params: { prompt: string; system?: string; images?: ReferenceImage[] }, options?: { signal?: AbortSignal }): Promise<string> {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.textModel);
    const root = promptRoot(requestConfig);
    const system = [config.systemPrompt, params.system]
        .map((part) => (part || "").trim())
        .filter(Boolean)
        .join("\n\n");

    const formData = new FormData();
    formData.set("prompt", params.prompt);
    if (system) formData.set("system", system);
    setCloud(formData, cloudOf(config));
    await appendImages(formData, params.images || []);

    const payload = await postPrompt<{ text?: string }>(`${root}/api/prompts/chat`, requestConfig, formData, options?.signal);
    if (!payload.text) throw new Error(apiText("promptOptimizeEmpty"));
    return payload.text;
}
