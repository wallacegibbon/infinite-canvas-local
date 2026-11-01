import localforage from "localforage";

import { useStorageRecoveryStore, type StorageFailure } from "@/stores/use-storage-recovery-store";

/**
 * Handle a persisted value that would not parse, without destroying it.
 *
 * Two things have to happen before anything touches the key again: the raw bytes are copied to a
 * side key, and the failure is recorded so the storage adapter stops writing and the banner can
 * explain. Both are best-effort — the copy failing does not change the outcome, because the raw
 * string is also held in memory for export.
 */
export function preserveUnreadable(store: StorageFailure["store"], key: string, raw: string, error: unknown) {
    const copyKey = `${key}.corrupt-${new Date().toISOString().replace(/[:.]/g, "-")}`;
    useStorageRecoveryStore.getState().recordFailure({
        store,
        key,
        raw,
        copyKey,
        error: error instanceof Error ? error.message : String(error),
    });
    void localforage.setItem(copyKey, raw).catch(() => {});
}
