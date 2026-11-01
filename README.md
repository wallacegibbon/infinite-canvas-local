# Comfy Loft

A local workbench for ComfyUI: images, video and audio made on your own machine. No cloud API, no
vendor account, no per-image bill — the models, the graphs and the hardware are yours.

## Credits

This project started as a fork of
[1314521gjy/ComfyUI-Local-Infinite-Canvas](https://github.com/1314521gjy/ComfyUI-Local-Infinite-Canvas),
itself a modified copy of [basketikun/infinite-canvas](https://github.com/basketikun/infinite-canvas).
Both are MIT; the original license text is kept at [`canvas/LICENSE`](canvas/LICENSE).

The canvas frontend in `canvas/` started as a copy of that project's UI, and the Go gateway
(`main.go`, `internal/`) is a port of the Node gateway that shipped with it. Neither is a copy any more:
both have been reworked far enough that carrying the upstream project's name would have been a claim
about the code that is not true, so this project and that directory have names of their own (see
`docs/decisions.md` #137–#138). The upstream licence is kept verbatim at `canvas/LICENSE`, and the fork
this project descends from is named above.

Maintained here: the workflow graphs and `registry.json`, the resource index and installer,
`docs/`, `tools/` and `start.sh`.

## What it does

- **Infinite canvas**: text / image / video / group nodes; wiring, box selection, export, autosave.
- **Workflows are the unit**: the gateway runs ComfyUI workflows (`GET /api/workflows`, `POST /api/runs`) and reports what each accepts; the canvas decides which of them the picker offers.
- **Images**: Z-Image Turbo, MOSS SFT (8-step, regional layout), Qwen Image 2.1 (multi-image edit, masked inpaint, outpaint).
- **Video**: MiniMax-H3 text-to-video, first/last-frame and multi-reference, with sampling preview and LoRA cards.
- **Upscaling**: ESRGAN for images.
- **Local plugins**: LoRA strength card, ComfyUI parameter card.
- **Prompts come from AlayaCore**: one `alayacore --rawio` process per request, its frames relayed to the canvas; the model, the skills and the wording are the agent's.
- **System panel**: VRAM / RAM / GPU / ComfyUI status, one-click VRAM release.
- **Local-only by construction**: there is no cloud mode and no flag to re-enable one. The image/video/prompt workbenches, the cloud model channels, the prompt library and the prompt-API settings tab are deleted, not hidden: there is one channel, it is the gateway that served the page, and prompts are the agent's (`docs/decisions.md` #71, #76).

## Layout

The repository root *is* the Go gateway (`main.go` + `internal/` + runtime assets);
the canvas frontend is a self-contained subtree beside it (see Credits):

```text
.
├── main.go            # gateway entry point
├── internal/          # the whole gateway (config/comfy/workflow/generate/resource/prompt/…)
├── workflows/         # what the product exposes: graphs + registry.json
├── resources.json     # what has to be installed: models, node packs, runtime deps
├── prompts/           # prompt skill files
├── data/              # runtime state: tasks.json, resource-lock.json, outputs/ (not tracked)
├── bin/               # build output (not tracked)
├── canvas/            # the canvas frontend (from the upstream project, since reworked)
├── docs/              # gateway API and env vars, the run protocol, prompt optimization, runtime deps
├── tools/             # development scripts
├── Makefile           # make build / check / check-full / verify-static / web / web-check
└── start.sh           # starts the gateway and previews the canvas
```

Two directories carry the runtime assets, and the split is deliberate:

- **`workflows/`** is the outward face: one graph per model, plus `registry.json` describing
  how it is presented (name, type, prompt binding, parameters). Adding a model means dropping
  a `<type>-<slug>.json` here and scanning.
- **`resources.json`** is inward: it lists what has to be present on the machine (models,
  custom nodes, runtime deps). Nothing is bundled with the binary: a model comes from its URLs and a
  node pack comes from its archive, both pinned — a model by `sha256`, a node pack by
  `pinnedCommit`. A `payload/` directory is still honoured as an escape hatch if a file ever has to
  ship inside the release; nothing is staged there today.

  A pack does not list the files it needs: those are read off the graphs under `workflows/`, so the
  panel and the workflows cannot disagree. The only list a pack writes down is its `alternates` — the
  second precision of a base model, the other upscale checkpoints — because no graph names them.

What does ship with the installer is the application itself: the gateway binary, the canvas bundle,
the graphs under `workflows/` and this index. That is why the panel lists no pack for them — a pack is
a bundle of downloads, and the installer's own files are not one.

## Language

Everything written for a human is **English**: documentation, code comments, log lines, error
messages and commit messages. Two kinds of text do not follow the rule, and only because they
are data or user-facing copy rather than prose:

- **UI copy** lives in the i18n dictionaries (`canvas/web/src/i18n`), never inline in
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
make web               # installs canvas/web deps if needed, then vite build
make build             # Go gateway, static binary; start.sh builds it too if missing

# Configure and start
cp .env.example .env   # adjust COMFY_URL / GATEWAY_HOST for this machine
                        # (INSTALL_INTO_COMFY=1 there is what allows the panel to install models)
./start.sh
```

Canvas `http://127.0.0.1:3000` · gateway `http://127.0.0.1:8190`
ComfyUI `http://127.0.0.1:8188` must already be running; the workbench only connects, never starts it.

> The canvas bundle `canvas/web/dist/` is build output and is not tracked. After changing
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

One thing to know when the gateway is reachable from the network: **it has no authentication.** Any
device that can reach the port can run a workflow, read its artifacts and drive a canvas's agent (the
gateway warns about this at startup). There is no token to turn on — the browser could not have kept
one — so the choice is the address you bind and the network it sits on. `CORS_ORIGINS` is only needed
for a canvas served from a *different* host than the gateway, and it governs browsers only.

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
| `/api/agent/socket/{session}` | The agent: one WebSocket per session, named by the surface that owns it |
| `/mcp/{workspace}` | The agent's hands: the MCP server a session's tool calls arrive at |
| `/api/resources/*` | Download models, install custom nodes |
| `/api/system/*`, `/api/loras`, `/files/:name`, `/health` | System status and artifacts |

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
- [`docs/runtime-deps.md`](docs/runtime-deps.md): SageAttention and other runtimes must be installed with `pip` by hand (the workbench no longer does it).
- [`tools/bake-moss-preset.py`](tools/bake-moss-preset.py): bakes MOSS Base+SFT into `moss_balanced_s100.safetensors` — the `builder` behind the `type: "bake"` entry in the resource index; run it with ComfyUI's Python (it imports torch).

## License

MIT, see [`LICENSE`](LICENSE).
