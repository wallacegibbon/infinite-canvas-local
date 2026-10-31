# comfyui-gateway (Go)

A local gateway that runs ComfyUI workflows for the Infinite Canvas. It is the repository root: `main.go`, `internal/`, and the runtime
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
| "Pick a model file" dialog | `zenity` | the request fails with a message telling you to type the relative path |

`start.sh` is a bash script, so Windows needs WSL or Git Bash; running `bin/comfyui-gateway`
by hand works anywhere.

## Quick start

```bash
make build          # -> bin/comfyui-gateway (static, ~2 MB)
GATEWAY_HOST=192.168.3.232 ./bin/comfyui-gateway
```

Run it from the repository root: the asset root (`workflows/`, `resources/`, `prompts/`)
is discovered from the working directory first, then the executable's directory. Set
`GATEWAY_ROOT` to override.

Verify: `curl -s localhost:8190/health`.

## Configuration

Every variable is optional; the defaults match the previous implementation.

| Variable | Default | Meaning |
| --- | --- | --- |
| `GATEWAY_HOST` | `127.0.0.1` | Gateway listen address, bound as given. `0.0.0.0` accepts every interface. |
| `GATEWAY_PORT` | `8190` | Gateway listen port. |
| `COMFY_URL` | `http://127.0.0.1:8188` | ComfyUI base URL. |
| `INSTALL_INTO_COMFY` | *(empty)* | `1` = this gateway can write into ComfyUI's folders (`models/`, `custom_nodes/`) because it runs there or sees them through a mount. Installs stay off without it. |
| `WORKFLOW_DIR` | `<root>/workflows` | Registry directory. |
| `DATA_DIR` | `<root>/data` | Tasks, locks, preferences. |
| `OUTPUT_DIR` | `<data>/outputs` | Generated files served under `/files/`. |
| `PUBLIC_BASE_URL` | `http://GATEWAY_HOST:GATEWAY_PORT` | Absolute prefix for returned URLs. |
| `CORS_ORIGINS` | canvas + ComfyUI origins | Extra allow list for canvases on another host. A canvas on the gateway's own host is allowed by rule. `*` is refused. |

`GATEWAY_HOST` is not a policy switch: `GATEWAY_HOST=192.168.3.232` binds the LAN address exactly
like `127.0.0.1` binds loopback, and the gateway says so in a startup warning when that address is
reachable from the network and no `GATEWAY_TOKEN` is set. Browser access is allowed when the
canvas is served from the same host name as the gateway — the usual `:3000` canvas and `:8190`
gateway — so a LAN address needs no `CORS_ORIGINS` entry; a canvas on a *different* host does.

| `GATEWAY_TOKEN` | *(empty)* | When set, `/api/*` requires the `X-Gateway-Token` header. |
| `REQUEST_TIMEOUT_MS` | `600000` | Image generation timeout. |
| `VIDEO_TIMEOUT_MS` | `0` | Video timeout; `0` means no limit. |
| `AUDIO_TIMEOUT_MS` | `0` | Audio/music timeout; `0` means no limit. A song is the same order of work as a short video, so it gets the same default. |
| `POLL_INTERVAL_MS` | `750` | ComfyUI history poll interval. |
| `SCAN_ON_START` | `1` | Scan ComfyUI for nodes and models at boot. |
| `TASK_RETENTION_DAYS` | `0` | Prune finished runs (and uploads) older than this; `0` keeps everything. |
| `MAX_STORED_TASKS` | `0` | Cap on stored tasks; `0` is unlimited. |
| `MAX_UPLOAD_BYTES` | `52428800` | Per-file upload limit. |
| `RESOURCE_CONCURRENCY` | `2` | Parallel downloads during a resource install. |
| `ALAYACORE_BIN` | *(found)* | The agent binary. Empty looks on `PATH`, then beside the gateway. |
| `AGENT_CONFIG_PATH` | `UserConfigDir()/comfyui-gateway/alayacore` | The agent's own config directory (`model.conf`, …). The gateway names it and never reads or writes it; AlayaCore creates it on first spawn. |

### The gateway runs beside ComfyUI

An install is a plain file write into ComfyUI's model folders, so the gateway belongs on the
ComfyUI machine, or on a host where those folders are mounted. Whether that is true cannot be
discovered from inside the process — a mount at another path looks exactly like a remote machine,
and two machines that both install to `/opt/ComfyUI` look exactly like one — so it is configured,
not detected:

