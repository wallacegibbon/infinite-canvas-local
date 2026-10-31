import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * Chinese copy for the resource catalogue.
 *
 * `resources/index.json` is data, so it is English (`docs/decisions.md` #8, #66, #67). The Chinese a
 * reader of the Chinese UI expects lives beside the app, keyed by the resource's stable id — the same
 * arrangement `gateway-labels.ts` uses for workflow labels. These tests pin the two directions that
 * can silently break: Chinese leaking into the English UI, and the Chinese UI falling back to English
 * because an id is not in the table.
 */
const language = { current: "zh-CN" };

vi.mock("@/i18n", () => ({
    default: {
        get resolvedLanguage() {
            return language.current;
        },
    },
}));

const { packZh, resourceZh } = await import("@/lib/resource-copy-zh");

beforeEach(() => {
    language.current = "zh-CN";
});

describe("resource-copy-zh", () => {
    it("gives the Chinese UI Chinese for a pack and a resource", () => {
        expect(packZh("core", "label")).toBe("画布与基础工作流");
        expect(packZh("h3", "description")).toContain("H3 视频生成");
        expect(resourceZh("esrgan_anime6b", "label")).toBe("RealESRGAN anime 6B（动漫放大）");
        expect(resourceZh("esrgan_x4plus", "note")).toBe("4x 快速放大；输出尺寸较大。");
        expect(resourceZh("h3_selflift_node", "license")).toBe("未声明 LICENSE");
    });

    it("gives the English UI nothing, so the index text stands", () => {
        language.current = "en-US";
        for (const [id, field] of [
            ["core", "label"],
            ["esrgan_x4plus", "note"],
            ["h3_selflift_node", "license"],
        ] as const) {
            expect(packZh(id, field)).toBe("");
            expect(resourceZh(id, field)).toBe("");
        }
    });

    it("returns nothing for an id it has never heard of", () => {
        expect(resourceZh("my_own_model", "label")).toBe("");
        expect(packZh("my_own_pack", "description")).toBe("");
    });

    it("resolves an id that contains a dot", () => {
        // `qwen3.5_9b_int8_convrot` would be read as a nested i18n path, which is why this table is a
        // plain map. If it ever moves back into the dictionaries, this is the test that fails.
        expect(resourceZh("qwen3.5_9b_int8_convrot", "label")).toBe("Qwen3.5-9B int8 convrot 文本编码器");
    });
});

describe("resources/index.json", () => {
    it("carries no Chinese copy, only real paths", async () => {
        const { readFile } = await import("node:fs/promises");
        const raw = await readFile(new URL("../../../resources/index.json", import.meta.url), "utf8");
        const doc = JSON.parse(raw) as {
            packs: Array<Record<string, unknown>>;
            resources: Array<Record<string, unknown>>;
        };

        const chinese = /[\u4e00-\u9fff]/;
        const leftovers: string[] = [];
        for (const [kind, rows] of [
            ["pack", doc.packs],
            ["resource", doc.resources],
        ] as const) {
            for (const row of rows) {
                for (const [field, value] of Object.entries(row)) {
                    if (typeof value !== "string" || !chinese.test(value)) continue;
                    // `target` is a file name on disk (an upstream model published under a Chinese
                    // path). Translating it would break the install, so it stays.
                    if (field === "target") continue;
                    leftovers.push(`${kind} ${String(row.id)}.${field} = ${value}`);
                }
            }
        }
        expect(leftovers).toEqual([]);
    });
});
