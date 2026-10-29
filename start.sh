#!/usr/bin/env bash
# Starts the services the local infinite canvas needs: the gateway (default :8190) and the
# static canvas server (default :3000).
# ComfyUI is out of scope by default (external mode); set START_COMFY=1 in .env to launch it too.
#
# Runs in the foreground; Ctrl+C stops everything. There is no crash recovery: if it dies,
# run ./start.sh again.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# .env is sourced into this shell with `set -a`, so without protection anything the caller
# passed explicitly would be silently overwritten: `PORT=9000 ./start.sh` used to come up
# on 8190 anyway. Only the variables .env declares are at risk — everything else is simply
# inherited by the child processes — so those names are collected first, the caller's
# values are snapshotted, and the snapshot is restored after the source.
if [ -f "$ROOT/.env" ]; then
  env_names=()
  while IFS= read -r name; do
    [ -n "$name" ] && env_names+=("$name")
  done < <(sed -nE 's/^[[:space:]]*(export[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)=.*/\2/p' "$ROOT/.env")

  caller_values=()
  for name in "${env_names[@]}"; do
    if [ -n "${!name+set}" ]; then
      caller_values+=("$name=${!name}")
    fi
  done

  set -a
  # shellcheck disable=SC1091
  . "$ROOT/.env"
  set +a

  restored=()
  if [ "${#caller_values[@]}" -gt 0 ]; then
    for pair in "${caller_values[@]}"; do
      name="${pair%%=*}"
      value="${pair#*=}"
      if [ "${!name-}" != "$value" ]; then
        restored+=("$name")
        export "$name=$value"
      fi
    done
  fi
  if [ "${#restored[@]}" -gt 0 ]; then
    echo "→ command-line variables win over .env: ${restored[*]}"
  fi
  unset env_names caller_values restored name value pair
fi

export COMFY_URL="${COMFY_URL:-http://127.0.0.1:8188}"
export COMFYUI_DIR="${COMFYUI_DIR:-$HOME/comfy/ComfyUI}"
export COMFY_WORKFLOWS_DIR="${COMFY_WORKFLOWS_DIR:-$COMFYUI_DIR/user/default/workflows}"
export HOST="${HOST:-127.0.0.1}"
export PORT="${PORT:-8190}"

CANVAS_HOST="${CANVAS_HOST:-127.0.0.1}"
CANVAS_PORT="${CANVAS_PORT:-3000}"
VITE_BIN="$ROOT/infinite-canvas/web/node_modules/.bin/vite"
GATEWAY_BIN="$ROOT/bin/comfyui-gateway"

command -v node >/dev/null 2>&1 || { echo "node not found; install Node.js first" >&2; exit 1; }
[ -x "$VITE_BIN" ] || { echo "missing $VITE_BIN; run: cd infinite-canvas/web && npm install" >&2; exit 1; }
[ -f "$ROOT/infinite-canvas/web/dist/index.html" ] || { echo "canvas bundle is missing; run: cd infinite-canvas/web && npm run build" >&2; exit 1; }

pids=()
cleanup() {
  trap - EXIT INT TERM
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

if [ "${START_COMFY:-0}" = "1" ]; then
  echo "→ ComfyUI   ${COMFYUI_DIR}（${COMFY_PYTHON:-python} main.py ${COMFY_ARGS:-}）"
  ( cd "$COMFYUI_DIR" && exec "${COMFY_PYTHON:-python}" main.py ${COMFY_ARGS:-} ) & pids+=($!)
fi

# The gateway is a static binary: build it once when it is missing (needs the Go toolchain),
# after which startup takes milliseconds.
if [ ! -x "$GATEWAY_BIN" ]; then
  echo "→ building the gateway (first run, needs the Go toolchain)"
  ( cd "$ROOT" && make build ) || { echo "gateway build failed" >&2; exit 1; }
fi

echo "→ gateway   http://${HOST}:${PORT}"
( cd "$ROOT" && exec "$GATEWAY_BIN" ) & pids+=($!)

echo "→ canvas    http://${CANVAS_HOST}:${CANVAS_PORT}"
( cd "$ROOT/infinite-canvas/web" && exec "$VITE_BIN" preview --host "$CANVAS_HOST" --port "$CANVAS_PORT" ) & pids+=($!)

echo
echo "All services are up. Ctrl+C stops every process."
wait