| `INSTALL_INTO_COMFY` | Behaviour |
| --- | --- |
| `1` | the gateway may write into ComfyUI's folders: installs allowed, nothing is inspected |
| unset / anything else | installs off: `GET /api/resources/status` reports `modelInstall { allowed: false, comfyHost }`, the canvas shows a banner and disables the download buttons, and `POST /api/resources/install` answers `400 model_install_disabled` naming `COMFY_URL`'s host |

Fail-closed on purpose. The failure this prevents is not one the user can react to: writing into a
path that happens to exist on this host succeeds, several gigabytes land on the wrong disk, and
ComfyUI still reports the model as missing with nothing in the log. One line in `.env` — shipped
filled in by `.env.example`, which is the single-machine template — is cheaper than that.

### What works, and what does not, when the gateway is not on the ComfyUI machine

Everything the gateway *reads over HTTP* works from anywhere; everything that touches ComfyUI's
filesystem does not, and no environment variable can change that — a folder path is a folder path
on the gateway's own host by definition.

| Feature | Path |
| --- | --- |
| Generation, uploads, fetching images/videos, queue, system stats | HTTP (`/prompt`, `/upload/image`, `/view`, `/queue`, `/system_stats`) |
| Which models are installed (panel "present/missing") | HTTP (`/object_info`) |
| Installing models and custom nodes | **local write** — needs `INSTALL_INTO_COMFY=1` and the folders on this host |
| `extra_model_paths.yaml` (mapped drives, NFS model dirs) | **local read** under ComfyUI's own directory |
| Knowing where to write at all | ComfyUI's reported folder layout (`custom_nodes`) |
| "Runtime deps" interpreter hint | **local read** of `/proc` |
| Replacing a model via the file picker | **local** GUI dialog |

So a gateway on another machine still generates images; it just cannot install models, and it says
so instead of filling the wrong disk. Run it beside ComfyUI when you want the whole panel.

An install also needs to know *where* ComfyUI is, and the only thing that knows is ComfyUI: the
`custom_nodes` entry of `GET /internal/folder_paths`. There is no configuration override and no
fallback inference — model folders can live outside the tree (`extra_model_paths`), so deriving a
root from them writes custom nodes into the wrong place, silently. A build that does not report
`custom_nodes` cannot be installed into: the request answers `400 install_root_unknown`, names the
version ComfyUI reported, and says what to fix. No minimum version is documented, because the
capability is checked rather than a number.

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

Anything a graph cannot describe — a prompt binding, presets, and the media a workflow accepts —
is left for you to add in `registry.json`; the scan warns when a registered graph has no prompt
binding or no output node. Media acceptance (`accepts`, `frameMode`, `mode`, `promptInject`,
`promptReorder`, `sourceVideoFrames`) is validated by one function, so a new workflow declares its
contract instead of adding a branch to a handler. `GET /api/workflows/{id}/schema` publishes the
same contract as typed `inputs`, which is what `POST /api/runs` validates a request against.

## Endpoints

