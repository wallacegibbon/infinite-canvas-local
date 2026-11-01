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
- **Workflows are the unit**: the gateway runs ComfyUI workflows (`GET /api/workflows`, `POST /api/runs`) and reports what each accepts; the canvas decides which of them the picker offers.
- **Images**: Z-Image Turbo, Krea2 Aesthetics, MOSS SFT (8-step, regional layout), Qwen Image 2.1 (multi-image edit, masked inpaint, outpaint).
- **Video**: MiniMax-H3 text-to-video, first/last-frame and multi-reference, with sampling preview and LoRA cards.
- **Upscaling**: ESRGAN, SeedVR2 for images and video.
- **Local plugins**: LoRA strength card, ComfyUI parameter card.
- **Prompts come from AlayaCore**: one `alayacore --rawio` process per request, its frames relayed to the canvas; the model, the skills and the wording are the agent's.
- **System panel**: VRAM / RAM / GPU / ComfyUI status, one-click VRAM release.
- **Local-only by construction**: there is no cloud mode and no flag to re-enable one. The image/video/prompt workbenches, cloud model channels and the prompt library are deleted, not hidden; the prompt optimizer's "prompt API" tab stays, because it accepts any OpenAI-compatible endpoint including `127.0.0.1`.

## Layout

The repository root *is* the Go gateway (`main.go` + `internal/` + runtime assets);
the canvas frontend is a self-contained third-party subtree beside it:

```text
.
├── main.go            # gateway entry point
├── internal/          # the whole gateway (config/comfy/workflow/generate/resource/prompt/…)
├── workflows/         # what the product exposes: graphs + registry.json
├── resources/         # what it needs installed: index.json + payload/
├── prompts/           # prompt skill files
├── data/              # runtime state: tasks.json, resource-lock.json, outputs/ (not tracked)
├── bin/               # build output (not tracked)
├── infinite-canvas/   # canvas frontend (third-party, ships its own web/ and build)
├── docs/              # gateway API and env vars, the run protocol, prompt optimization, runtime deps
├── tools/             # development scripts
├── Makefile           # make build / check / check-full / verify-static / web / web-check
└── start.sh           # starts the gateway and previews the canvas
```

Two directories carry the runtime assets, and the split is deliberate:

- **`workflows/`** is the outward face: one graph per model, plus `registry.json` describing
  how it is presented (name, type, prompt binding, parameters). Adding a model means dropping
  a `<type>-<slug>.json` here and scanning.
- **`resources/`** is inward: `index.json` lists what has to be present on the machine (models
  to download, custom nodes, runtime deps), and `payload/` holds the files that ship with the
  gateway instead of being downloaded. The layout mirrors the install target, so a payload
  resource needs no field of its own — `resources/payload/custom_nodes/<name>` is a node and
  `resources/payload/<type>/<file>` is a model; anything without a payload comes from its URLs.

## Language

Everything written for a human is **English**: documentation, code comments, log lines, error
messages and commit messages. Two kinds of text do not follow the rule, and only because they
are data or user-facing copy rather than prose:

- **UI copy** lives in the i18n dictionaries (`infinite-canvas/web/src/i18n`), never inline in
  a component, so the same screen can be read in Chinese and English. That includes workflow
  metadata: `workflows/registry.json` is English (it is data the gateway serves) and the canvas
  translates the labels it displays by model id — `web/src/lib/gateway-labels.ts`, decisions
  #20.
- **Model-facing and protocol Chinese** — H3 writing instructions in `internal/prompt`, the
  Chinese-matching regexes, and ComfyUI mode strings such as `文生视频(T2VA)` — stays Chinese
  because translating it would change generation behaviour or break parsing.

The reasoning and the exact exceptions: [`docs/decisions.md`](docs/decisions.md) #8, #9, #17, #20.
Chinese prose that still predates the rule is listed in
[`docs/known-issues.md`](docs/known-issues.md) #10.

## Quick start

```bash
# Dependencies (first run)
make web               # installs infinite-canvas/web deps if needed, then vite build
make build             # Go gateway, static binary; start.sh builds it too if missing

# Configure and start
cp .env.example .env   # adjust COMFY_URL / GATEWAY_HOST for this machine
                        # (INSTALL_INTO_COMFY=1 there is what allows the panel to install models)
./start.sh
```

Canvas `http://127.0.0.1:3000` · gateway `http://127.0.0.1:8190`
ComfyUI `http://127.0.0.1:8188` must already be running; the workbench only connects, never starts it.

> The canvas bundle `infinite-canvas/web/dist/` is build output and is not tracked. After changing
> `web/src/` run `make web` again (`make web-check` typechecks without emitting); `./start.sh`
> refuses to start without a bundle and says so.

### Reaching the workbench from another device

