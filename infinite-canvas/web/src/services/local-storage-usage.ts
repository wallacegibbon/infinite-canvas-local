import { APP_DATABASE, PLUGIN_DATABASE } from "@/services/localforage-databases";

type IndexedDbStoreUsage = { name: string; records: number; bytes: number };
type IndexedDbDatabaseUsage = { name: string; version: number; bytes: number; stores: IndexedDbStoreUsage[] };
export type LocalStorageUsage = { usage: number; quota: number | null; contentBytes: number; databases: IndexedDbDatabaseUsage[] };

/**
 * How much this app has put in the browser, broken down by object store.
 *
 * Both databases are measured. The plugin one used to be missing, so a canvas with heavy plugins
 * reported a number far below what the browser was actually holding against the quota.
 */
export async function readLocalStorageUsage(): Promise<LocalStorageUsage> {
    const [estimate, databases] = await Promise.all([readStorageEstimate(), readDatabases([APP_DATABASE, PLUGIN_DATABASE])]);
    const bytes = databases.reduce((total, database) => total + database.bytes, 0);
    return { usage: estimate?.usage ?? bytes, quota: estimate?.quota ?? null, contentBytes: bytes, databases };
}

async function readStorageEstimate() {
    if (typeof navigator === "undefined" || !navigator.storage?.estimate) return null;
    try {
        return await navigator.storage.estimate();
    } catch {
        return null;
    }
}

async function readDatabases(names: string[]) {
    const present = await existingDatabases(names);
    const databases: IndexedDbDatabaseUsage[] = [];
    for (const name of present) databases.push(await readDatabaseUsage(name));
    return databases.sort((a, b) => b.bytes - a.bytes);
}

/**
 * Which of these databases exist right now.
 *
 * `indexedDB.open` *creates* a database that is not there, so measuring must not use it blindly —
 * opening a name we merely guessed at would leave an empty database behind on every visit to the
 * settings panel. `databases()` answers the question without touching anything; where it is missing
 * we fall back to opening, because an empty database is a smaller problem than showing no numbers.
 */
async function existingDatabases(names: string[]) {
    if (typeof indexedDB === "undefined" || !indexedDB.databases) return names;
    try {
        const present = new Set((await indexedDB.databases()).map((database) => database.name).filter(Boolean));
        return names.filter((name) => present.has(name));
    } catch {
        return names;
    }
}

function readDatabaseUsage(name: string) {
    return new Promise<IndexedDbDatabaseUsage>((resolve, reject) => {
        const request = indexedDB.open(name);
        request.onerror = () => reject(request.error);
        request.onsuccess = () => {
            const database = request.result;
            const names = Array.from(database.objectStoreNames);
            if (!names.length) {
                database.close();
                resolve({ name, version: database.version, bytes: 0, stores: [] });
                return;
            }
            const transaction = database.transaction(names, "readonly");
            Promise.all(names.map((storeName) => readStoreUsage(transaction.objectStore(storeName))))
                .then((stores) => resolve({ name, version: database.version, bytes: stores.reduce((total, store) => total + store.bytes, 0), stores: stores.sort((a, b) => b.bytes - a.bytes) }))
                .catch(reject)
                .finally(() => database.close());
        };
    });
}

function readStoreUsage(store: IDBObjectStore) {
    return new Promise<IndexedDbStoreUsage>((resolve, reject) => {
        let records = 0;
        let bytes = 0;
        const request = store.openCursor();
        request.onerror = () => reject(request.error);
        request.onsuccess = () => {
            const cursor = request.result;
            if (!cursor) {
                resolve({ name: store.name, records, bytes });
                return;
            }
            records += 1;
            bytes += valueBytes(cursor.value);
            cursor.continue();
        };
    });
}

function valueBytes(value: unknown) {
    if (value instanceof Blob) return value.size;
    if (value instanceof ArrayBuffer) return value.byteLength;
    if (ArrayBuffer.isView(value)) return value.byteLength;
    return new TextEncoder().encode(typeof value === "string" ? value : JSON.stringify(value)).byteLength;
}
