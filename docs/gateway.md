# comfyui-gateway (Go)

A local, OpenAI-compatible gateway that exposes ComfyUI workflows as models for the
Infinite Canvas. It is the repository root: `main.go`, `internal/`, and the runtime
assets (`workflows/`, `resources/`, `prompts/`) live at the top level, while the canvas
frontend sits beside them in `infinite-canvas/`.

It is a **single statically linked binary**: no Node runtime, no `node_modules`, no
Python virtualenv. Configuration comes from the environment; all mutable state lives
in `data/`.

## Why Go

The gateway used to be ~7400 lines of JavaScript in the upstream fork, and needed a
matching Node install on every machine. The gateway is the one component the canvas cannot
work without, so it is now a static binary that starts in milliseconds and cannot break
because a transitive npm package changed. `make verify-static` asserts the build has no
dynamic dependencies at all.

The Go implementation is a port of that Node gateway.

## Requirements

- Go 1.26+ (build only)
- A running ComfyUI (default `http://127.0.0.1:8188`)
- Optional: `ffprobe` (character-swap workflows read the source video length),
  `zenity` (the "pick a model file" dialog), `nvidia-smi` (GPU panel)

## Platforms

Nothing in the gateway is tied to one OS: it is pure Go with `CGO_ENABLED=0`, so it
cross-compiles to the three mainstream targets from any host, and the frontend is just a
browser bundle.

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o comfyui-gateway.exe .
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o comfyui-gateway-mac .
```

Linux is the target that is actually run and tested. Three probes lean on Linux facilities
and degrade on their own elsewhere rather than failing the process:

| Probe | On Linux | Elsewhere |
|---|---|---|
| RAM usage | `/proc/meminfo` | panel shows nothing (returns an empty record) |
| ComfyUI's interpreter | `/proc/<pid>/cmdline` + `/proc/<pid>/exe` | detection returns nothing; set `COMFY_PYTHON` instead |
| "Pick a model file" dialog | `zenity` | the request fails with a message telling you to type the relative path |

`start.sh` is a bash script, so Windows needs WSL or Git Bash; running `bin/comfyui-gateway`
by hand works anywhere.

## Quick start

```bash
make build          # -> bin/comfyui-gateway (static, ~2 MB)
COMFYUI_DIR=$HOME/comfy/ComfyUI ./bin/comfyui-gateway
```

Run it from the repository root: the asset root (`workflows/`, `resources/`, `prompts/`)
is discovered from the working directory first, then the executable's directory. Set
`GATEWAY_ROOT` to override.

Verify: `curl -s localhost:8190/health`.

## Configuration

Every variable is optional; the defaults match the previous implementation.

| Variable | Default | Meaning |
| --- | --- | --- |
| `HOST` | `127.0.0.1` | Listen address, bound as given. `0.0.0.0` accepts every interface. |
| `PORT` | `8190` | Listen port. |
| `COMFY_URL` | `http://127.0.0.1:8188` | ComfyUI base URL. |
| `COMFYUI_DIR` | *(empty)* | ComfyUI install dir **on this host**. Locates model roots and `extra_model_paths.yaml`. Unset: ComfyUI's own `GET /internal/folder_paths` is used. |
| `COMFY_PYTHON` | *(empty)* | Interpreter shown in the "runtime deps" hint. |
| `COMFY_MODELS_SHARED` | *(empty)* | `1` = the gateway can write into ComfyUI's model folders (same machine, or a mount). Model installs stay off without it. |
| `WORKFLOW_DIR` | `<root>/workflows` | Registry directory. |
| `DATA_DIR` | `<root>/data` | Tasks, locks, preferences. |
| `OUTPUT_DIR` | `<data>/outputs` | Generated files served under `/files/`. |
| `PUBLIC_BASE_URL` | `http://HOST:PORT` | Absolute prefix for returned URLs. |
| `CORS_ORIGINS` | canvas + ComfyUI origins | Extra allow list for canvases on another host. A canvas on the gateway's own host is allowed by rule. `*` is refused. |

