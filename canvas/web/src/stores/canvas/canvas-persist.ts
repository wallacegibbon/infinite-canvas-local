import type { ViewportTransform } from "@/types/canvas";
import type { CanvasBackgroundMode } from "@/lib/canvas-theme";
import { defaultTitleKeyFor } from "@/lib/canvas/node-title";
import type { CanvasProject } from "@/stores/canvas/use-canvas-store";

/**
 * The on-disk format of the canvas store, and the rules for reading it back.
 *
 * zustand's own `version`/`migrate` is left unused on purpose. It runs *after* `getItem`, so a
 * migration that fails there cannot reuse the recovery this app already has for unreadable blobs —
 * it would just log and boot empty. Doing the version check and the migration inside `getItem`
 * means one failure path for everything: a blob that cannot be brought to the current shape is
 * handed to `preserveUnreadable`, which copies it aside, refuses further writes, and tells the
 * user. `version` is still set on the store so that what we *write* is labelled.
 */
export const CANVAS_PERSIST_VERSION = 4;

type PersistedCanvasState = {
    projects: CanvasProject[];
    deletedProjects: { id: string; deletedAt: string }[];
    lastProjectId: string;
};

const DEFAULT_VIEWPORT: ViewportTransform = { x: 0, y: 0, k: 1 };

/**
 * Parse a stored blob into the current shape, or throw.
 *
 * Throwing is the contract: the caller turns a throw into "preserve it and stop writing", which is
 * the only outcome that cannot lose data.
 */
export function readCanvasPersisted(raw: string): PersistedCanvasState {
    const parsed = asRecord(JSON.parse(raw));
    const version = typeof parsed.version === "number" ? parsed.version : 0;
    if (version > CANVAS_PERSIST_VERSION) {
        // Written by a newer build. We cannot know what changed, so we must not guess.
        throw new Error(`stored with format version ${version}, this build reads up to ${CANVAS_PERSIST_VERSION}`);
    }
    const state = version < CANVAS_PERSIST_VERSION ? migrateCanvasState(parsed.state, version) : parsed.state;
    return assertCanvasState(state);
}

/**
 * Bring an older blob up to the current shape.
 *
 * The merge zustand performs is shallow at the top level, so anything inside `projects[]` is taken
 * from disk verbatim. That is where an added field quietly becomes `undefined`, and where a
 * changed field quietly becomes the wrong type — hence a per-project pass rather than a `{...}`
 * over the array.
 */
function migrateCanvasState(state: unknown, from: number) {
    if (from !== 0 && from !== 1 && from !== 2 && from !== 3) throw new Error(`no migration from format version ${from}`);
    const source = asRecord(state);
    const projects = Array.isArray(source.projects) ? source.projects : [];
    return {
        projects: projects.map(backfillProject),
        deletedProjects: Array.isArray(source.deletedProjects) ? source.deletedProjects : [],
        // Introduced with the canvas list's last-visited memory; older blobs do not have it.
        lastProjectId: typeof source.lastProjectId === "string" ? source.lastProjectId : "",
    };
}

function backfillProject(raw: unknown): unknown {
    const project = asRecord(raw);
    return {
        ...withoutRetiredProjectFields(project),
        backgroundMode: typeof project.backgroundMode === "string" ? (project.backgroundMode as CanvasBackgroundMode) : "lines",
        showImageInfo: typeof project.showImageInfo === "boolean" ? project.showImageInfo : false,
        viewport: backfillViewport(project.viewport),
        nodes: Array.isArray(project.nodes) ? project.nodes.map(backfillNode) : project.nodes,
    };
}

function backfillNode(raw: unknown): unknown {
    return withoutUiOnlyParams(backfillNodeTitle(raw));
}

/**
 * Drop the fields the abandoned canvas-chat model left in every stored project (format 3 → 4).
 *
 * `chatSessions` and `activeChatId` described a chat whose messages the canvas kept itself. The agent
 * panel does not keep them any more — the conversation is the agent's session file — so nothing ever wrote
 * a message, and the two keys only travelled: persisted on every edit, restored on every open, carried
 * through the undo history. Removing them here is the whole migration; a canvas saved by an older build is
 * repaired the next time it is read.
 */
