import { create } from "zustand";

/**
 * Where the agent panel is, and whether it is open.
 *
 * Not persisted, and deliberately: where a panel sits is the window's business, not the canvas's. It
 * has no place in the project blob, in an export, or in the undo history — dropping a card and moving a
 * panel are not the same kind of edit.
 *
 * `position` is null until the panel has been dragged, and null means "the default corner": the panel
 * has to survive the canvas being resized without a stored pixel value leaving it off-screen. `size` is the
 * same bargain: null is the size it opens at, and the user's own size is theirs for the session only.
 */
type CanvasAgentStore = {
    open: boolean;
    position: { x: number; y: number } | null;
    size: { width: number; height: number } | null;
    setOpen: (open: boolean) => void;
    toggle: () => void;
    setPosition: (position: { x: number; y: number }) => void;
    setSize: (size: { width: number; height: number }) => void;
};

export const useCanvasAgentStore = create<CanvasAgentStore>((set) => ({
    open: false,
    position: null,
    size: null,
    setOpen: (open) => set({ open }),
    toggle: () => set((state) => ({ open: !state.open })),
    setPosition: (position) => set({ position }),
    setSize: (size) => set({ size }),
}));
