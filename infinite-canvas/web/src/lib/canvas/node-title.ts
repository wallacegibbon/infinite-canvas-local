import i18n from "@/i18n";

/**
 * A node's title, in the language the reader is using.
 *
 * A canvas is saved data, and a title used to be saved *translated*: creating an image node in the
 * Chinese UI wrote "图片" into the node, so the same canvas showed "图片" in the English UI forever.
 * The fix is the one the rest of the app already uses for shipped text: store the identifier, render
 * the translation. `titleKey` is that identifier, `titleParams` are its values, and `title` stays as
 * the snapshot — a filename, an export, or a node the user renamed.
 */

/**
 * Titles the app generates from a translation key.
 *
 * The migration below needs this list to recognise a stored string as "a default title, translated",
 * so it must name every key the app puts straight into `node.title`. A new one belongs here; the test
 * fails when a stored default goes unrecognised.
 */
const DEFAULT_TITLE_KEYS = [
    "canvas.nodeTypes.image",
    "canvas.nodeTypes.text",
    "canvas.nodeTypes.video",
    "canvas.nodeTypes.audio",
    "canvas.nodeTypes.config",
    "canvas.node.group",
    "canvas.runtime.maskNodeTitle",
    "canvas.runtime.maskResult",
    "canvas.runtime.reverseTitle",
    "canvas.runtime.cropNodeTitle",
    "canvas.outpaint.resultTitle",
    "canvas.mossLayout.resultTitle",
    "canvas.videoUpscale.resultTitle",
    "canvas.nodeToolbar.interpolateTitle",
    "canvas.superResolve.title",
    "canvas.superResolve.fastTitle",
] as const;

type TitleSource = { title?: string; titleKey?: string; titleParams?: Record<string, string | number> };

type Translate = (key: string, options?: Record<string, string | number>) => string;

/**
 * The title to render for a node.
 *
 * A key wins, so a canvas created in one language reads correctly in the other. Without one the
 * stored title is the answer — that is what a user-typed name looks like, and what any node created
 * before the title became a key carries.
 */
export function nodeTitle(node: TitleSource, t: Translate) {
    if (node.titleKey) return t(node.titleKey, node.titleParams);
    return node.title || "";
}

/**
 * The key a stored title was rendered from, when it was one of the app's own defaults.
 *
 * Used by the canvas migration: a node saved as "音频" (or "Audio") gets `titleKey:
 * "canvas.nodeTypes.audio"` so it follows the language from now on. A user-typed title matches
 * nothing and keeps its text.
 */
export function defaultTitleKeyFor(title: string): string | null {
    const value = title.trim();
    if (!value) return null;
    for (const key of DEFAULT_TITLE_KEYS) {
        // Both dictionaries, because the stored string came from whichever language was active.
        // `lng` rather than `getFixedT`: the fallback dictionary is zh-CN, so a plain `t()` would
        // answer in Chinese for both languages and never recognise an English snapshot.
        for (const language of ["zh-CN", "en-US"]) {
            if (i18n.t(key, { lng: language }) === value) return key;
        }
    }
    return null;
}
