import { describe, expect, it } from "vitest";

import { withLegacyPromptFields } from "@/lib/config-migration";

/**
 * The prompt endpoint used to be called the "prompt cloud API" in the saved config. Reading a blob
 * that still says so has to produce a working endpoint: it is a rename, not a reset.
 */
describe("prompt endpoint rename", () => {
    it("reads the old field names", () => {
        const config = withLegacyPromptFields({
            promptCloudBaseUrl: "http://172.16.9.7:9999/v1",
            promptCloudApiKey: "nokey",
            promptCloudModel: "qwen3.8-27b",
            promptCloudApis: [{ id: "a1", name: "172.16.9.7", baseUrl: "http://172.16.9.7:9999/v1", apiKey: "nokey", model: "qwen3.8-27b" }],
            promptCloudApiId: "a1",
        });

        expect(config.promptBaseUrl).toBe("http://172.16.9.7:9999/v1");
        expect(config.promptApiKey).toBe("nokey");
        expect(config.promptModel).toBe("qwen3.8-27b");
        expect(config.promptEndpointId).toBe("a1");
        expect(config.promptEndpoints).toHaveLength(1);
    });

    it("drops the old names, so the next write uses the current ones only", () => {
        const migrated = withLegacyPromptFields({ promptCloudBaseUrl: "http://old", promptCloudModel: "m" }) as Record<string, unknown>;

        expect(migrated).not.toHaveProperty("promptCloudBaseUrl");
        expect(migrated).not.toHaveProperty("promptCloudModel");
        expect(migrated.promptBaseUrl).toBe("http://old");
        expect(migrated.promptModel).toBe("m");
    });

    it("keeps the current spelling when a blob carries both", () => {
        const config = withLegacyPromptFields({ promptBaseUrl: "http://new", promptCloudBaseUrl: "http://old" });

        expect(config.promptBaseUrl).toBe("http://new");
    });

    it("leaves a config with no prompt fields alone", () => {
        const config = withLegacyPromptFields({ baseUrl: "http://127.0.0.1:8190", model: "image-z-image-turbo" });

        expect(config.promptBaseUrl).toBeUndefined();
        expect(config.baseUrl).toBe("http://127.0.0.1:8190");
    });
});
