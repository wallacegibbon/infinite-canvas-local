import i18n from "@/i18n";
import { audioMimeType, normalizeAudioFormatValue } from "@/lib/audio-generation";
import { uploadMediaFile, type UploadedFile } from "@/services/file-storage";
import { apiErrorMessage } from "@/services/api/http";
import { createRun, downloadRunOutput, waitForRun, type Run } from "@/services/api/runs";
import { resolveModelRequestConfig, type AiConfig } from "@/stores/use-config-store";

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
 * One music run: the prompt is the style caption, the voice-instructions field the lyrics.
 *
 * The chosen container is sent as-is rather than downgraded: the workflow declares which containers
 * it writes, and an unsupported one comes back as a message naming the accepted set — a silent
 * substitution would leave the node labelled with a format it does not hold.
 */
export async function requestAudioGeneration(config: AiConfig, prompt: string, options?: RequestOptions & AudioRunHooks): Promise<Blob> {
    const requestConfig = resolveModelRequestConfig(config, config.model || config.audioModel);
    const model = requestConfig.model.trim();
    const format = normalizeAudioFormatValue(config.audioFormat);
    assertAudioConfig(requestConfig, model);
    const lyrics = config.audioInstructions.trim();

    const inputs: Record<string, unknown> = { prompt, format };
    if (lyrics) inputs.lyrics = lyrics;
    try {
        const run = await createRun(requestConfig, { workflow: model, inputs });
        options?.onRun?.(run.id);
        const finished = await waitForRun(requestConfig, run.id, {
            signal: options?.signal,
            onProgress: options?.onProgress,
            // The user asked for this: stopping it should free the GPU, not leave it rendering.
            cancelOnAbort: true,
        });
        const blob = await downloadRunOutput(requestConfig, finished, 0, { signal: options?.signal });
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
}
