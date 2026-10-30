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

## 4. "Restart needed" is shown for a missing custom node *(fixed — decisions #48)*

**Where** `infinite-canvas/web/src/components/settings/resource-center-panel.tsx`, the workflow
status tag:

```tsx
workflow.missing ? <Tag>{copy.missing} {workflow.missing}</Tag>
  : workflow.available ? <Tag>{copy.workflowReady}</Tag>
  : <Tag>{copy.workflowRestart}</Tag>          // ← every other "not available" case
```

**Problem** The last branch is the fallback for *any* unavailable workflow, so a workflow
blocked by an uninstalled custom node reads "Restart needed". The label promises a restart,
which will not help.

**Fix** *(applied)* A workflow carrying `problems` now reports `workflowBlocked` ("Load failed")
and only a genuinely pending restart reads "Restart needed".

---

## 5. "Canvas entry" tag is shown inverted *(fixed — decisions #48)*

**Where** Same file. The `canvasEntry` tag renders when `workflow.modelVisible === false`,
i.e. exactly when the workflow is *hidden* from the model picker.

**Fix** *(applied)* The label now says what the condition is — "Hidden from picker" — rather than
inverting the condition, which would have changed behaviour on a guess about intent.

---

## 6. Thirteen lines of Chinese stay hardcoded, on purpose

**Was** 55 strings of UI copy written inline, showing Chinese in the English UI, plus
`resource-center-panel.tsx` keeping its own `zh`/`en` copy object.

