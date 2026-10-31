# The gateway protocol

Status: **target, step 1 landed.** Written before the code so every step has an acceptance
criterion.

- Done: one execution core (`internal/server/runs.go`) over the task store. Video and audio both
  submit through it; an audio request is now a queued run that the `/v1/audio/speech` adapter waits
  on, so the song survives a dead connection, is listed in `/api/tasks`, and keeps its ComfyUI
  error code and details when it fails.
- Done: the media half of the contract is data. `accepts` / `frameMode` / `mode` /
  `promptInject` / `promptReorder` / `sourceVideoFrames` live in `registry.json` and are resolved by
  `workflow.PlanMedia`; no handler mentions a workflow id any more. `outputs.formats` declares the
  containers a workflow's encoder can write (mp3/V0, flac, opus/128k), so the audio adapter no longer
  hardcodes them.
- Done: `/api/runs` and `/api/uploads` exist with the shape below — submit, status, outputs, preview
  and cancel, media referenced by upload id and role, one row per artifact (a batch image request
  returns several). Audio is already an adapter on the same execution path.
- Next: typed `inputs` per workflow (type, range, enum) so submission rejects a bad value with a
  `400` instead of failing inside ComfyUI; then the canvas moves node type by node type (audio,
  image, video) and `/v1/*` is deleted. Until the canvas has moved, `POST /api/runs` accepts the
  generic `prompt`/`negative`/`size`/`count`/`quality`/`seconds`/`mode` keys plus any binding key
  passed through `inputs`, which is the same mapping the adapters use.

The routes below are the contract to build against; where a route already exists with the same
name (`/api/workflows`, `/api/tasks`), it is the real thing.

The canvas is the only client. A protocol aimed at one known caller can describe *our* domain
instead of imitating someone else's API, and every compatibility branch we do not need is code
we would maintain forever.

## Four resources

```
workflow   what can run        read-only catalog, from workflows/registry.json
upload     what goes in        a media file with an id, uploaded once, referenced by id
run        one execution       created by POST, owned by an id, terminal once
output     what comes out      part of a run; bytes hang off the run
```

