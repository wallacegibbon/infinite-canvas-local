import { describe, expect, it } from "vitest";

import { IMAGE_ANGLE_WORKFLOW, IMAGE_UPSCALE_WORKFLOW, isToolWorkflow, VIDEO_INTERPOLATE_WORKFLOW } from "@/lib/canvas/tool-workflows";

/**
 * Which workflows are actions rather than models.
 *
 * The gateway registers a workflow and reports whether it can run; whether it belongs in the model
 * dropdown is the client's decision, because the canvas is the only client. Every id listed here has
 * a button that supplies the media the workflow really needs.
 */
describe("tool workflows", () => {
    it("are the ones the canvas drives from an action", () => {
        for (const id of [VIDEO_INTERPOLATE_WORKFLOW, IMAGE_UPSCALE_WORKFLOW, IMAGE_ANGLE_WORKFLOW]) {
            expect(isToolWorkflow(id)).toBe(true);
        }
    });

    it("do not include the workflows that are picked from the model list", () => {
        for (const id of ["video-h3-frames", "video-h3-t2v", "image-qwen21", "image-z-image-turbo", "audio-minimax-music-3"]) {
            expect(isToolWorkflow(id)).toBe(false);
        }
    });
});