`GATEWAY_HOST` is a listen address, not a policy switch: put the machine's LAN address there and the
gateway binds it, the same way `127.0.0.1` binds loopback. Nothing else has to be enabled.

```bash
# .env
GATEWAY_HOST=192.168.3.232  # the gateway; also the address the browser will call
GATEWAY_PORT=8190
CANVAS_HOST=192.168.3.232   # what `vite preview` serves
CANVAS_PORT=3000
```

`./start.sh` derives everything else from those two addresses: it writes the gateway URL into
`dist/config.js` (`window.__RUNTIME_CONFIG__.LOCAL_GATEWAY_URL`, so the browser does not fall
back to its own loopback) and lists the canvas origin for `CORS_ORIGINS`. Open
`http://192.168.3.232:3000` from the phone and the canvas talks to `http://192.168.3.232:8190`.

Two things to know when the gateway is reachable from the network: `/api/*` is unauthenticated
until `GATEWAY_TOKEN` is set (the gateway warns about this at startup), and `CORS_ORIGINS` is
only needed for a canvas served from a *different* host than the gateway.

Gateway endpoints and environment variables: [`docs/gateway.md`](docs/gateway.md).

## Processes and interface ownership

Four processes, all calls one-directional; the browser only talks to the gateway.

| Process | Port | What it is | Started by |
|---|---|---|---|
| Canvas frontend | `3000` | Prebuilt static pages (`vite preview`) | `./start.sh` |
| Gateway | `8190` | Go static binary that runs ComfyUI workflows through its own run protocol | `./start.sh` |
| ComfyUI | `8188` | Python, does the actual image / video work | You, wherever it runs — nothing here starts it |
| alayacore | *(spawned per request)* | The agent that answers prompts | The ecosystem's own install |

```text
browser ─→ :8190 gateway ─┬─→ :8188 ComfyUI      images / videos
```

The frontend sources contain no reference to `8188`; the canvas never bypasses the gateway.

**There are no other background services.** The local agent used by Codex and the
CORS-avoidance proxy have been removed from the repository; `./start.sh` starts one
gateway process and one canvas process.

### Where each endpoint lives

**The gateway serves no `/v1/...` at all.** It has its own protocol — `POST /api/runs` produces every
kind of media, `GET /api/workflows` describes what can run — and OpenAI's paths are not among its
endpoints. `/v1/...` in this repository belongs to the model provider AlayaCore talks to.

**Gateway `:8190` endpoints:**

| Endpoint | Purpose |
|---|---|
| `/api/runs`, `/api/uploads` | Produce media (submit, poll, download, cancel) and stage its inputs |
| `/api/workflows/*`, `/api/setup/*` | Workflow metadata, schemas, ComfyUI scanning |
| `/api/agent/socket/{session}`, `/api/agent/once` | The agent: a WebSocket per canvas, and a one-shot socket that dies with its answer |
| `/api/resources/*` | Download models, install custom nodes |
| `/api/system/*`, `/api/loras`, `/api/tasks`, `/files/:name`, `/health` | System status and artifacts |

**ComfyUI `:8188` endpoints** — only the gateway's ComfyUI client (`internal/comfy`) calls
these, the canvas never sees them:

`POST /prompt`, `GET /history/:id`, `GET /view`, `POST /upload/image`, `GET /object_info`,
`GET /internal/folder_paths`, `GET /system_stats`, `POST /free`, `POST /interrupt`, `/queue`

**AlayaCore** — spawned by the gateway, one process per prompt request
(`alayacore --rawio --builtin-tools=read_file --skill <container> --config-path <dir>`), speaking its
own frame protocol on stdin/stdout. Its model list, model choice and skills are its own
`model.conf`; the gateway names the directory and never reads it.

> ⚠️ A gateway "model" **is** a workflow: `image-z-image-turbo`, `video-h3-t2v`, and one id per
> installed base model. They come from `GET /api/workflows`. A **prompt** model is a different thing
> and belongs to the agent — the two lists have nothing to do with each other.

## Documentation

- [`docs/gateway.md`](docs/gateway.md): gateway endpoints, environment variables, architecture and language policy.
- [`docs/decisions.md`](docs/decisions.md): why the code looks the way it does — one entry per decision, with its cost.
- [`docs/known-issues.md`](docs/known-issues.md): verified open problems and the command that fixes each.
- [`docs/runtime-deps.md`](docs/runtime-deps.md): SageAttention / SeedVR2 and other runtimes must be installed with `pip` by hand (the workbench no longer does it).
- [`tools/bake-moss-preset.py`](tools/bake-moss-preset.py): bakes MOSS Base+SFT into `moss_balanced_s100.safetensors` — the `builder` behind the `type: "bake"` entry in the resource index; run it with ComfyUI's Python (it imports torch).

## License

MIT, see [`LICENSE`](LICENSE).
