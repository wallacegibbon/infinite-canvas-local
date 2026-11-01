import { create } from "zustand";

export const CANVAS_SIDE_PANEL_MOTION_MS = 500;
export const CANVAS_SIDE_PANEL_MIN_WIDTH = 220;
export const CANVAS_SIDE_PANEL_MAX_WIDTH = 480;
const CANVAS_SIDE_PANEL_DEFAULT_WIDTH = 280;

const WIDTH_KEY = "canvas-side-panel-width";
const OPEN_KEY = "canvas-side-panel-open";
const NODE_VIEW_KEY = "canvas-side-panel-node-view";

type CanvasNodeViewMode = "list" | "grid";

function initialWidth() {
    if (typeof window === "undefined") return CANVAS_SIDE_PANEL_DEFAULT_WIDTH;
    const stored = Number(localStorage.getItem(WIDTH_KEY));
    if (!stored) return CANVAS_SIDE_PANEL_DEFAULT_WIDTH;
    return Math.min(CANVAS_SIDE_PANEL_MAX_WIDTH, Math.max(CANVAS_SIDE_PANEL_MIN_WIDTH, stored));
}

/** Shut until asked for: the board floats over the canvas, so nothing is missing while it is away. */
function initialOpen() {
    if (typeof window === "undefined") return false;
    return localStorage.getItem(OPEN_KEY) === "1";
}

function initialNodeView(): CanvasNodeViewMode {
    if (typeof window === "undefined") return "list";
    return localStorage.getItem(NODE_VIEW_KEY) === "grid" ? "grid" : "list";
}

type CanvasSidePanelStore = {
    width: number;
    panelOpen: boolean;
    nodeViewMode: CanvasNodeViewMode;
    setWidth: (width: number) => void;
    /** Write the current width to storage. Called when a drag ends, not on every pixel. */
    commitWidth: () => void;
    setNodeViewMode: (mode: CanvasNodeViewMode) => void;
    togglePanel: () => void;
};

export const useCanvasSidePanelStore = create<CanvasSidePanelStore>((set, get) => ({
    width: initialWidth(),
    panelOpen: initialOpen(),
    nodeViewMode: initialNodeView(),
    setWidth: (width) => set({ width }),
    commitWidth: () => {
        if (typeof window !== "undefined") localStorage.setItem(WIDTH_KEY, String(get().width));
    },
    setNodeViewMode: (mode) => {
        if (typeof window !== "undefined") localStorage.setItem(NODE_VIEW_KEY, mode);
        set({ nodeViewMode: mode });
    },
    togglePanel: () => {
        const open = !get().panelOpen;
        if (typeof window !== "undefined") localStorage.setItem(OPEN_KEY, open ? "1" : "0");
        set({ panelOpen: open });
    },
}));

/**
 * Where the canvas's own chrome should sit, measured from the left edge. The board floats there, so a
 * zoom dock or a minimap that used to be pushed aside by a column now has to step over it instead.
 */
export function useSidePanelInset(gap: number) {
    const open = useCanvasSidePanelStore((state) => state.panelOpen);
    const width = useCanvasSidePanelStore((state) => state.width);
    return open ? width + gap : gap;
}
