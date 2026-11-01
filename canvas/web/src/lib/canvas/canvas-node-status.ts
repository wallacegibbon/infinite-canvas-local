/**
 * The four states a node's metadata.status can hold.
 *
 * These were string literals scattered through `project.tsx` and the node components; a typo in one
 * of them was silently a different state. They live here so the union is written once.
 */
export const NODE_STATUS_IDLE = "idle" as const;
export const NODE_STATUS_LOADING = "loading" as const;
export const NODE_STATUS_SUCCESS = "success" as const;
export const NODE_STATUS_ERROR = "error" as const;
