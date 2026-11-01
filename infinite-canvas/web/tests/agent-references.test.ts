import { describe, expect, it } from "vitest";

import { referenceBlock, referenceLine, withoutReferences } from "@/lib/canvas/agent-references";

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

    it("builds no block when nothing is cited", () => {
        expect(referenceBlock("【画布引用】", [])).toBe("");
    });
});