Prompts are the agent's, not the gateway's: these routes carry what the canvas wants asked and stream
back what AlayaCore says. The gateway owns no wording, no model choice and no state — see
[`decisions.md`](decisions.md) #75 for why, and AlayaCore's own `adapter-guide` for the frames.

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/api/agent/connect` | Start an agent under a name, or return the one already filed there. |
| `POST` | `/api/agent/chat` | Write one request to that agent and stream its frames back. |
| `POST` | `/api/agent/once` | Ask one question of a process of its own, released when the answer ends. |
| `DELETE` | `/api/agent/session/{name}` | Release an agent. This is the only thing that ends one. |

Both chat routes take `prompt` (and images), or `command` + `args` (one of AlayaCore's own commands,
forwarded verbatim), or both, and answer with an event stream: `models`, `delta`, `reasoning`, `model`,
`reasoning_level`, `command`, `tool_call`, `tool_result`, `done`, `error`.

They are two kinds of thing, and the difference is lifetime. `/api/agent/chat` requires a `session` and
writes into the process filed under that name — one per canvas, alive until the client disconnects, so
its history accumulates and its tools can be used. Changing a model or a reasoning level goes the same
way: a frame to that process, not a question that spends it. `/api/agent/once` names no session and reads
none: it spawns `alayacore --rawio`, asks, and releases the process when the answer ends. That split is
what keeps a prompt rewrite or a caption — a single answer that nothing follows — from spending the
history of the agent the user is working with.

The name on `connect` is the client's to choose (the canvas id, in practice). Connecting again with the
same name returns the same process, which is how a reloaded page finds the conversation it left, and it
means the gateway can stay ignorant of what a canvas is. Nothing expires: a process lives until the
client releases it, or until it dies — see [`decisions.md`](decisions.md) #80 for the cost of that.

A built-in tool (`read_file`, enabled for the skills the agent loads) runs inside AlayaCore, so the call
and its result arrive as frames and the gateway relays both without answering either.

A music graph needs two texts, and both are ordinary `registry.json` bindings (`prompt`, `lyrics`), so
a run carries them as inputs like any other parameter. `response_format`-style container selection is
`outputs.formats` in the registry: the encoder writes `mp3`, `flac` or `opus`, and anything else is
refused with `unsupported_audio_format` at submission instead of being silently converted.

Runs (the protocol in [`protocol.md`](protocol.md); every canvas node and tool uses them — frame
interpolation included, as a run of the `video-rife` workflow):

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/api/uploads` | Upload one media file. Returns an id a run can reference by role. |
| `GET` | `/api/uploads` | Uploads that have not expired, newest first. |
| `POST` | `/api/runs` | Submit a run. Always `202` with the run, never blocks. |
| `GET` | `/api/runs` | Runs, newest first; `?status=` and `?workflow=` filter. |
| `GET` | `/api/runs/{id}` | Status, progress, preview frame, outputs, error. |
| `GET` | `/api/runs/{id}/outputs/{index}` | One artifact. |
| `GET` | `/api/runs/{id}/preview` | In-flight preview frame, when one exists. |
| `DELETE` | `/api/runs/{id}` | Cancel (idempotent; also interrupts ComfyUI). |

Metadata and management:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/health` | Liveness, ComfyUI reachability, counts. |
| `GET` | `/api/workflows` | Full workflow list: availability, typed parameters, and the model ids each workflow answers to (`models`). Every registered workflow, tool or model. |
| `GET` | `/api/resources/status` | Model and node install status. |
| `POST` | `/api/resources/install` | Start an install job. |
| `POST` | `/api/resources/refresh` | The only rescan: reloads the resource index, re-scans ComfyUI (nodes, models, availability, new workflow graphs) and returns the resource status. This is the panel's refresh button. |
| `GET` | `/api/tasks` | Every run the gateway has queued or finished, newest first. |
| `GET` | `/api/tasks/{id}` | One run by id. |
| `GET` | `/api/setup/status` | Last scan result. |
| `GET` | `/api/system/stats` | GPU and RAM, plus the last OOM. |

`/api/previews/{promptId}` and `/api/progress/{requestId}` serve in-flight preview
frames and step counts; both are best-effort and never fail a generation. A run also carries its own
`preview` (`{url, mime}` with a timestamp) in the payload, so a client polling a run needs no second
endpoint to show one.

`loras` is the one input the engine applies itself rather than a binding. Everything else a run
carries reaches a node.

### Prompt models are the agent's

The gateway has no prompt model of its own: `/api/agent/chat` and `/api/agent/once` spawn AlayaCore,
and every question about models — which exist, which is active, how hard it should think — is answered
by the agent's own `model.conf` and its `reasoning_0/1/2` blocks. Nothing here configures a provider,
negotiates a thinking parameter or retries a rejected field: AlayaCore does, and the gateway relays
what it says.

## Architecture

```
main.go                  startup, signal handling, task cleanup loop
internal/config          environment -> immutable Config
internal/httpx           error type, JSON responses, middleware, multipart
internal/comfy           ComfyUI HTTP client + minimal WebSocket client
internal/workflow        registry, graph model, bindings, base-model variants
internal/generate        runner, LoRA injection, step planning, previews
internal/setup           availability scan, model aliases, model path resolution
internal/task            the run queue: persistence, progress, cancel, multi-output records
internal/upload          uploaded media: bytes on disk, index, cached ComfyUI reference
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

## Relationship to ComfyUI's HTTP API

The gateway sits on top of ComfyUI, so the job skeleton is ComfyUI's: submit, get a prompt id, poll
until done, fetch the artifact. `internal/comfy/client.go` speaks exactly that set of endpoints —
`POST /prompt`, `GET /history/{prompt_id}`, `GET /view`, `POST /upload/image`, `POST /interrupt`,
`GET /queue`, `GET /object_info`, and a WebSocket for progress. What is ours is the abstraction above
it: a caller names a **workflow** and its inputs and never sees a node graph.

