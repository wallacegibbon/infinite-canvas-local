import { requestAgentOnce } from "@/services/api/agent";
import type { AiConfig } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";

/** The H3 task types, as the skill names them. */
export type PromptOptimizeMode = "全参考模式(Reference to Video)" | "文生视频(T2VA)" | "首帧图生视频(I2VA)" | "首尾帧视频(FL2VA)" | "尾帧图生视频(L2VA)";

/**
 * Ask the agent for words: one prompt in, one answer out, read as it arrives.
 *
 * The model and the reasoning level are the agent's own settings, not fields of a prompt — they are
 * changed with `command` (see requestAgentChat), never attached to a question.
 */
export async function requestPromptChat(config: AiConfig, params: { prompt: string; images?: ReferenceImage[] }, options?: { signal?: AbortSignal; onDelta?: (text: string) => void }): Promise<string> {
    let streamed = "";
    const answer = await requestAgentOnce(
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