`HOST` is not a policy switch: `HOST=192.168.3.232` binds the LAN address exactly like
`127.0.0.1` binds loopback, and the gateway says so in a startup warning when that address is
reachable from the network and no `GATEWAY_TOKEN` is set. Browser access is allowed when the
canvas is served from the same host name as the gateway — the usual `:3000` canvas and `:8190`
gateway — so a LAN address needs no `CORS_ORIGINS` entry; a canvas on a *different* host does.

| `GATEWAY_TOKEN` | *(empty)* | When set, `/api/*` requires the `X-Gateway-Token` header. `/v1/*` never does. |
| `REQUEST_TIMEOUT_MS` | `600000` | Image generation timeout. |
| `VIDEO_TIMEOUT_MS` | `0` | Video timeout; `0` means no limit. |
| `POLL_INTERVAL_MS` | `750` | ComfyUI history poll interval. |
| `SCAN_ON_START` | `1` | Scan ComfyUI for nodes and models at boot. |
| `TASK_RETENTION_DAYS` | `0` | Prune finished tasks older than this; `0` keeps everything. |
| `MAX_STORED_TASKS` | `0` | Cap on stored tasks; `0` is unlimited. |
| `MAX_UPLOAD_BYTES` | `52428800` | Per-file upload limit. |
| `RESOURCE_CONCURRENCY` | `2` | Parallel downloads during a resource install. |
| `PROMPT_MAX_TOKENS` | `8192` | Completion budget for `/v1/prompts/*`. Must cover reasoning plus the answer on a thinking model. |
| `PROMPT_THINKING_PARAM` | `none` | `none` sends only standard OpenAI fields. `auto` learns per endpoint. Or pin `chat_template_kwargs` / `enable_thinking` / `reasoning_effort`. |

### The gateway runs beside ComfyUI

An install is a plain file write into ComfyUI's model folders, so the gateway belongs on the
ComfyUI machine, or on a host where those folders are mounted. Whether that is true cannot be
discovered from inside the process — a mount at another path looks exactly like a remote machine,
and two machines that both install to `/opt/ComfyUI` look exactly like one — so it is configured,
not detected:

| `COMFY_MODELS_SHARED` | Behaviour |
| --- | --- |
| `1` | the gateway may write into ComfyUI's model folders: installs allowed, nothing is inspected |
| unset / anything else | installs off: `GET /api/resources/status` reports `modelInstall { allowed: false, comfyHost }`, the canvas shows a banner and disables the download buttons, and `POST /api/resources/install` answers `400 model_install_disabled` naming `COMFY_URL`'s host |

Fail-closed on purpose. The failure this prevents is not one the user can react to: writing into a
path that happens to exist on this host succeeds, several gigabytes land on the wrong disk, and
ComfyUI still reports the model as missing with nothing in the log. One line in `.env` — shipped
filled in by `.env.example`, which is the single-machine template — is cheaper than that.

### What works, and what does not, when the gateway is not on the ComfyUI machine

Everything the gateway *reads over HTTP* works from anywhere; everything that touches ComfyUI's
filesystem does not, and no environment variable can change that — `COMFYUI_DIR` is a path on the
gateway's own host by definition.

| Feature | Path |
| --- | --- |
| Generation, uploads, fetching images/videos, queue, system stats | HTTP (`/prompt`, `/upload/image`, `/view`, `/queue`, `/system_stats`) |
| Which models are installed (panel "present/missing") | HTTP (`/object_info`) |
| Installing models and custom nodes | **local write** — needs `COMFY_MODELS_SHARED=1` and the folders on this host |
| `extra_model_paths.yaml` (mapped drives, NFS model dirs) | **local read** of `COMFYUI_DIR` and its parent |
| "Runtime deps" interpreter hint | **local read** of `/proc` |
| Replacing a model via the file picker | **local** GUI dialog |

So a gateway on another machine still generates images; it just cannot install models, and it says
so instead of filling the wrong disk. Run it beside ComfyUI when you want the whole panel.

