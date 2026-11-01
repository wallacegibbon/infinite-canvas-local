import { describe, expect, it } from "vitest";

import i18n from "@/i18n";
import { defaultTitleKeyFor, nodeTitle } from "@/lib/canvas/node-title";
import { channelName, normalizeChannelModels } from "@/stores/use-config-store";

/**
 * Node titles follow the language of the reader, not the language of the session that created them.
 *
 * A canvas is saved data. Storing a translated title meant a canvas made in Chinese said "图片" in the
 * English UI forever; storing the key means the same canvas reads correctly in both.
 */
const zh = (key: string, options?: Record<string, unknown>) => i18n.t(key, { ...options, lng: "zh-CN" });
const en = (key: string, options?: Record<string, unknown>) => i18n.t(key, { ...options, lng: "en-US" });

describe("nodeTitle", () => {
    it("renders the key when there is one", () => {
        const node = { title: "图片", titleKey: "canvas.nodeTypes.image" };
        expect(nodeTitle(node, zh)).toBe("图片");
        expect(nodeTitle(node, en)).toBe("Image");
    });

    it("passes the params through, so a template title stays a template", () => {
        const node = { title: "a.png 1-1", titleKey: "canvas.runtime.splitTitle", titleParams: { name: "a.png", row: 1, column: 1 } };
        expect(nodeTitle(node, en)).toBe("a.png 1-1");
    });

    it("falls back to the stored title, which is what a user-typed name is", () => {
        expect(nodeTitle({ title: "My hero shot" }, en)).toBe("My hero shot");
    });

    it("is empty when the node has neither, so the caller can substitute its own word", () => {
        expect(nodeTitle({}, en)).toBe("");
    });
});

describe("defaultTitleKeyFor", () => {
    it("recognises a stored title in either language", () => {
        expect(defaultTitleKeyFor("音频")).toBe("canvas.nodeTypes.audio");
        expect(defaultTitleKeyFor("Audio")).toBe("canvas.nodeTypes.audio");
        expect(defaultTitleKeyFor("图片放大")).toBe("canvas.superResolve.fastTitle");
        expect(defaultTitleKeyFor("Upscale image")).toBe("canvas.superResolve.fastTitle");
    });

    it("leaves a title the user typed alone", () => {
        expect(defaultTitleKeyFor("My hero shot")).toBeNull();
        expect(defaultTitleKeyFor("a.png 1-1")).toBeNull();
        expect(defaultTitleKeyFor("")).toBeNull();
    });

    it("only matches whole titles, not a default that happens to be a prefix", () => {
        expect(defaultTitleKeyFor("图片放大 v2")).toBeNull();
    });
});

describe("channelName", () => {
    it("follows the language while the name is still the default", () => {
        expect(channelName({ name: "本地 ComfyUI", nameKey: "config.channels.localName" })).toBe(i18n.t("config.channels.localName"));
        expect(channelName({ name: "Local ComfyUI", nameKey: "config.channels.localName" })).toBe(i18n.t("config.channels.localName"));
    });

    it("shows a name the user typed as typed", () => {
        expect(channelName({ name: "My rig" })).toBe("My rig");
    });
});

describe("normalizeChannelModels", () => {
    it("keeps the capability a stored model had", () => {
        expect(normalizeChannelModels([{ name: "audio-minimax-music-3", capability: "audio" }])[0].capability).toBe("audio");
    });
});
