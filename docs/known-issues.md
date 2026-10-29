# Known issues

Open problems, verified against the current tree. Each entry says what breaks, why, and
what the fix is, so nobody has to rediscover it. Fixes that only need a command are
written out as commands.

---

## 1. SeedVR2 workflows cannot load (missing Python packages)

**Symptom** `image-seedvr2-upscale` and `video-seedvr2-upscale` never become available.
ComfyUI's log shows `IMPORT FAILED` for `ComfyUI-SeedVR2_VideoUpscaler`; the canvas reports
"missing node SeedVR2LoadDiTModel".

**Cause** The node is present in ComfyUI's `custom_nodes`, but three of its imports are
missing from the ComfyUI venv. Measured on the reference machine:

```
MISSING diffusers
MISSING omegaconf
MISSING rotary_embedding_torch
OK      cv2
OK      einops
```

**Fix** See [`runtime-deps.md`](runtime-deps.md):

```bash
"<ComfyUI's python>" -m pip install diffusers omegaconf rotary_embedding_torch opencv-python einops
```

Restart ComfyUI afterwards.

---

## 2. Two recorded videos play audio only in the browser

**Symptom** Some clips from before the H.264 switch play sound but show no picture in the
canvas or in a browser.

**Cause** They are 10-bit HEVC (`hevc, Main 10, yuv420p10le`), which browsers other than
Safari cannot decode. The three workflow files were changed to `format: "video/h264-mp4"` +
`pix_fmt: "yuv420p"` (`h3-ref2va-drama`, `h3-ref2va-drama-vdn`, `h3-character-swap-vdn`), but
files recorded earlier keep the old codec.

Still affected in `data/outputs/` (checked with `ffprobe`):

```
task_dcd0fb5a-9ff6-49f5-a580-dc5d98efdec2.mp4   hevc, Main 10, yuv420p10le
task_ec533830-dd05-4839-8f4f-6495d3138e9b.mp4   hevc, Main 10, yuv420p10le
```

**Fix** Transcode in place:

```bash
ffmpeg -i in.mp4 -c:v libx264 -preset medium -crf 20 -pix_fmt yuv420p \
  -c:a copy -movflags +faststart out.mp4
```

`internal/workflow/browser_playback_test.go` guards the shipped workflows against
reintroducing 10/12-bit or H.265, but it cannot cover already-written files.

---

## 3. H3 video prompts ignore the requested output language

**Symptom** The prompt asks for Chinese (`输出语言：字段正文全部使用中文`) and the generated
prompt still comes back in English.

**Cause** Not diagnosed. It was observed with a local `qwen3.8-27b` endpoint with thinking
disabled (`PROMPT_THINKING_PARAM=none`), so the instruction is being sent but is not being
followed — either the model ignores it or the system prompt in
`prompts/skills/h3-prompt-writing/` does not carry enough weight.

**Next step** Reproduce with the same prompt through `curl /v1/prompts/optimize`, compare the
raw completion against the system prompt, and decide whether the fix belongs in the skill
files (stronger instruction) or in `internal/prompt`.

---

## 4. "Restart needed" is shown for a missing custom node

**Where** `infinite-canvas/web/src/components/layout/resource-center-panel.tsx`, the workflow
status tag:

```tsx
workflow.missing ? <Tag>{copy.missing} {workflow.missing}</Tag>
  : workflow.available ? <Tag>{copy.workflowReady}</Tag>
  : <Tag>{copy.workflowRestart}</Tag>          // ← every other "not available" case
```

**Problem** The last branch is the fallback for *any* unavailable workflow, so a workflow
blocked by an uninstalled custom node reads "Restart needed". The label promises a restart,
which will not help.

**Fix** Distinguish the reason (missing nodes vs pending restart) and give the tag its own
copy.

---

## 5. "Canvas entry" tag is shown inverted

**Where** Same file. The `canvasEntry` tag renders when `workflow.modelVisible === false`,
i.e. exactly when the workflow is *hidden* from the model picker.

**Fix** Either invert the condition or rename the label to "Hidden from picker".

---

## 6. 55 hardcoded Chinese strings outside the i18n dictionaries

**Symptom** The English UI still shows Chinese in a few dialogs.

**Cause** Those strings were never routed through `t()`. Remaining, per file:

```
21  components/canvas/canvas-image-settings-popover.tsx
 7  components/canvas/canvas-node-prompt-panel.tsx     (5 of these are protocol values — keep)
 6  pages/canvas/project.tsx
 4  stores/use-config-store.ts
 4  components/layout/resource-setup-gate.tsx
 2  components/layout/app-config-modal.tsx
 2  components/layout/channel-editor-drawer.tsx
 2  components/canvas/canvas-node.tsx
 2  pages/config/index.tsx
 2  components/canvas/canvas-node-moss-layout-dialog.tsx (data: region labels + regex)
 1  components/canvas/canvas-node-hover-toolbar.tsx
 1  services/api/image.ts
 1  components/layout/user-status-actions.tsx           (the 「中」 language glyph — keep)
```