Workflows are the gateway's own: `workflows/registry.json` plus its graphs. There is no polling and
no automatic import of ComfyUI's workflow folder. To add a graph made in ComfyUI, export it in
**API format** (the UI format ComfyUI saves is rejected) and drop the file into `workflows/`, then
scan with `POST /api/resources/refresh` — the refresh button in the canvas's *Add workflows* tab.
One scan both registers the graph and clears its availability, which is why it is the only scan
route: no "scan only the workflow folder" endpoint, no reload endpoint and no background poller.

The file name *is* the workflow's identity, and it has to say what it is:

```
workflows/image-my-flow.json      id = name = image-my-flow, file = image-my-flow.json
workflows/video-h3-t2v.json       id = name = video-h3-t2v, file = video-h3-t2v.json
```

`<type>-<slug>.json`, where `<type>` is `image`, `video`, `audio` or `text` and `<slug>` is
lowercase letters, digits, `-` or `_`. Nothing is copied, renamed or re-slugged — the file you edit
is the file that runs, and `workflows/` holds no duplicates. A file that breaks the rule (no
prefix, wrong prefix for the graph it contains, or capitals and spaces in the name) is **not
registered**; the scan reports it as `workflow files left unregistered` with the reason, and the
fix is to rename the file.

Anything a graph cannot describe — a prompt binding, presets — is left for you to add in
`registry.json`; the scan warns when a registered graph has no prompt binding or no output node.

## Endpoints

Generation (multipart where media is involved):

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/images/generations` | Text-to-image. |
| `POST` | `/v1/images/edits` | Reference-image editing, inpaint, outpaint. |
| `POST` | `/v1/images/upscale` | Image upscaling. |
| `POST` | `/v1/videos` | Video generation (returns a job id). |
| `POST` | `/v1/videos/upscale` | Video upscaling. |
| `POST` | `/v1/videos/interpolate` | RIFE frame interpolation (2x / 3x). |
| `GET` | `/v1/videos/{id}` | Job status, including preview and step progress. |
| `GET` | `/v1/videos/{id}/content` | Finished video bytes. |
| `POST` | `/v1/videos/{id}/cancel` | Cancel a job. |
| `POST` | `/v1/prompts/optimize` | Rewrite a prompt for H3 video. |
| `POST` | `/v1/prompts/optimize-image` | Rewrite a prompt for image generation. |
| `POST` | `/v1/prompts/caption` | Reverse-engineer a prompt from an image. |

Metadata and management:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/health` | Liveness, ComfyUI reachability, counts. |
| `GET` | `/v1/models` | Registered workflows as OpenAI models. |
| `GET` | `/api/workflows` | Full workflow list with availability. |
| `GET` | `/api/resources/status` | Model and node install status. |
| `POST` | `/api/resources/install` | Start an install job. |
| `POST` | `/api/resources/refresh` | The only rescan: reloads the resource index, re-scans ComfyUI (nodes, models, availability, new workflow graphs) and returns the resource status. This is the panel's refresh button. |
| `GET` | `/api/setup/status` | Last scan result. |
| `GET` | `/api/system/stats` | GPU and RAM, plus the last OOM. |

`/api/previews/{promptId}` and `/api/progress/{requestId}` serve in-flight preview
frames and step counts; both are best-effort and never fail a generation.

### Prompt endpoints and thinking models

`/v1/prompts/*` rewrites text, so a chain of thought is pure overhead — and one
specific failure. A thinking model writes its reasoning to `reasoning_content` and
only starts `content` when it finishes; if `max_tokens` runs out first, the answer is
empty. Measured against Qwen3.8-27B on the real H3 rewrite: ~900 reasoning tokens
typically, with a long tail, ~8 s with thinking off and ~19 s with it on.

Two independent levers, both optional:

- `PROMPT_MAX_TOKENS` (default 8192) is the safety net. It is a standard field, so it
  works everywhere; too small and the answer never starts.
