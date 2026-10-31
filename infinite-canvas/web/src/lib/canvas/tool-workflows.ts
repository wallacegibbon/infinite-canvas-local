/**
 * Workflows the canvas invokes as a node action instead of offering as a model.
 *
 * This list is a **presentation choice and it lives in the client**, because the canvas is the only
 * client. The gateway registers a workflow and reports whether it can run; whether it belongs in the
 * model dropdown is ours to decide, and every id here has a button or a dialog that submits it with
 * the media it actually needs — an upscaler takes a clip, not a prompt.
 *
 * Keeping the ids in one place means a tool cannot drift: the same constant names the button, builds
 * the request, and filters the model list.
 */
export const VIDEO_UPSCALE_WORKFLOW = "video-seedvr2-upscale";
export const VIDEO_INTERPOLATE_WORKFLOW = "video-rife";
export const IMAGE_UPSCALE_WORKFLOW = "image-esrgan-upscale";
export const IMAGE_ANGLE_WORKFLOW = "image-qwen21-angle";

const TOOL_WORKFLOWS: readonly string[] = [VIDEO_UPSCALE_WORKFLOW, VIDEO_INTERPOLATE_WORKFLOW, IMAGE_UPSCALE_WORKFLOW, IMAGE_ANGLE_WORKFLOW];

/** Whether a workflow is one the canvas drives from an action rather than from the model list. */
export function isToolWorkflow(id: string) {
    return TOOL_WORKFLOWS.includes(id);
}