Two batches are done (47 + 10 strings); the rest is mechanical work.

**Careful** Some Chinese is *data*, not copy: the MOSS region labels and their regex, the H3
mode strings (`文生视频(T2VA)` …) sent to the gateway, and the language glyph on the switcher.
Those must not be translated.

---

## 7. Three packages have no tests

`internal/setup`, `internal/sysinfo` and `internal/logx` have no `_test.go`. They are covered
indirectly (the server end-to-end test drives setup, and the resource tests now build a real
setup manager), but the parsing code has no direct tests:

- `setup/paths.go` — `extra_model_paths.yaml` parsing, path validation
- `sysinfo` — `/proc/meminfo` parsing, `nvidia-smi` CSV parsing
- `logx` — formatting

---

## 8. `ComfyUI-MiniMaxH3-TimelineDirector` is unaccounted for

The directory exists in ComfyUI's `custom_nodes`, but no resource in
`resources/index.json` references it (0 matches), so the panel neither installs nor removes it
and its origin is unverified. Either add it to the index or leave a note about where it came
from.

---

## 9. Repository size is dominated by history (operational)

`.git` is ~78 MB, but the tracked tree is small. The bulk lives in four large files plus
`infinite-canvas/web/dist` blobs that were committed before it was ignored:

```
16.1 MB  comfyui-gateway/resources/nodes/unpacked/TE_MAN/.../T5_tokenizer/tokenizer.json
 9.3 MB  resources/vae_approx/taeh3.safetensors
 4.4 MB  comfyui-gateway/resources/nodes/ComfyUI-qwenmultiangle.zip
 4.3 MB  .../TE_MAN/.../T5_tokenizer/spiece.model
 4.1 MB × N  infinite-canvas/web/dist/assets/index-*.js
```

**Fix** Keep the history and strip those paths:

```bash
pip install --user git-filter-repo
git filter-repo --invert-paths \
  --path infinite-canvas/web/dist \
  --path resources/vae_approx/taeh3.safetensors \
  --path comfyui-gateway/resources/nodes/ComfyUI-qwenmultiangle.zip \
  --path-glob '*/T5_tokenizer/*'
```

Deleting `.git` and re-initialising also works, but loses 292 commits of history.

---

## 10. `legacy-peer-deps` is pinned until pro-components supports Ant Design 6

**Symptom** `npm install` / `npm ci` in `infinite-canvas/web` aborts:

```
npm error ERESOLVE could not resolve
npm error While resolving: @ant-design/pro-components@3.0.0-beta.3
npm error Found: antd@6.4.2
npm error Could not resolve dependency:
npm error peer antd@^5.11.2 from @ant-design/pro-components@3.0.0-beta.3
```

**Cause** The app is on `antd@^6`, but every published `@ant-design/pro-components`
(`3.0.0-beta.3` and even `3.1.15-*` on the `latest` tag) still declares
`antd: ^4.24.15 || ^5.11.2` as a peer. Nothing in the tree uses a Pro component: the only
import is `ProConfigProvider` in `web/src/components/layout/app-providers.tsx`, a wrapper
that just re-provides the antd theme/locale context — it runs fine on antd 6, the peer range
is simply stale. Downgrading antd to 5 to satisfy the resolver is the wrong trade.

**Workaround in place** `infinite-canvas/web/.npmrc` sets `legacy-peer-deps=true`, so plain
`npm ci` (and therefore `make web`) works without flags. The trade-off is that npm no longer
fails on *any* peer conflict in that subtree, so a genuinely broken peer range would be
installed silently.

**TODO — pick one when touching the canvas deps**

1. Drop `@ant-design/pro-components` and replace `<ProConfigProvider dark={dark}>` with a
   plain passthrough (or fold `dark` into the existing `ConfigProvider`). Cheapest and
   removes ~1 MB of JS, but it changes antd's css-hash nesting — recheck dark/light tokens
   and the `ant-app` styles in `web/src/styles/`.
2. Or bump `@ant-design/pro-components` once a build declares `antd: ^6`, delete
   `.npmrc`, run `npm ci` and `make web` again.

Either way the install has to go back to strict peer resolution, otherwise the next antd
major upgrade is silent. Related: any antd upgrade should be validated with `make web` +
`make web-check` (the vendor chunk in `vite.config.ts` keys on `antd`/`@ant-design`/`rc-*`,
so a new scope would land in the generic `vendor` chunk instead).

