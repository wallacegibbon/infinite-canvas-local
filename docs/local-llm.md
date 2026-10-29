# Local LLM (prompt optimization / image captioning)

"Prompt optimization" and "caption" in the canvas do not use ComfyUI nodes. They call any
**OpenAI-compatible `/chat/completions`** endpoint, so `llama-server` from llama.cpp runs
the whole thing offline.

> The old implementation had a second, "local" path: it submitted a ComfyUI workflow and let
> nodes such as `TE_MAN` do the prompting. Those nodes only ship Windows binaries (`.pyd`)
> which cannot load on Linux, so that entire path — including the canvas's "local" option —
> was removed. HTTP is now the only route, and it does not compete for ComfyUI's VRAM
> scheduler.

## Who calls whom

```text
browser (canvas :3000)
   └─→ gateway :8190   /v1/prompts/*
          └─→ llama-server :8099   POST /v1/chat/completions
```

The tab in the canvas is called **"prompt API"** and only requires an **OpenAI-compatible
endpoint**; pointing it at `127.0.0.1` is completely normal. The gateway is the only party
that sends requests — the browser never contacts llama-server directly.

For the other processes and their endpoints see "Processes and interface ownership" in the
root [`../README.md`](../README.md).

---

## All three `/v1/prompts/*` endpoints are `/chat/completions`

The names invite confusion: the "image" in `optimize-image` means **a prompt for an image
model**, not "optimize an image". All three are **gateway-specific** (OpenAI has none of
them) and all end up in the same `cloudChat()`. Each request sends exactly one
`POST /chat/completions` to the base URL; the differences are the system prompt and whether
an image is attached:

| Gateway endpoint | Purpose | System prompt | Attaches image |
|---|---|---|---|
| `/v1/prompts/optimize` | Video (H3) prompts | `prompts/skills/h3-prompt-writing/`, **≈ 4500 tokens** | Optional, up to 8 |
| `/v1/prompts/optimize-image` | Image prompts | `CLOUD_IMAGE_SYSTEM_PROMPT`, ≈ 200 tokens | **No, text only** |
| `/v1/prompts/caption` | Image → text (captioning) | Caption template | **Always exactly 1** |

Mapping to the canvas: the "optimize" button on an H3 node uses the first, the "optimize"
button on an image node uses the second, and the "caption" button on an image node uses the third.

So `optimize-image` is the easiest smoke test (text only, short prompt, instant answer),
while `/v1/prompts/optimize` needs a large `-c` because of that 4500-token system prompt.

---

## Prerequisites

| Item | Requirement | Notes |
|---|---|---|
| `llama-server` | Any reasonably recent build | Must support `POST /v1/chat/completions` |
| Text model | Any chat GGUF | Prompt expansion only needs text |
| Vision projector | Optional | **Only needed for captioning (image → text)**, via `--mmproj` |
| VRAM / RAM | A 27B IQ2 is about 10 GB | CPU-only works but is roughly 20× slower |

Verify the install:

```bash
llama-server --version          # e.g. version: 0.6.0-dev (build 11467)
```

---

## Steps

### 1. Free the VRAM first

An idle ComfyUI still holds a dozen GB; without freeing it, llama.cpp can only run on the CPU.

```bash
curl -X POST http://127.0.0.1:8190/api/system/free-memory
```

Or click "release VRAM" in the system panel at the top right of the canvas. Measured effect:
VRAM usage drops from **14.6 GB** to **2.2 GB**.

### 2. Start llama-server

```bash
llama-server \
  -m      ~/models/Qwen3.8-27B-GGUF/Qwen3.8-27B-GSQ-RCO-IQ2_S-mtp.gguf \
  --mmproj ~/models/Qwen3.8-27B-GGUF/mmproj-Qwen3.8-27B-BF16.gguf \
  -c 16384 -ngl 99 --host 127.0.0.1 --port 8099
```

| Flag | Meaning | Watch out |
|---|---|---|
| `-m` | Text model GGUF | For sharded models pass only the first shard |
| `--mmproj` | Vision projector, **required for captioning** | Without it you only get text expansion |
| `-c` | Context length | **Must be ≥ 8192**, see pitfall ① |
| `-ngl` | Layers offloaded to the GPU | `99` = all; use `0` for CPU when VRAM is short |
| `--port` | Port | The examples below all use `8099` |
| `--host` | Bind address | `127.0.0.1` if only this machine needs it |

These two lines mean it is up:

```
srv  load_model: loaded multimodal model, '.../mmproj-...gguf'
srv  llama_server: listening on http://127.0.0.1:8099
```

### 3. Fill it in inside the canvas

1. Click **settings** (gear icon) at the top right
2. Switch to the **"prompt API"** tab
3. Click **"add API"** and fill in four fields:

| Field | What to enter |
|---|---|
| Name | Anything, e.g. `local llama.cpp` |
| Base URL | `http://127.0.0.1:8099/v1` |
| API key | Anything (`llama-server` does not check it, but it cannot be empty) |
| Model | Anything (the name is ignored in single-model mode) |