function withoutRetiredProjectFields(project: Record<string, unknown>): Record<string, unknown> {
    if (!("chatSessions" in project) && !("activeChatId" in project)) return project;
    const { chatSessions: _sessions, activeChatId: _active, ...rest } = project;
    return rest;
}

/**
 * Delete `editCanvasRatio` from a node's params (format 2 → 3).
 *
 * It was UI state — which ratio pill is lit — saved next to the workflow's own inputs and sent with every
 * request. The gateway refuses an input the workflow does not accept, so every edit run from such a node
 * failed with `unknown input "editCanvasRatio"`. The pill is read back from `editCanvasSize` now, so the
 * key has no meaning left; dropping it here is what repairs canvases saved before that.
 */
function withoutUiOnlyParams(raw: unknown): unknown {
    const node = asRecord(raw);
    const metadata = asRecord(node.metadata);
    const params = metadata.params;
    if (!params || typeof params !== "object") return raw;
    const stored = asRecord(params);
    if (!("editCanvasRatio" in stored)) return raw;
    const { editCanvasRatio: _dropped, ...rest } = stored;
    return { ...node, metadata: { ...metadata, params: rest } };
}

/**
 * Give a stored default title back its key (format 1 → 2).
 *
 * Titles used to be saved already translated, so a canvas written in Chinese said "图片" in the English
 * UI. A title that matches one of the app's own defaults in either language becomes a `titleKey`, which
 * the renderer translates; `title` keeps the stored text as the snapshot a filename or export uses.
 * Anything a user typed matches nothing and is left exactly as it is.
 */
function backfillNodeTitle(raw: unknown): unknown {
    const node = asRecord(raw);
    if (node.titleKey || typeof node.title !== "string") return raw;
    const key = defaultTitleKeyFor(node.title);
    return key ? { ...node, titleKey: key } : raw;
}

/**
 * A missing or non-finite `k` is not cosmetic: every coordinate on the canvas passes through it, so
 * `undefined` there is `NaN` everywhere and a blank canvas. Fall back rather than propagate.
 */
function backfillViewport(raw: unknown): ViewportTransform {
    const viewport = asRecord(raw);
    const k = finite(viewport.k, DEFAULT_VIEWPORT.k);
    return { x: finite(viewport.x, DEFAULT_VIEWPORT.x), y: finite(viewport.y, DEFAULT_VIEWPORT.y), k: k > 0 ? k : DEFAULT_VIEWPORT.k };
}

/**
 * The last line of defence.
 *
 * Backfill repairs the fields it knows about; this rejects anything that is still wrong, which is
 * what catches a shape change nobody migrated. It is deliberately shallow — enough to guarantee the
 * canvas can render, not a schema validator.
 */
function assertCanvasState(state: unknown): PersistedCanvasState {
    const root = asRecord(state);
    if (!Array.isArray(root.projects)) throw new Error("projects is not an array");
    root.projects.forEach((raw, index) => {
        const project = asRecord(raw);
        const where = `projects[${index}]`;
        if (typeof project.id !== "string" || !project.id) throw new Error(`${where}.id is missing`);
        if (typeof project.title !== "string") throw new Error(`${where}.title is not a string`);
        if (!Array.isArray(project.nodes)) throw new Error(`${where}.nodes is not an array`);
        if (!Array.isArray(project.connections)) throw new Error(`${where}.connections is not an array`);
        if (!isViewport(project.viewport)) throw new Error(`${where}.viewport is not a viewport`);
    });
    if (root.deletedProjects !== undefined && !Array.isArray(root.deletedProjects)) throw new Error("deletedProjects is not an array");
    if (root.lastProjectId !== undefined && typeof root.lastProjectId !== "string") throw new Error("lastProjectId is not a string");
    return root as PersistedCanvasState;
}

function isViewport(value: unknown): value is ViewportTransform {
    const viewport = asRecord(value);
    return Number.isFinite(viewport.x) && Number.isFinite(viewport.y) && Number.isFinite(viewport.k) && (viewport.k as number) > 0;
}

function finite(value: unknown, fallback: number) {
    return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function asRecord(value: unknown): Record<string, unknown> {
    return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : {};
}
