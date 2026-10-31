import { describe, expect, it, vi } from "vitest";

/**
 * Which nodes survive a reload in the `loading` state.
 *
 * A video task and an audio run both live on the gateway, so a node waiting on one must not be
 * marked "interrupted" by the restore pass — that is what the resume effect depends on.
 */
vi.mock("@/i18n", () => ({ default: { t: (key: string) => key } }));

const { hasResumableAudioRun, hasResumableVideoTask, resetInterruptedGeneration } = await import("@/lib/canvas/canvas-generation-helpers");

type Node = Parameters<typeof hasResumableAudioRun>[0];
const node = (overrides: Record<string, unknown>): Node =>
    ({
        id: "n1",
        type: "audio",
        title: "n1",
        position: { x: 0, y: 0 },
        width: 100,
        height: 100,
        metadata: { status: "loading" },
        ...overrides,
    }) as unknown as Node;

describe("hasResumableAudioRun", () => {
    it("is true for a loading audio node with a run id and no content", () => {
        expect(hasResumableAudioRun(node({ metadata: { status: "loading", audioRunId: "task_1" } }))).toBe(true);
    });

    it("is false once the audio exists", () => {
        expect(hasResumableAudioRun(node({ metadata: { status: "loading", audioRunId: "task_1", content: "blob:1" } }))).toBe(false);
    });

    it("is false for an error node, even if it kept the id", () => {
        expect(hasResumableAudioRun(node({ metadata: { status: "error", audioRunId: "task_1" } }))).toBe(false);
    });

    it("is false for other node types", () => {
        expect(hasResumableAudioRun(node({ type: "image", metadata: { status: "loading", audioRunId: "task_1" } }))).toBe(false);
    });
});

describe("resetInterruptedGeneration", () => {
    it("keeps a loading node whose audio run can be resumed", () => {
        const input = [node({ metadata: { status: "loading", audioRunId: "task_1" } })];
        expect(resetInterruptedGeneration(input)[0].metadata?.status).toBe("loading");
    });

    it("keeps a loading node whose video task can be resumed", () => {
        const input = [node({ type: "video", metadata: { status: "loading", videoTaskId: "task_2" } })];
        expect(hasResumableVideoTask(input[0])).toBe(true);
        expect(resetInterruptedGeneration(input)[0].metadata?.status).toBe("loading");
    });

    it("marks a loading node with no run as interrupted", () => {
        const input = [node({ metadata: { status: "loading" } })];
        const [restored] = resetInterruptedGeneration(input);
        expect(restored.metadata?.status).toBe("error");
        expect(restored.metadata?.errorDetails).toBe("canvas.generation.interrupted");
    });

    it("leaves nodes that are not loading alone", () => {
        const input = [node({ metadata: { status: "success", audioRunId: "task_1" } })];
        expect(resetInterruptedGeneration(input)[0].metadata?.status).toBe("success");
    });
});