That is the whole model. Media type is **data**, not routing: an image, a video and a song are
three runs of three workflows, not three families of endpoints.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/workflows` | Catalog, including availability. |
| `GET` | `/api/workflows/{id}/schema` | Machine-readable inputs, media roles, outputs. |
| `POST` | `/api/uploads` | Multipart upload of one media file. Returns an upload id. |
| `POST` | `/api/runs` | Submit a run. Always returns `202` with the run. |
| `GET` | `/api/runs/{id}` | Status, progress, outputs, error. |
| `GET` | `/api/runs/{id}/outputs/{index}` | Output bytes. |
| `GET` | `/api/runs/{id}/preview` | Best-effort in-flight preview frame (may be absent). |
| `DELETE` | `/api/runs/{id}` | Cancel. Idempotent; also interrupts ComfyUI. |
| `GET` | `/api/runs` | List/filter runs (`status`, `workflow`), newest first. |

`/health`, `/api/resources/*`, `/api/setup/*`, `/api/system/*`, `/api/session/*` are unchanged:
they manage the gateway, they do not run workflows.

## Submitting a run

```json
{
  "workflow": "audio-minimax-music-3",
  "inputs": {
    "prompt": "indie folk, fingerpicked",
    "lyrics": "[Verse]\nline one",
    "seed": 42,
    "duration": 60
  },
  "media": {
    "reference": ["upl_9f01"],
    "first_frame": ["upl_2b77"],
    "mask": ["upl_7c31"]
  }
}
```

- **`inputs` keys are the workflow's binding keys.** They are described by
  `GET /api/workflows/{id}/schema` (type, default, min/max, enum, required) and validated by one
  generic checker. There is no `params` escape hatch and no per-workflow branch in the handler.
- **`media` keys are roles, not file names.** The workflow declares which roles it accepts and how
  many (`accepts`), so `mask` and `first_frame` are expressible and a wrong role is a `400`, not a
  silently ignored file.
- When a workflow has modes (`image-qwen21` has edit/inpaint/outpaint/t2i), `mode` selects one and
  the **first mode is the default**. The variant carries its own graph, bindings and media contract,
  so an inpaint request means "source + mask" without the handler knowing the workflow.
- No `wait`, no `stream`, no synchronous variant: see "The id is the contract" below.

Response (`202` in every case, including a run that finished instantly). `status` is the state at
the moment the response is written, so a queue that picks the run up immediately answers `running`:

```json
{
  "id": "run_6f2c",
  "workflow": "audio-minimax-music-3",
  "status": "queued",
  "progress": { "phase": "queued", "step": 0, "total": 30 },
  "outputs": [],
  "error": null,
  "created_at": "2026-10-31T06:00:00Z",
  "started_at": null,
  "finished_at": null
}
```

## Reading a run

`status` is one of `queued`, `running`, `succeeded`, `failed`, `cancelled`. Terminal statuses never
change again. `GET /api/runs/{id}`:

```json
{
  "id": "run_6f2c",
  "status": "succeeded",
  "progress": { "phase": "done", "step": 30, "total": 30 },
  "outputs": [
    {
      "index": 0,
      "kind": "audio",
      "mime": "audio/mpeg",
      "bytes": 118811,
      "filename": "run_6f2c.mp3",
      "duration_ms": 4000,
      "url": "/api/runs/run_6f2c/outputs/0"
    }
  ],
  "error": null
}
```

One download route for every kind of artifact. There is no base64 mode, no `/files/{name}`
side channel and no per-media content endpoint.

## Errors

```json
{ "error": { "code": "unsupported_media_role", "message": "…", "details": {} } }
```

`code` is the contract (stable, documented, mapped to localized text by the canvas). HTTP status
carries the class: `400` bad request, `404` unknown workflow/run/upload, `409` wrong state,
`422` invalid workflow file, `502` ComfyUI failure, `504` timeout. No `type` field: it would only
repeat the status code.

## What a run may carry, exactly

`GET /api/workflows/{id}/schema` returns `inputs`: the closed list of keys a run accepts, with type,
required, range and (where the gateway owns the value set) the allowed options. A run is validated
against it **at submission**, so a mistake is a `400` now rather than a failed run minutes later.

The list is assembled from three sources, so no workflow has to restate it:

1. Request-level keys: `prompt` (required whenever the workflow has a prompt binding), `negative`,
   `size`, `count`/`n`, `quality`, `seconds`/`duration`, `mode`.
2. The workflow's declared `parameters` (`seed`, `steps`, `cfg`, `sampler`, …), whose ranges are
   authoritative.
3. Every remaining non-media binding key (`unet`, `format`, `lyrics`, a LoRA list …), typed from
   the binding when its intent is obvious.

Rules worth knowing:

- **Unknown keys are rejected**, with the accepted list in the message. A typo (`setps`) used to be
  ignored silently, which is the worst possible outcome.
- **Media keys are rejected with a pointer**: `refs` is a binding name, but media travels in
  `media.<role>`; the error says so.
- **`quality` is a closed set** (the workflow's preset names plus `auto`) because the gateway owns
  that vocabulary.
- **Other `select` options are advisory.** A sampler list in `registry.json` is a UI suggestion;
  ComfyUI has more valid samplers than the canvas offers, and rejecting one would be wrong.
- Values reach the bindings unchanged, which is why a new workflow becomes runnable without a new
  route or a new struct.

`/v1/*` adapters stay lenient until the canvas has migrated: they keep their old field mapping and
their old clamping, because changing what an existing client gets is a separate decision from adding
the protocol.

## The id is the contract

`POST` hands out the run id at submission time. Everything after that — progress, outputs, cancel —
hangs off it. Consequences:

- A task longer than any HTTP timeout is fine.
- A browser reload, a closed tab, a dropped tunnel: the run keeps going and can be collected later.
- There is exactly one success shape (`202` + run), so a client has exactly one code path.

This is why there is no `wait` parameter. A blocking variant would return two different success
shapes (`200` with outputs, `202` with a run), so every client would still have to implement
polling — while the blocking call itself reintroduces the "connection died, work lost" failure the
id exists to remove.

## Execution model

- Runs are queued and executed **one at a time**. ComfyUI owns a single GPU; parallel gateway
  submissions would only queue inside ComfyUI anyway, with worse diagnostics.
- Every run is persisted (`data/tasks.json`, soon `runs.json`) and reconciled on boot: a run whose
  output file exists becomes `succeeded`, a run that was in flight becomes `failed` with an
  explanation.
- Cancelling interrupts the ComfyUI prompt, so the GPU is actually freed.
- A missing model or node is caught by the availability scan before submission; a run that still
  fails records the ComfyUI error (code, message, details) on the run.

## What this replaces

| Today | Target |
| --- | --- |
| `/v1/images/generations`, `/v1/images/edits`, `/v1/images/upscale` | one `POST /api/runs` with `inputs` + `media` |
| `/v1/videos`, `/v1/videos/{id}`, `/{id}/content`, `/{id}/cancel` | the same `run` endpoints |
| `/v1/audio/speech` | the same `run` endpoints |
| `/v1/videos/upscale`, `/v1/videos/interpolate` | workflows whose `inputs`/`media` say so |
| `mode`, `vquality`, `resolution_name`, `params` (JSON string) | declared `inputs` in the schema |
| field names that must contain `image`/`frame` | explicit media roles |
| `entry.ID == "video-h3-…"`, `image-qwen21`, `image-moss-*` branches in handlers | `accepts` / `inputs` / `outputs` in `registry.json` |
| base64 bodies, `/files/{name}`, `/{id}/content` | one output download route |

`/v1/*` survives only as an adapter while the canvas migrates, one node type at a time. When the
last node is on `/api/runs`, the adapters and their file are deleted.

## Migration order

1. **Execution core** (this step): one submission path over the existing task store; audio and
   video both use it, audio stops being a blocking generation. Existing routes keep working as
   adapters.
2. **Registry as data**: `accepts` / `inputs` / `outputs` + one validator; delete every hardcoded
   workflow-id branch from the handlers.
3. **`/api/runs` + `/api/uploads`**: the target routes land next to the adapters; the canvas moves
   node type by node type (audio first, then image, then video).
4. **Deletion**: adapters, `/v1/*`, dead fields, dead tests, and the docs that described them.

Every phase leaves the repository runnable and `make check` green. Nothing in a later phase
depends on a half-finished earlier one.
