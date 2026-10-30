/**
 * The IndexedDB databases this app owns, and the object stores inside the main one.
 *
 * These names used to be string literals in four files — `localforage.config` in one place,
 * `createInstance` in three services, and again in the storage-usage reader. Nothing made them
 * agree, and they did not: the reader only knew about the main database, so anything stored by a
 * plugin was invisible in the storage panel while still counting against the browser quota.
 *
 * Writing them down once makes the reader enumerate what actually exists instead of guessing.
 */

/** Main database: canvas state, uploaded media, generated images and the logs for both. */
export const APP_DATABASE = "infinite-canvas";

/** One object store per plugin id, created on demand by the plugin event bus. */
export const PLUGIN_DATABASE = "infinite-canvas-plugins";

/** Every object store in {@link APP_DATABASE}, in rough order of size. */
const APP_STORES = ["app_state", "media_files", "image_files", "image_previews", "image_generation_logs", "video_generation_logs"] as const;

