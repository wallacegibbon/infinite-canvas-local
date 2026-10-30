import type { Asset } from "@/stores/use-asset-store";

/**
 * The on-disk format of the asset store. Same reasoning as `canvas-persist.ts`: the version check
 * and the migration live inside `getItem` so that a blob which cannot be brought to the current
 * shape takes the one recovery path this app has — copy it aside, stop writing, tell the user —
 * instead of being discarded by zustand's own migration hook.
 */
export const ASSET_PERSIST_VERSION = 1;

type PersistedAssetState = { assets: Asset[] };

export function readAssetPersisted(raw: string): PersistedAssetState {
    const parsed = asRecord(JSON.parse(raw));
    const version = typeof parsed.version === "number" ? parsed.version : 0;
    if (version > ASSET_PERSIST_VERSION) throw new Error(`stored with format version ${version}, this build reads up to ${ASSET_PERSIST_VERSION}`);
    const state = version < ASSET_PERSIST_VERSION ? migrateAssetState(parsed.state) : parsed.state;
    return assertAssetState(state);
}

function migrateAssetState(state: unknown) {
    const source = asRecord(state);
    const assets = Array.isArray(source.assets) ? source.assets : [];
    return { assets: assets.filter(isUsableAsset) };
}

/**
 * An entry with no id, no kind or no `data` cannot be turned back into a card, and the hydration
 * pass in the store dereferences `data.storageKey` on every one of them. Drop those rather than
 * carry a shape nothing downstream can handle.
 */
function isUsableAsset(raw: unknown): boolean {
    const asset = asRecord(raw);
    return typeof asset.id === "string" && asset.id !== "" && typeof asset.kind === "string" && Boolean(asRecord(asset.data)) && Object.keys(asRecord(asset.data)).length > 0;
}

function assertAssetState(state: unknown): PersistedAssetState {
    const root = asRecord(state);
    if (!Array.isArray(root.assets)) throw new Error("assets is not an array");
    root.assets.forEach((raw, index) => {
        const asset = asRecord(raw);
        if (typeof asset.id !== "string" || !asset.id) throw new Error(`assets[${index}].id is missing`);
        if (typeof asset.kind !== "string") throw new Error(`assets[${index}].kind is not a string`);
        if (!Array.isArray(asset.tags)) throw new Error(`assets[${index}].tags is not an array`);
    });
    return root as PersistedAssetState;
}

function asRecord(value: unknown): Record<string, unknown> {
    return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : {};
}
