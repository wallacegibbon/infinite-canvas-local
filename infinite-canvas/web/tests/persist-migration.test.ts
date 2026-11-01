import { describe, expect, it } from "vitest";

import { CANVAS_PERSIST_VERSION, readCanvasPersisted } from "@/stores/canvas/canvas-persist";
import { ASSET_PERSIST_VERSION, readAssetPersisted } from "@/stores/asset-persist";

/**
 * These feed blobs to `readCanvasPersisted` / `readAssetPersisted`, which is exactly what the
 * storage adapters call. A throw there is not an exception to catch — it is the signal that turns
 * into "copy the blob aside, refuse to write, show the banner", so most of these assert `toThrow`.
 */

const project = (overrides: Record<string, unknown> = {}) => ({
    id: "P1",
    title: "Canvas",
    createdAt: "2026-01-01T00:00:00.000Z",
    updatedAt: "2026-01-01T00:00:00.000Z",
    nodes: [],
    connections: [],
    backgroundMode: "lines",
    showImageInfo: false,
    viewport: { x: 10, y: 20, k: 0.5 },
    ...overrides,
});

describe("canvas store format", () => {
    it("reads a current-version blob unchanged", () => {
        const raw = JSON.stringify({ version: CANVAS_PERSIST_VERSION, state: { projects: [project()], deletedProjects: [], lastProjectId: "P1" } });
        const state = readCanvasPersisted(raw);
        expect(state.projects).toHaveLength(1);
        expect(state.lastProjectId).toBe("P1");
        expect(state.projects[0].viewport).toEqual({ x: 10, y: 20, k: 0.5 });
    });

    it("backfills lastProjectId, which older blobs never had", () => {
        const raw = JSON.stringify({ version: 0, state: { projects: [project()], deletedProjects: [] } });
        expect(readCanvasPersisted(raw).lastProjectId).toBe("");
    });

    it("backfills the per-project fields older blobs never had", () => {
        const raw = JSON.stringify({
            version: 0,
            state: { projects: [{ id: "P1", title: "Canvas", nodes: [], connections: [], viewport: { x: 1, y: 2, k: 1 } }] },
        });
        const [only] = readCanvasPersisted(raw).projects;
        expect(only.backgroundMode).toBe("lines");
        expect(only.showImageInfo).toBe(false);
    });

    it("drops the abandoned chat fields a format-3 blob still carries", () => {
        const raw = JSON.stringify({
            version: 3,
            state: {
                projects: [{ ...project(), chatSessions: [{ id: "chat-1", title: "old", messages: [] }], activeChatId: "chat-1" }],
                deletedProjects: [],
                lastProjectId: "P1",
            },
        });
        const [only] = readCanvasPersisted(raw).projects;
        expect("chatSessions" in only).toBe(false);
        expect("activeChatId" in only).toBe(false);
        // Everything the canvas does use survives the drop.
        expect(only.nodes).toEqual([]);
        expect(only.viewport).toEqual({ x: 10, y: 20, k: 0.5 });
    });

    it("repairs a viewport with no zoom, which would otherwise be NaN across the canvas", () => {
        const raw = JSON.stringify({ version: 0, state: { projects: [project({ viewport: { x: 5, y: 6 } })], deletedProjects: [] } });
        expect(readCanvasPersisted(raw).projects[0].viewport).toEqual({ x: 5, y: 6, k: 1 });
    });

    it("treats a zero or negative zoom as unset", () => {
        const raw = JSON.stringify({ version: 0, state: { projects: [project({ viewport: { x: 0, y: 0, k: 0 } })], deletedProjects: [] } });
        expect(readCanvasPersisted(raw).projects[0].viewport.k).toBe(1);
    });

    it("turns a stored translated title back into its key", () => {
        const raw = JSON.stringify({
            version: 1,
            state: {
                projects: [
                    project({
                        nodes: [
                            { id: "n1", type: "audio", title: "音频", position: { x: 0, y: 0 }, width: 340, height: 120 },
                            { id: "n2", type: "image", title: "My hero shot", position: { x: 0, y: 0 }, width: 340, height: 240 },
                            { id: "n3", type: "image", title: "Image", titleKey: "canvas.superResolve.fastTitle", position: { x: 0, y: 0 }, width: 340, height: 240 },
                        ],
                    }),
                ],
                deletedProjects: [],
            },
        });
        const [only] = readCanvasPersisted(raw).projects;
        // A default title was saved already translated; it becomes a key so it follows the language.
        expect(only.nodes[0]).toMatchObject({ title: "音频", titleKey: "canvas.nodeTypes.audio" });
        // A title someone typed stays exactly as typed.
        expect(only.nodes[1]).toMatchObject({ title: "My hero shot" });
        expect(only.nodes[1].titleKey).toBeUndefined();
        // An explicit key is never overwritten.
        expect(only.nodes[2].titleKey).toBe("canvas.superResolve.fastTitle");
    });

    it("drops editCanvasRatio, which the gateway refuses as an input", () => {
        const raw = JSON.stringify({
            version: 2,
            state: {
                projects: [
                    project({
                        nodes: [
                            {
                                id: "n1",
                                type: "image",
                                title: "Shot",
                                position: { x: 0, y: 0 },
                                width: 340,
                                height: 240,
                                metadata: { prompt: "a kettle", params: { editCanvasRatio: "16:9", editCanvasSize: "1792x1024", resolution: 1024, seed: 7 } },
                            },
                            { id: "n2", type: "image", title: "Other", position: { x: 0, y: 0 }, width: 340, height: 240 },
                        ],
                    }),
                ],
                deletedProjects: [],
            },
        });
        const [only] = readCanvasPersisted(raw).projects;
        // The workflow's own inputs are untouched, including the size the pill is read back from.
        expect(only.nodes[0].metadata.params).toEqual({ editCanvasSize: "1792x1024", resolution: 1024, seed: 7 });
        // A node with nothing stored is left exactly as it was.
        expect(only.nodes[1]).toMatchObject({ id: "n2", title: "Other" });
    });

    it("refuses a blob written by a newer build rather than guessing", () => {
        const raw = JSON.stringify({ version: CANVAS_PERSIST_VERSION + 1, state: { projects: [] } });
        expect(() => readCanvasPersisted(raw)).toThrow(/newer|format version/);
    });

    it("refuses invalid JSON", () => {
        expect(() => readCanvasPersisted("{oops truncated")).toThrow();
    });

    it("refuses a project whose nodes are not an array", () => {
        const raw = JSON.stringify({ version: CANVAS_PERSIST_VERSION, state: { projects: [project({ nodes: "nope" })] } });
        expect(() => readCanvasPersisted(raw)).toThrow(/nodes/);
    });

    it("refuses a project with no id", () => {
        const raw = JSON.stringify({ version: CANVAS_PERSIST_VERSION, state: { projects: [project({ id: "" })] } });
        expect(() => readCanvasPersisted(raw)).toThrow(/id/);
    });
});

