import i18n from "@/i18n";
import { audioMimeType, normalizeAudioFormatValue, normalizeAudioSpeedValue, normalizeAudioVoiceValue } from "@/lib/audio-generation";
import { uploadMediaFile, type UploadedFile } from "@/services/file-storage";
import { apiErrorMessage, httpBlob } from "@/services/api/http";
import { buildApiUrl, resolveModelRequestConfig, type AiConfig } from "@/stores/use-config-store";

type RequestOptions = { signal?: AbortSignal };
const apiText = (key: string, options?: Record<string, unknown>) => i18n.t(`apiErrors.${key}`, options);

function aiApiUrl(config: AiConfig, path: string) {
    return buildApiUrl(config.baseUrl, path);
}

function aiHeaders(config: AiConfig) {
    return {
        Authorization: `Bearer ${config.apiKey}`,
        "Content-Type": "application/json",
    };
}

export async function requestAudioGeneration(config: AiConfig, prompt: string, options?: RequestOptions): Promise<Blob> {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.audioModel);
    const model = requestConfig.model.trim();
    const format = normalizeAudioFormatValue(config.audioFormat);
    assertAudioConfig(requestConfig, model);
    const instructions = config.audioInstructions.trim();

    try {
        const blob = await httpBlob(aiApiUrl(requestConfig, "/audio/speech"), {
            method: "POST",
            headers: aiHeaders(requestConfig),
            body: JSON.stringify({
                model,
                input: prompt,
                voice: normalizeAudioVoiceValue(config.audioVoice),
                response_format: format,
                speed: Number(normalizeAudioSpeedValue(config.audioSpeed)),
                ...(instructions ? { instructions } : {}),
            }),
            signal: options?.signal,
        });
        await assertAudioBlob(blob);
        return blob.type.startsWith("audio/") ? blob : new Blob([blob], { type: audioMimeType(format) });
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("audioGenerationFailed")));
    }
}

export async function storeGeneratedAudio(blob: Blob, format = "mp3"): Promise<UploadedFile> {
    const audio = blob.type.startsWith("audio/") ? blob : new Blob([blob], { type: audioMimeType(format) });
    return uploadMediaFile(audio, "audio");
}

function assertAudioConfig(config: AiConfig, model: string) {
    if (!model) throw new Error(apiText("audioModelRequired"));
    if (!config.baseUrl.trim()) throw new Error(apiText("baseUrlRequired"));
    if (!config.apiKey.trim()) throw new Error(apiText("apiKeyRequired"));
    if (config.apiFormat === "gemini") throw new Error(apiText("geminiAudioUnsupported"));
}

async function assertAudioBlob(blob: Blob) {
    if (!blob.type.includes("json")) return;
    let payload: { code?: number; msg?: string; error?: { message?: string } };
    try {
        payload = JSON.parse(await blob.text()) as { code?: number; msg?: string; error?: { message?: string } };
    } catch {
        return;
    }
    if (typeof payload.code === "number" && payload.code !== 0) throw new Error(payload.msg || apiText("audioGenerationFailed"));
    if (payload.error?.message) throw new Error(payload.error.message);
}
