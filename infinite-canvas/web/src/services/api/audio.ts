import i18n from "@/i18n";
import { isLocalGatewayUrl } from "@/constant/app-mode";
import { audioMimeType, normalizeAudioFormatValue, normalizeAudioSpeedValue, normalizeAudioVoiceValue } from "@/lib/audio-generation";
import { uploadMediaFile, type UploadedFile } from "@/services/file-storage";
import { apiErrorMessage, httpBlob } from "@/services/api/http";
import { createRun, downloadRunOutput, waitForRun, type Run } from "@/services/api/runs";
import { buildApiUrl, resolveModelRequestConfig, type AiConfig } from "@/stores/use-config-store";

type RequestOptions = { signal?: AbortSignal };

/**
 * Hooks a canvas node needs to keep its generation alive across a reload: the run id arrives as soon
 * as the gateway accepts the job, and the progress ticks come from the run itself.
 */
type AudioRunHooks = {
    onRun?: (runId: string) => void;
    onProgress?: (run: Run) => void;
};

const apiText = (key: string, options?: Record<string, unknown>) => i18n.t(`apiErrors.${key}`, options);

/**
 * Gateway workflow ids follow `<type>-<slug>` (the registry's naming rule), so an audio workflow can
 * be recognised from its id. Anything else — a TTS channel, a proxy — keeps the speech contract.
 */
const GATEWAY_AUDIO_WORKFLOW = /^audio-/;

function aiApiUrl(config: AiConfig, path: string) {
    return buildApiUrl(config.baseUrl, path);
}

function aiHeaders(config: AiConfig) {
    return {
        Authorization: `Bearer ${config.apiKey}`,
        "Content-Type": "application/json",
    };
}

export async function requestAudioGeneration(config: AiConfig, prompt: string, options?: RequestOptions & AudioRunHooks): Promise<Blob> {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.audioModel);
    const model = requestConfig.model.trim();
    const format = normalizeAudioFormatValue(config.audioFormat);
    assertAudioConfig(requestConfig, model);
    const instructions = config.audioInstructions.trim();

    // The local gateway exposes runs, not a speech endpoint: submit the workflow and collect the
    // artifact by run id. The synchronous speech call stays for every other channel.
    if (isLocalGatewayUrl(requestConfig.baseUrl) && GATEWAY_AUDIO_WORKFLOW.test(model)) {
        return requestAudioRun(requestConfig, model, prompt, instructions, format, options);
    }

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

/**
 * One music run: the prompt is the style caption, the voice-instructions field is the lyrics.
 *
 * The chosen container is sent as-is rather than downgraded: the workflow declares which containers
 * it writes, and an unsupported one comes back as a message naming the accepted set — a silent
 * substitution would leave the node labelled with a format it does not hold.
 */
async function requestAudioRun(config: AiConfig, model: string, prompt: string, lyrics: string, format: string, options?: RequestOptions & AudioRunHooks): Promise<Blob> {
    const inputs: Record<string, unknown> = { prompt, format };
    if (lyrics) inputs.lyrics = lyrics;

    try {
        const run = await createRun(config, { workflow: model, inputs });
        options?.onRun?.(run.id);
        const finished = await waitForRun(config, run.id, {
            signal: options?.signal,
            onProgress: options?.onProgress,
            // The user asked for this: stopping it should free the GPU, not leave it rendering.
            cancelOnAbort: true,
        });
        const blob = await downloadRunOutput(config, finished, 0, { signal: options?.signal });
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
