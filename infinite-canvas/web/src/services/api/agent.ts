import i18n from "@/i18n";
import { dataUrlToFile } from "@/lib/image-utils";
import { localGatewayRoot } from "@/services/api/image";
import { apiErrorMessage } from "@/services/api/http";
import { imageToDataUrl } from "@/services/image-storage";
import type { AiConfig } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";

const apiText = (key: string, options?: Record<string, unknown>) => i18n.t(`apiErrors.${key}`, options);

/**
 * The agent, as one stream.
 *
 * A prompt and a command are the same thing to AlayaCore — frames on its stdin — so there is one
 * request: what we send is written to it (`command` + `args`, or `prompt` + images), and what it says
 * comes back here. This file only transcribes; it decides nothing about the content.
 */
type AgentModel = { id: number; name: string; protocolType?: string; contextLimit?: number };

type AgentEvent = {
    type: "models" | "delta" | "reasoning" | "model" | "reasoning_level" | "command" | "done" | "error";
    text?: string;
    models?: AgentModel[];
    active?: AgentModel;
    level?: number;
    coreVersion?: string;
    ok?: boolean;
    message?: string;
};

type AgentRequest = {
    prompt?: string;
    images?: ReferenceImage[];
    /** One of AlayaCore's own command names, forwarded verbatim. */
    command?: string;
    args?: string;
};

/**
 * Run one agent request and read its stream.
 *
 * Returns the answer text as the deltas assembled it (empty for a command), and reports every event
 * so a caller can render the model list, the current model, or a refusal as they arrive.
 */
export async function requestAgentStream(config: AiConfig, request: AgentRequest, options?: { signal?: AbortSignal; onEvent?: (event: AgentEvent) => void }): Promise<string> {
    const baseUrl = String(config.baseUrl || "").trim();
    const root = localGatewayRoot(baseUrl);
    if (!root) throw new Error(apiText("promptOptimizeLocalOnly"));

    const formData = new FormData();
    if (request.prompt) formData.set("prompt", request.prompt);
    if (request.command) formData.set("command", request.command);
    if (request.args !== undefined) formData.set("args", request.args);
    const files = await Promise.all((request.images || []).map(async (image) => dataUrlToFile({ ...image, dataUrl: await imageToDataUrl(image) })));
    files.forEach((file, index) => formData.append(index === 0 ? "image" : `image${index + 1}`, file));

    let answer = "";
    let failure = "";
    try {
        const response = await fetch(`${root}/api/agent/chat`, { method: "POST", headers: { Authorization: `Bearer ${config.apiKey}` }, body: formData, signal: options?.signal });
        if (!response.ok) throw new Error(apiText("requestFailed"));
        if (!response.body) throw new Error(apiText("requestFailed"));

        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        for (;;) {
            const { done, value } = await reader.read();
            if (done) break;
            buffer += decoder.decode(value, { stream: true });
            // EventSource framing: one `data: {...}` per line, a blank line between events.
            for (;;) {
                const index = buffer.indexOf("\n\n");
                if (index < 0) break;
                const block = buffer.slice(0, index);
                buffer = buffer.slice(index + 2);
                const payload = block.replace(/^data: /, "").trim();
                if (!payload) continue;
                const event = JSON.parse(payload) as AgentEvent;
                if (event.type === "delta") answer += event.text || "";
                // The agent's own sentence is the failure: relaying it is all this layer does.
                if (event.type === "error" && event.message) failure = event.message;
                options?.onEvent?.(event);
            }
        }
        if (!answer && failure) throw new Error(failure);
        return answer;
    } catch (error) {
        throw new Error(apiErrorMessage(error, apiText("requestFailed"), "imageTimeout"));
    }
}
