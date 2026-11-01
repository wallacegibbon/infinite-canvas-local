// Runtime configuration, copied from public/ into dist/ at build time.
// start.sh overwrites this file on every run with the gateway address taken from .env
// (window.__RUNTIME_CONFIG__.LOCAL_GATEWAY_URL), so edit .env rather than this file.
// Assign window.__RUNTIME_CONFIG__ here when a value has to be baked into the bundle.
// The only keys the frontend reads are LOCAL_GATEWAY_URL / LOCAL_GATEWAY_HOST /
// LOCAL_GATEWAY_PORT — see src/constant/runtime-config.ts.
window.__RUNTIME_CONFIG__ = window.__RUNTIME_CONFIG__ || {};
