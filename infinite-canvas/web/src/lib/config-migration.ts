import type { AiConfig } from "@/stores/use-config-store";

/**
 * Config written by an older build, read as the current one.
 *
 * Decision 74 renamed the prompt endpoint from "prompt cloud API" to "prompt endpoint": the saved
 * config used to say `promptCloudBaseUrl` / `promptCloudApiKey` / `promptCloudModel` /
 * `promptCloudApis` / `promptCloudApiId`. The rename is not worth losing a working endpoint over, so
 * the old spellings are read once here and never written back.
 */
const RENAMED_PROMPT_FIELDS: Array<[current: string, legacy: string]> = [
    ["promptBaseUrl", "promptCloudBaseUrl"],
    ["promptApiKey", "promptCloudApiKey"],
    ["promptModel", "promptCloudModel"],
    ["promptEndpoints", "promptCloudApis"],
    ["promptEndpointId", "promptCloudApiId"],
];

export function withLegacyPromptFields(stored: Record<string, unknown>): Partial<AiConfig> {
    const migrated: Record<string, unknown> = { ...stored };
    for (const [current, legacy] of RENAMED_PROMPT_FIELDS) {
        if (migrated[current] === undefined && migrated[legacy] !== undefined) migrated[current] = migrated[legacy];
        // A value that was read is not kept: the next write is in the current spelling only.
        delete migrated[legacy];
    }
    return migrated as Partial<AiConfig>;
}
