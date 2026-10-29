# ComfyUI Local Infinite Canvas

A local infinite-canvas workbench for ComfyUI: generate images and videos
from ComfyUI workflows without touching a cloud API.

## Credits

This project started as a fork of
[1314521gjy/ComfyUI-Local-Infinite-Canvas](https://github.com/1314521gjy/ComfyUI-Local-Infinite-Canvas),
itself a modified copy of [basketikun/infinite-canvas](https://github.com/basketikun/infinite-canvas).
Both are MIT; the original license text is kept at [`infinite-canvas/LICENSE`](infinite-canvas/LICENSE).

The canvas frontend in `infinite-canvas/` comes from those repositories. The Go gateway
(`main.go`, `internal/`) is a port of the Node gateway that shipped with the fork.

Maintained here: the workflow graphs and `registry.json`, the resource index and installer,
`docs/`, `tools/` and `start.sh`.

## What it does

- **Infinite canvas**: text / image / video / group nodes; wiring, box selection, export, autosave.
- **Workflows as models**: the local gateway wraps ComfyUI workflows as OpenAI-compatible models, so the canvas only changes its base URL.
- **Images**: Z-Image Turbo, Krea2 Aesthetics, MOSS SFT (8-step, regional layout), Qwen Image 2.1 (multi-image edit, masked inpaint, outpaint).
- **Video**: MiniMax-H3 text-to-video, first/last-frame and multi-reference, with sampling preview and LoRA cards.
- **Upscaling**: ESRGAN, SeedVR2 for images and video.
- **Local plugins**: LoRA strength card, ComfyUI parameter card.
- **Prompt optimization**: optional, points at any local or remote OpenAI-compatible endpoint — see [`docs/local-llm.md`](docs/local-llm.md).
- **System panel**: VRAM / RAM / GPU / ComfyUI status, one-click VRAM release.
- **Local-only mode**: cloud model channels, WebDAV and the prompt library are hidden by default; the prompt optimizer's "prompt API" tab stays, because it accepts any OpenAI-compatible endpoint including `127.0.0.1`.

## Layout

The repository root *is* the Go gateway (`main.go` + `internal/` + runtime assets);
the canvas frontend is a self-contained third-party subtree beside it:

```text
.
├── main.go            # gateway entry point
├── internal/          # the whole gateway (config/comfy/workflow/generate/resource/prompt/…)
├── workflows/         # workflow graphs + registry.json (runtime assets)
├── resources/         # resource index (index.json) and bundled custom nodes
├── prompts/           # prompt skill files
├── data/              # runtime state: tasks.json, resource-lock.json, outputs/ (not tracked)
├── bin/               # build output (not tracked)
├── infinite-canvas/   # canvas frontend (third-party, ships its own web/ and build)
├── docs/              # gateway API and env vars, prompt optimization, runtime deps
├── tools/             # development scripts
├── Makefile           # make build / check / check-full / verify-static / web / web-check
└── start.sh           # starts the gateway and previews the canvas
```

## Quick start

```bash
# Dependencies (first run)
make web               # installs infinite-canvas/web deps if needed, then vite build
make build             # Go gateway, static binary; start.sh builds it too if missing

# Configure and start
cp .env.example .env   # set COMFYUI_DIR / COMFY_WORKFLOWS_DIR / COMFY_PYTHON for this machine
./start.sh
```

Canvas `http://127.0.0.1:3000` · gateway `http://127.0.0.1:8190`
ComfyUI `http://127.0.0.1:8188` must already be running; the workbench only connects, never starts it.

> The canvas bundle `infinite-canvas/web/dist/` is build output and is not tracked. After changing
> `web/src/` run `make web` again (`make web-check` typechecks without emitting); `./start.sh`
> refuses to start without a bundle and says so.

Gateway endpoints and environment variables: [`docs/gateway.md`](docs/gateway.md).

## Processes and interface ownership

Four processes, all calls one-directional; the browser only talks to the gateway.

| Process | Port | What it is | Started by |
|---|---|---|---|
| Canvas frontend | `3000` | Prebuilt static pages (`vite preview`) | `./start.sh` |
| Gateway | `8190` | Go static binary that wraps ComfyUI workflows as OpenAI-compatible models | `./start.sh` |
| ComfyUI | `8188` | Python, does the actual image / video work | You (`START_COMFY=0` means the workbench leaves it alone) |
| llama-server | `8099` | Optional, the model behind prompt optimization | You, see [`docs/local-llm.md`](docs/local-llm.md) |

```text
browser ─→ :8190 gateway ─┬─→ :8188 ComfyUI      images / videos
                          └─→ :8099 llama-server  prompt optimization (optional)
```

The frontend sources contain no reference to `8188`; the canvas never bypasses the gateway.

**There are no other background services.** The local agent used by Codex and the
CORS-avoidance proxy have been removed from the repository; `./start.sh` starts one
gateway process and one canvas process.

### Where each endpoint lives

The `/v1/...` prefix appears on both the gateway and llama-server, but they **belong to
different services**.

**Gateway `:8190` endpoints of its own** — OpenAI has none of these:

| Endpoint | Purpose |
|---|---|
| `/v1/prompts/optimize`, `/v1/prompts/optimize-image`, `/v1/prompts/caption` | Prompt optimization, image captioning |
| `/api/resources/*` | Download models, install custom nodes |
| `/api/workflows/*`, `/api/setup/*` | Workflow metadata, ComfyUI scanning |
| `/api/system/*`, `/api/loras`, `/api/tasks`, `/files/:name`, `/health` | System status and artifacts |

**Gateway `:8190` endpoints shaped like OpenAI** — the paths look standard, ComfyUI
workflows are behind them:

`/v1/models`, `/v1/images/generations`, `/v1/images/edits`, `/v1/images/upscale`, `/v1/videos`, …

**ComfyUI `:8188` endpoints** — only the gateway's ComfyUI client (`internal/comfy`) calls
these, the canvas never sees them:

`POST /prompt`, `GET /history/:id`, `GET /view`, `POST /upload/image`, `GET /object_info`,
`GET /internal/folder_paths`, `GET /system_stats`, `POST /free`, `POST /interrupt`, `/queue`

**llama-server `:8099` endpoints** — only the `cloud` mode of `/v1/prompts/*` calls these:

`POST /v1/chat/completions`, `GET /v1/models`, `GET /health`

> ⚠️ `/v1/models` means different things on the two services: `:8190` returns **workflows
> wrapped as models** (`image-z-image-turbo`, `video-h3-t2v`…), `:8099` returns the **weights
> actually loaded**. Both satisfy the OpenAI schema, but one is a "fake model" and the other
> is real.

## Documentation

- [`docs/gateway.md`](docs/gateway.md): gateway endpoints, environment variables, architecture and language policy.
- [`docs/decisions.md`](docs/decisions.md): why the code looks the way it does — one entry per decision, with its cost.
- [`docs/known-issues.md`](docs/known-issues.md): verified open problems and the command that fixes each.
- [`docs/local-llm.md`](docs/local-llm.md): prompt optimization / image captioning with a local llama.cpp (arguments, verification commands, pitfalls).
- [`docs/runtime-deps.md`](docs/runtime-deps.md): SageAttention / SeedVR2 and other runtimes must be installed with `pip` by hand (the workbench no longer does it).
- [`tools/bake-moss-preset.py`](tools/bake-moss-preset.py): bakes MOSS Base+SFT into `moss_balanced_s100.safetensors` — the `builder` behind the `type: "bake"` entry in the resource index; run it with ComfyUI's Python (it imports torch).

## License

MIT, see [`LICENSE`](LICENSE).
