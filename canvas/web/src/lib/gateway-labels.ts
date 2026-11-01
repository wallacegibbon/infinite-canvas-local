import i18n from "@/i18n";

// Workflow metadata comes from workflows/registry.json, which is written in English because it
// is data rather than UI copy (docs/decisions.md #8). Chinese for the workflows that ship with
// this repo therefore lives in the dictionary, keyed by the identifier the gateway already
// sends — a model id, a variant mode — and anything the dictionary does not know falls back to
// the English text, so a hand-written workflow still renders instead of showing a raw key.
//
// The lookup is explicit about the language: the i18n setup falls back to `zh-CN`, so a plain
// `t()` would answer with Chinese in the English UI too.
function chinese(key: string): string {
    if (i18n.resolvedLanguage !== "zh-CN") return "";
    const text = i18n.getFixedT("zh-CN")(key, { defaultValue: "" });
    return text === key ? "" : text;
}

export function gatewayModelLabel(id: string, fallback?: string) {
    return chinese(`gateway.models.${id}.label`) || fallback || id;
}

export function gatewayVariantLabel(mode: string, fallback?: string) {
    return chinese(`gateway.variants.${mode}`) || fallback || mode;
}