| ComfyUI | Gateway |
| --- | --- |
| `POST /prompt` with a full API-format graph | `POST /api/runs` with `{workflow, inputs, media}` |
| `prompt_id` | `run.id` |
| `POST /upload/image` → file name | `POST /api/uploads` → upload id, referenced by role |
| `GET /queue` (running/pending) | `GET /api/runs?status=`, `GET /api/tasks` |
| `GET /history/{prompt_id}` | `GET /api/runs/{id}` |
| `GET /view?filename=&subfolder=&type=` | `GET /api/runs/{id}/outputs/{index}` |
| `POST /interrupt` + queue deletion | `DELETE /api/runs/{id}` (idempotent, reports `cancelled` at once) |
| WebSocket `executing` / `progress` / binary preview | `GET /api/runs/{id}` progress + `GET /api/runs/{id}/preview` (polled; the socket stays internal) |
| `GET /object_info` (node classes and their file options) | `GET /api/workflows[/{id}/schema]` (runnable capabilities, availability, typed inputs) |

Where the two differ, and why:

- **What is submitted.** ComfyUI takes a graph: node ids, classes, every link. The gateway takes
  `{workflow, inputs, media}`; the graph lives in `workflows/*.json` and bindings translate inputs
  into it (`internal/workflow/bindings.go`). This is the difference that produces all the others: the
  client that knows a workflow id does not need to know `"37:13"` exists. Raw graphs are refused on
  purpose — the exposed surface is the registered capability set, not the executor.
- **When a request is judged.** ComfyUI validates a graph when it executes. The gateway validates at
  submission against the schema: closed key set, types, ranges, `required`, media counts and roles
  (`internal/workflow/inputs.go`, `media.go`). A `400` now instead of a failed run later.
- **How media is referenced.** In ComfyUI an upload returns a filename that the graph has to place in
  the right socket, which means the client must understand the wiring. Here an upload returns an id
  and the run references it by **role** (`source`, `mask`, `first_frame`, `last_frame`, `reference`,
  `video`, `audio`); the workflow's bindings decide which socket it lands on.
- **Where artifacts live.** `/view` needs the filename and subfolder from ComfyUI's own history and
  serves from its output directory. The gateway copies results into `data/outputs` and addresses them
  by index with their MIME type and size, so a finished run stays fetchable after ComfyUI cleans up or
  restarts.
- **State.** ComfyUI's queue and history are in memory. Runs are persisted to `data/tasks.json` and
  reconciled on boot (file present → completed, in flight → failed with an explanation); uploads have
  their own index and retention.
- **Progress.** ComfyUI pushes it over the WebSocket. The gateway consumes that socket and exposes
  polling, so a browser needs no long-lived connection and a reload loses nothing.
- **Cancellation.** ComfyUI has a global interrupt plus queue deletion. A run has its own idempotent
  `DELETE`, which maps to both and is reflected immediately.
- **Discovery.** `/object_info` describes node classes and their file options. `/api/workflows`
  describes *runnable capabilities*: whether the models and nodes are installed, the modes/variants,
  and the typed inputs — it is computed from `object_info`, but it answers a different question.
- **Trust boundary.** ComfyUI is single-user and unauthenticated. The gateway has an origin allow
  list and an optional token, and it is the only surface meant to be reachable from a browser.

The same request, both ways — one image:

```jsonc
// ComfyUI POST /prompt
{"prompt": {
  "4": {"class_type": "CLIPTextEncode", "inputs": {"text": "a cat", "clip": ["1", 1]}},
  "1": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "x.safetensors"}},
  "7": {"class_type": "KSampler", "inputs": {"seed": 42, "steps": 8, "model": ["1", 0], "positive": ["4", 0]}},
  "9": {"class_type": "SaveImage", "inputs": {"images": ["7", 0], "filename_prefix": "out"}}
}, "client_id": "..."}

// Gateway POST /api/runs
{"workflow": "image-z-image-turbo",
 "inputs": {"prompt": "a cat", "size": "1024x1024", "count": 1, "quality": "medium"}}
```

The resemblance is deliberate where it is: single queue, single GPU, submit → id → poll → fetch is
what ComfyUI actually offers and there is nothing to gain from inventing a different lifecycle. The
difference is the unit of exchange — ComfyUI exchanges graphs, the gateway exchanges workflow runs.

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