**Now** Every one of those strings goes through `t()`, and the panel's 102 strings moved to
`resources.panel.*` (decisions #39). What is left is 13 lines, and each is data rather than copy:

```
 6  components/canvas/canvas-node-prompt-panel.tsx   protocol: PromptOptimizeMode + referenceNote
 2  components/canvas/canvas-node.tsx                art: mascot slogans, paired with their English line
 2  components/canvas/canvas-node-moss-layout-dialog.tsx  data: caption regex + prefix the gateway parses
 1  services/api/image.ts                           protocol: PromptOptimizeMode union
 1  components/layout/user-status-actions.tsx       endonym: 「中」 on the language switcher
 1  components/layout/app-config-modal.tsx          endonym: 中文 in the language picker
```

**Why these are not a violation** #17 asks for user-visible copy to live in the dictionaries. The
protocol values are form fields the gateway matches or feeds to a workflow (#9); the MOSS regex and
prefix are an agreement about the shape of the gateway's output; the mascot slogans are set in a CJK
font stack and printed next to their own English line, so translating them would show the sentence
twice; and a language name is written in the language it names.

**Not verified** The remaining risk is a raw key or a `zh` fallback appearing in a surface nobody
clicked through. The DOM scan covers every route in both locales, but dialogs, drawers and
context menus are opened by hand and are not exhaustive.

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

## 10. Chinese prose remains in the vendored canvas and plugin subtree

**Where** `infinite-canvas/plugins/` — 36 files, 454 Chinese lines:

| Lines | Comments | File kind |
| --- | --- | --- |
| 161 | 3 | plugin `README.md` files |
| 132 | 71 | plugin components (`src/*.tsx`) |
| 92 | 73 | plugin SDK (`sdk/src/*.ts`) |
| 26 | 19 | build scripts (`build.mjs`) |
| 35 | 0 | `SKILL.md` agent instructions |
| 8 | 0 | `packages.json` metadata |

Plus `infinite-canvas/CHANGELOG.md` (265 Chinese lines of release history).

**Why it is listed** [decisions.md #17](decisions.md) requires English for documentation and
comments. These files came in with the upstream canvas subtree and predate the rule. Note that
the 163 comment/JSDoc lines and the READMEs describe the plugin API, i.e. the part a third-party
plugin author reads, and that the plugins' own UI strings are compiled into
`web/public/plugins/*.js` — the canvas dictionaries cannot translate them because each plugin
ships as a standalone bundle.

**Fix** Translate the comments and READMEs first (mechanical). The plugin UI copy needs a
decision before code moves: either the plugins grow their own copy table (one per bundle, chosen
from the canvas locale) or the strings become English only. `CHANGELOG.md` is release history —
leave the existing entries as written, write new ones in English.

**Not affected** The `SKILL.md` files (agent input, same reasoning as decisions #9), the LoRA
name filter in `workflows/registry.json` (`mj风格` and friends are patterns matched against real
file names), `web/src/i18n/locales/zh-CN.ts` and everything under `internal/prompt`.

## 11. Radix hides the page from assistive tech while a focused button sits in it

**Symptom** Opening a confirmation (`kit/confirm`, which is radix's `AlertDialog`) over a button
that currently has focus logs a browser warning:

```
Blocked aria-hidden on an element because its descendant retained focus.
Element with focus: <button ... border-red-300 ...>        (the danger button that was clicked)
Ancestor with aria-hidden: <div class="flex h-dvh ...">
```

**Cause** Radix sets `aria-hidden` on the rest of the document when a modal layer opens, and it
does so before focus has moved into the layer. Chrome therefore blocks the attribute (which is the
outcome we want — the page stays readable) and warns. Reproduced from the config page's delete
buttons; it is not specific to any one call site.

**Effect right now** None that a user can see: the dialog is operable and focus lands in it. The
warning is once per open and only in the console.

**Fix** Either upgrade radix once it uses `inert` instead of `aria-hidden` for `hideOthers`, or move
focus to the dialog's cancel button in `onPointerDown` of the trigger. Not done, because both are
larger than the symptom.

---

## 12. Corrupt persisted state is silently indistinguishable from "you have nothing"

**Where** `stores/canvas/use-canvas-store.ts:57` and `stores/use-asset-store.ts:52`, both custom
`persist` storages:

```ts
const parsed = JSON.parse(value) as StorageValue<CanvasStore>;
```

**What happens** If the stored blob is not valid JSON — a truncated write, a schema change that
makes `parsed.state.assets.map` throw, a manual edit — the parse throws inside `getItem`. Zustand
catches it, calls `onRehydrateStorage` with the error, and the listener ignores the error and sets
`hydrated: true`. The app then comes up with **empty defaults**.

**Reproduced** On the `127.0.0.1:3000` origin, with a live canvas holding two nodes:

1. back up `infinite-canvas:canvas_store` (2314 bytes, one project), then overwrite it with the
   invalid string `{oops truncated`;
2. hard reload.

Result: the app came up on `/canvas/QA-dFYz82fJmscKgcA7_2` — a **new project id** — titled
"Infinite Canvas 1", "No nodes on this canvas". The original canvas was gone from the list. The
console was **empty**: not the persist middleware's error, not a warning, nothing. Within seconds
the stored blob was 378 bytes containing a valid blob for the new empty project, so the 2314-byte
original had already been overwritten.

**Why that is worse than it sounds** The user sees an empty canvas list over data that is still in
IndexedDB, with no message anywhere. There is no "your saved state could not be read" in the UI and
none in the console, so the only available conclusion is that the canvases were deleted — and they
very nearly are, because the empty state is persisted over the unreadable one as soon as anything
sets store state.

**There is no `version` and no `migrate`** in either `persist` configuration, so this is not only a
corruption path. Any change to the persisted shape — `lastProjectId` was added earlier in this work
— hydrates a partially-defined state instead of being migrated or rejected.

**Fix** Guard both parses, and make the storage adapter refuse to write over a blob it failed to
read, so the unreadable state survives until someone can look at it. Then say so once, in the UI.
Not done yet: the "refuse to write" half means the session stops persisting, which is only coherent
together with the message, and the message is a product decision.

**Restored** The origin's data was put back from the backup afterwards and verified: 2314 bytes, one
project named 无限画布 1, two nodes.
