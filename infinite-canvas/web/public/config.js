// Runtime configuration, copied from public/ into dist/ at build time.
// start.sh overwrites this file on every run with the gateway address taken from .env
// (window.__RUNTIME_CONFIG__.LOCAL_GATEWAY_URL), so edit .env rather than this file.
// Leaving it empty disables optional features such as analytics; assign
// window.__RUNTIME_CONFIG__ here when a value has to be baked into the bundle.
window.__RUNTIME_CONFIG__ = window.__RUNTIME_CONFIG__ || {};