describe("asset store format", () => {
    const image = (overrides: Record<string, unknown> = {}) => ({
        id: "A1",
        kind: "image",
        title: "Asset",
        coverUrl: "",
        tags: [],
        createdAt: "2026-01-01T00:00:00.000Z",
        updatedAt: "2026-01-01T00:00:00.000Z",
        data: { dataUrl: "blob:x", width: 1, height: 1, bytes: 1, mimeType: "image/png" },
        ...overrides,
    });

    it("reads a current-version blob unchanged", () => {
        const raw = JSON.stringify({ version: ASSET_PERSIST_VERSION, state: { assets: [image()] } });
        expect(readAssetPersisted(raw).assets).toHaveLength(1);
    });

    it("migrates a versionless blob", () => {
        const raw = JSON.stringify({ state: { assets: [image()] } });
        expect(readAssetPersisted(raw).assets).toHaveLength(1);
    });

    it("drops entries with no data, which the hydration pass would dereference", () => {
        const raw = JSON.stringify({ version: 0, state: { assets: [image(), { id: "A2", kind: "image", title: "broken", tags: [] }] } });
        const assets = readAssetPersisted(raw).assets;
        expect(assets).toHaveLength(1);
        expect(assets[0].id).toBe("A1");
    });

    it("refuses a blob written by a newer build", () => {
        const raw = JSON.stringify({ version: ASSET_PERSIST_VERSION + 1, state: { assets: [] } });
        expect(() => readAssetPersisted(raw)).toThrow(/format version/);
    });

    it("refuses assets that are not an array", () => {
        const raw = JSON.stringify({ version: ASSET_PERSIST_VERSION, state: { assets: {} } });
        expect(() => readAssetPersisted(raw)).toThrow(/assets/);
    });
});