After saving it shows "in use". Note how the base URL is normalized:

- ends with `/v1` → the gateway appends `/chat/completions`
- already contains `/chat/completions` → used as-is
- anything else → the gateway appends `/v1/chat/completions`


---

## Verification

These three are the command-line equivalents of the three canvas buttons; `{"ok":true,...}`
means success:

```bash
C='-F cloudBaseUrl=http://127.0.0.1:8099/v1 -F cloudApiKey=sk-local -F cloudModel=local-model'

# Image prompt expansion
curl -s -X POST http://127.0.0.1:8190/v1/prompts/optimize-image $C \
  -F 'prompt=a cat' -F 'language=zh'        # omitting mode means text-to-image

# Captioning (needs --mmproj)
curl -s -X POST http://127.0.0.1:8190/v1/prompts/caption $C \
  -F 'language=zh' -F 'image=@/path/to/photo.png;type=image/png'

# H3 video prompt (system prompt ≈ 4500 tokens, the context must be big enough)
curl -s -X POST http://127.0.0.1:8190/v1/prompts/optimize $C \
  -F 'prompt=a black cat on a windowsill watching the rain' -F 'mode=文生视频(T2VA)' -F 'language=zh' -F 'duration=5'
```

Testing llama.cpp directly, without the gateway:

```bash
curl -s -X POST http://127.0.0.1:8099/v1/chat/completions \
  -H 'Content-Type: application/json' -H 'Authorization: Bearer sk-local' \
  -d '{"model":"m","messages":[{"role":"user","content":"name three numbers"}],"max_tokens":32}'
```

---

## Pitfalls we have already hit

### ① `-c` must be ≥ 8192

The H3 video system prompt is assembled from `prompts/skills/h3-prompt-writing/`
(`SKILL.md` + `references/base-en.txt` + `ref-en.txt`), **about 4504 tokens**. Too small a
context fails with a 502:

```
cloud API failed: request (4504 tokens) exceeds the available context size (2048 tokens)
```

`-c 16384` is a safe choice (it leaves room for output and reasoning).

### ② Captioning requires `--mmproj`

With only the text model loaded, image content never reaches the model and it makes things
up. Look for `loaded multimodal model` in the startup log — that is how you know vision is on.

### ③ Free the VRAM first, or you get CPU only

While ComfyUI holds 14 GB, `-ngl 99` either fails or silently falls back to the CPU.

### ④ CPU-only works, but it is slow

Same H3 request: about 1.6 tokens/s with `-ngl 0` (ten minutes plus), 21 seconds with `-ngl 99`.

---

## Measured performance

RTX 5080 16 GB, Qwen3.8-27B IQ2_S + mmproj, `-c 16384 -ngl 99`:

| Operation | Input size | Time |
|---|---|---|
| Image prompt expansion (`optimize-image`) | ~200 tokens | 4.1 s |
| Captioning (`caption`, with image) | ~1400 tokens | 10.4 s |
| H3 video prompt (`optimize`) | 4504 tokens | 21 s |

---

## Remember to stop it afterwards

`llama-server` keeps holding about 10 GB of VRAM. ComfyUI will OOM when it loads a large
model, so **stop llama-server before generating images or videos**:

```bash
# find the process by port, so other llama-server instances are left alone
kill $(ss -ltnp 2>/dev/null | grep ':8099' | grep -oE 'pid=[0-9]+' | cut -d= -f2 | head -1)

nvidia-smi --query-gpu=memory.used,memory.total --format=csv,noheader
```

Once VRAM is back to 1–2 GB it is fully released. Conversely, if `llama-server` refuses to
start or suddenly gets very slow, check whether ComfyUI has grabbed the VRAM again (back to
step 1).

Instead of typing the command every time you can wrap it in a systemd user unit, but **do not
enable it at boot** — it competes with ComfyUI for VRAM, and whoever grabs it first wins:

```ini
# ~/.config/systemd/user/llama-server.service
[Unit]
Description=llama-server (prompt optimization)

[Service]
ExecStart=%h/.local/bin/llama-server -m %h/models/<repo>/<model>.gguf --mmproj %h/models/<repo>/<mmproj>.gguf -c 16384 -ngl 99 --host 127.0.0.1 --port 8099
Restart=no

[Install]
WantedBy=default.target
```

```bash
systemctl --user start llama-server     # when you need it
systemctl --user stop llama-server      # before generating
```

---

## Using a different service

Anything that offers an OpenAI-compatible `/chat/completions` is configured the same way,
including:

- Remote services (DeepSeek, OpenAI, a self-hosted vLLM / LM Studio / Ollama OpenAI-compatible endpoint)
- Other local inference frameworks

Local, LAN or remote all work; there is no address restriction.

The gateway reads the standard `choices[0].message.content` field. Some reasoning models put
their thinking into `reasoning_content`; the gateway only reads `content`, so give
`max_tokens` enough headroom for the thinking phase.