- `PROMPT_THINKING_PARAM` shortens the call by asking for a direct answer. Its values
  are **not** part of the OpenAI API — `chat_template_kwargs` is vLLM/SGLang,
  `enable_thinking` is the llama.cpp/Qwen convention — so the default is `none` and no
  vendor field is ever sent. `auto` sends nothing until an endpoint proves it is a
  thinking model, then remembers the working mechanism per endpoint and retries a
  rejected field once without it.

## Architecture

```
main.go                  startup, signal handling, task cleanup loop
internal/config          environment -> immutable Config
internal/httpx           error type, JSON responses, middleware, multipart
internal/comfy           ComfyUI HTTP client + minimal WebSocket client
internal/workflow        registry, graph model, bindings, base-model variants
internal/generate        runner, LoRA injection, step planning, previews, RIFE
internal/setup           availability scan, model aliases, model path resolution
internal/task            async job queue for video work
internal/resource        resources/index.json, install jobs, main-model override
internal/prompt          H3 prompt rewriting, speaker tags, MOSS layout prompts
internal/store           sessions, skins, workflow visibility, main models
internal/sysinfo         GPU and memory sampling
internal/server          route table and wiring
```

Three design notes worth knowing before changing anything:

- **Graphs are generic JSON.** There are nineteen ComfyUI graphs and they carry node
  classes this codebase will never model. `internal/workflow` offers typed accessors
  (`Node.ClassType()`, `Node.Inputs()`) and everything above it is typed.
- **Bindings keep their raw JSON.** `registry.json` is user-editable, so a binding
  is a thin typed view over the raw object. A closed struct would silently drop
  fields when a newer workflow uses them.
- **Request-scoped copies only.** `ApplyBindings` and `ApplyLoraChain` always clone.
  The graphs loaded from disk are immutable, so concurrent requests cannot see each
  other's parameters.

## Language policy

The backend is English-only: log lines, error messages and warnings are all English,
and no Go package is localized. Translation lives exclusively in the canvas frontend.
The same rule covers the rest of the repository — documentation, code comments and
commit messages are English too, with UI copy and model-facing data as the only
exceptions (see [`decisions.md`](decisions.md) #17 and the Language section of the
[README](../README.md)).

Error `code` values are stable (`model_not_found`, `missing_reference_image`,
`LIMIT_FILE_SIZE`, …), so the frontend can map them to localized text instead of
displaying `error.message` verbatim.

Two things stay Chinese on purpose, because they are *data* rather than messaging:

- **Model-facing prompts** in `internal/prompt` — the H3 writing instructions, the image
  and caption system prompts, and the user-turn labels are input to a model, not text
  for a human. Translating them would change generation behaviour.
- **Chinese-matching regexes and mode names** — patterns that strip Chinese reasoning
  preambles from model output, infer bindings from Chinese node titles, or renumber
  `图片N` references. The H3 mode strings (`文生视频(T2VA)` …) are protocol values the
  frontend sends and are matched, not displayed.

## Testing

```bash
make check        # gofmt check + go vet + full suite with -race
make test         # fast suite
make verify-static
```

The suite covers binding application, LoRA injection, prompt sanitising, registry
loading, the task queue (including cancellation and retention), configuration
validation, resource index validation, and an end-to-end HTTP test that drives real
image generation against an in-process fake ComfyUI.

## Intentional differences from the Node gateway

- **Runtime pip installs are gone.** The gateway used to write into the user's venv
  and regularly corrupted it. `/api/resources/status` now reports `runtimeDeps` and
  the UI shows the exact command to run instead.
- **No remote resource index.** The panel reads `resources/index.json` only.
- **Memory is reported as `MemAvailable`.** Using `MemFree` made "used" read as
  almost the whole machine and made the freed-bytes figure meaningless.
- **`mainModel.options` ordering** may differ slightly from `localeCompare` in the
  old build. The option set is identical; only the dropdown order varies.
- **Bake artifacts** (`type: "bake"`) are never installed. They need a local merge, so the
  panel shows the exact command (`tools/bake-moss-preset.py`, exposed as `manualCommand` on
  the resource) instead of a download button.
