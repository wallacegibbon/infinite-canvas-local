import { create } from "zustand";

/**
 * Which panel is floating over the canvas right now.
 *
 * These live together because they answer one question — "what is open?" — and because
 * none of them deserves a route. A canvas has an address; a list of canvases, the asset
 * library and the settings panel do not. Nobody shares them, nobody bookmarks them, and
 * refreshing them means nothing. Only a canvas is worth a URL.
 *
 * Settings keep their own state in `use-config-store` (`isConfigOpen`) because twenty-odd
 * call sites already open it by tab, and moving that would have been churn with no payoff.
 */
type PanelStore = {
    canvasManagerOpen: boolean;
    assetsOpen: boolean;
    setCanvasManagerOpen: (open: boolean) => void;
    setAssetsOpen: (open: boolean) => void;
};

export const usePanelStore = create<PanelStore>((set) => ({
    canvasManagerOpen: false,
    assetsOpen: false,
    setCanvasManagerOpen: (canvasManagerOpen) => set({ canvasManagerOpen }),
    setAssetsOpen: (assetsOpen) => set({ assetsOpen }),
}));
