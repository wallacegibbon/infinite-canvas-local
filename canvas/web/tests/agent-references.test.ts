import { describe, expect, it } from "vitest";

import { referenceBlock, referenceEntries, referenceLine, withoutReferences } from "@/lib/canvas/agent-references";

/**
 * The block is plumbing that the agent needs and the reader does not. These check the two directions
 * against each other: whatever the canvas puts in front of a prompt is exactly what comes off again.
 */

describe("canvas references", () => {
    it("takes the block it built off again, in either language", () => {
        const lines = [referenceLine("lPiENyy6iXVTwtTVDzd7M", "image", "补全背景", "补全背景")];
        for (const marker of ["【画布引用】", "[Canvas references]"]) {
            expect(withoutReferences(referenceBlock(marker, lines) + "@补全背景 描述一下这张图片的背景。")).toBe("@补全背景 描述一下这张图片的背景。");
        }
    });

    it("takes off a block written in the other language than the UI is in now", () => {
        const prompt = referenceBlock("【画布引用】", ["- n1 (image) 图: 一只猫"]) + "再生成一张";
        expect(withoutReferences(prompt)).toBe("再生成一张");
    });

    it("leaves a prompt that never had a block exactly as it is", () => {
        expect(withoutReferences("@补全背景 描述一下这张图片的背景。")).toBe("@补全背景 描述一下这张图片的背景。");
        expect(withoutReferences("look at 【画布引用】 in the middle")).toBe("look at 【画布引用】 in the middle");
    });

    it("leaves a marker without its blank line alone rather than guessing where the words start", () => {
        const cut = "【画布引用】\n- n1 (image) x: y\n没有空行的一句话";
        expect(withoutReferences(cut)).toBe(cut);
    });

    it("reads the cited nodes back, so a mention can be a link", () => {
        const block = referenceBlock("【画布引用】", [referenceLine("lPiENyy6iXVTwtTVDzd7M", "image", "补全背景", "补全背景"), referenceLine("n2", "text", "第二章的开头", "正文")]);
        expect(referenceEntries(block + "@补全背景 描述一下")).toEqual([
            { id: "lPiENyy6iXVTwtTVDzd7M", title: "补全背景" },
            { id: "n2", title: "第二章的开头" },
        ]);
        expect(referenceEntries("没有引用块的一句话")).toEqual([]);
        // A title containing the separator costs the tail of the title in the match, never the id: the line is
        // still readable, and the link still knows which node it points at.
        expect(referenceEntries(referenceBlock("【画布引用】", [referenceLine("n3", "text", "第二章: 开头", "正文")]))).toEqual([{ id: "n3", title: "第二章" }]);
    });

    it("builds no block when nothing is cited", () => {
        expect(referenceBlock("【画布引用】", [])).toBe("");
    });
});
