import { create } from "zustand";

/**
 * Persisted stores whose saved value could not be read back.
 *
 * This exists because of a data-loss path: a `persist` storage adapter that throws on an
 * unreadable blob makes zustand swallow the error into `onRehydrateStorage`, the app boots with
 * empty defaults, and the next state change writes those defaults over the data that was still
 * sitting in IndexedDB. The user sees an empty canvas list and no explanation anywhere.
 *
 * So the adapters no longer throw. They record here, they refuse to write over what they could not
 * read, and the banner in `components/layout/storage-recovery-banner.tsx` tells the user what
 * happened and offers to export the raw bytes before anything is discarded.
 */
export type StorageFailure = {
    /** Which persisted store failed. Used both as the flag and as the banner's export name. */
    store: "canvas" | "asset";
    /** The key the adapter was asked for, e.g. `comfy-loft:canvas_store`. */
    key: string;
    /** The bytes as stored. Held so the user can export them. */
    raw: string;
    /** Where the raw bytes were copied to, so discarding cannot destroy them. */
    copyKey: string | null;
    error: string;
};

type StorageRecoveryStore = {
    failures: StorageFailure[];
    recordFailure: (failure: StorageFailure) => void;
    /** Called after "clear and start over": writes are allowed again, the copy stays behind. */
    dismiss: () => void;
};

export const useStorageRecoveryStore = create<StorageRecoveryStore>((set) => ({
    failures: [],
    recordFailure: (failure) => set((state) => (state.failures.some((item) => item.store === failure.store) ? state : { failures: [...state.failures, failure] })),
    dismiss: () => set({ failures: [] }),
}));

/**
 * Read outside React, from a storage adapter that runs before anything has mounted.
 *
 * The adapter consults this on every write: a store that could not be read must not be written
 * over, or the unreadable blob is gone before anybody can look at it.
 */
export function isStoreUnreadable(store: StorageFailure["store"]) {
    return useStorageRecoveryStore.getState().failures.some((item) => item.store === store);
}
