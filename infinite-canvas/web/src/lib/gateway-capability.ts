import { guessCapability, type ModelCapability } from "@/stores/use-config-store";

const CAPABILITIES: ModelCapability[] = ["image", "video", "audio", "text"];

/**
 * Which capability a gateway model has.
 *
 * The gateway declares it in `/v1/models` metadata (`type`), and its workflow ids are
 * `<type>-<slug>` by construction. Both are authoritative; the name heuristic is only a fallback for
 * a non-gateway endpoint, and it deliberately refuses `text` so an unrelated model cannot become the
 * default text model by accident. `audio` is accepted from a name because its keywords are explicit
 * ("music", "tts", "voice", …) and audio workflows were the ones being lost.
 *
 * The reason this exists as one function: the local-gateway sync used to accept only image and video,
 * so every audio workflow was dropped from the channel and could never be selected — the model
 * existed, the picker stayed empty.
 */
export function gatewayModelCapability(name: string, declaredType?: string): ModelCapability | null {
    const trimmed = name.trim();
    if (!trimmed) return null;
    const declared = (declaredType || "").trim().toLowerCase();
    if (CAPABILITIES.includes(declared as ModelCapability)) return declared as ModelCapability;
    const prefix = trimmed.split("-", 1)[0].toLowerCase();
    if (CAPABILITIES.includes(prefix as ModelCapability)) return prefix as ModelCapability;
    const guessed = guessCapability(trimmed);
    return guessed === "image" || guessed === "video" || guessed === "audio" ? guessed : null;
}
