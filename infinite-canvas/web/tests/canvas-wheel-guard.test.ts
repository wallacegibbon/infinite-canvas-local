import { describe, expect, it } from "vitest";

import { CANVAS_CLICK_GUARD, CANVAS_WHEEL_GUARD } from "@/lib/canvas-overlay";

/**
 * The canvas intercepts every wheel (it zooms) and cancels the browser's default, which is what
 * scrolls an element. Content that scrolls itself therefore has to opt out of the *wheel* guard —
 * and only of that one: a click inside a text node must still select it.
 */
describe("canvas wheel guard", () => {
    it("lets self-scrolling content keep the wheel", () => {
        expect(CANVAS_WHEEL_GUARD).toContain("[data-canvas-scroll]");
    });

    it("keeps self-scrolling content clickable", () => {
        // Attaching the wheel opt-out to the click guard would swallow the click that starts editing.
        expect(CANVAS_CLICK_GUARD).not.toContain("[data-canvas-scroll]");
    });

    it("still lets overlays and opted-out surfaces keep both", () => {
        for (const guard of [CANVAS_WHEEL_GUARD, CANVAS_CLICK_GUARD]) {
            expect(guard).toContain("[data-canvas-overlay]");
            expect(guard).toContain("[data-canvas-no-zoom]");
        }
    });
});
