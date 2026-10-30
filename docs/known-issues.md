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

## 12. Corrupt persisted state destroyed the data it could not read *(fixed — decisions #49)*

**Was** Neither `use-canvas-store` nor `use-asset-store` guarded its `JSON.parse`. An unreadable
blob threw inside `getItem`, zustand caught it into `onRehydrateStorage`, that listener ignored the
error and set `hydrated: true`, and the app booted on empty defaults. The write-refusal check in the
canvas store (`if (queuedPersistState && ...)`) was bypassed because `queuedPersistState` is only
assigned after a successful parse, so the empty state was persisted over the unreadable one within
400 ms. Reproduced on the test origin: a 2314-byte store holding one canvas became a 378-byte store
holding an empty one, the original canvas vanished from the list, and the console stayed empty.

**Now** Both adapters catch the parse, copy the raw bytes to
`<key>.corrupt-<timestamp>`, record the failure, return `null` so hydration proceeds deliberately,
and refuse to write while the failure stands. A banner over the canvas says what happened, offers
the raw bytes as a download, offers a retry, and offers a "clear and start over" that re-enables
writing and rewrites the main key.

**The other half, now also fixed** Neither `persist` configuration had a `version` or a `migrate`.
zustand's `merge` is `{...currentState, ...persistedState}` — shallow — so `projects[]` was taken
from disk verbatim and any field added inside a project came back `undefined`. A viewport without
`k` is `NaN` in every coordinate calculation on the canvas.

Three things were added: `version: 1` on both stores so what we write is labelled; a `migrate` pass
that backfills per-project fields (this is the part zustand's shallow merge could not do); and a
shape assertion after migration, so a blob that cannot be brought to the current shape throws and
takes the same recovery path as invalid JSON — copied aside, write refused, banner shown.

The check lives in `getItem` rather than in zustand's own `migrate` hook on purpose. That hook runs
*after* `getItem`, so a failure there cannot reuse this app's recovery; zustand logs and discards,
and the empty state is persisted over the blob — the same data-loss path as the first half, with one
exception: a *version mismatch* with no `migrate` would have gone straight through even with the
write refusal in place, because `getItem` succeeded.

Covered by `tests/persist-migration.test.ts` (14 cases): versionless blobs, missing `lastProjectId`,
missing per-project fields, a viewport with no `k`, a zero zoom, a blob from a newer build,
malformed JSON, and assets with no `data`.


---

## 13. The repo is not formatted with its own Prettier config

**Symptom** `npx prettier --check src` reports `Code style issues found in 78 files`.

**Cause** `.prettierrc.json` sets `printWidth: 255`, which is what the very long single-line import
statements in this repo match. But 78 files contain lines prettier would still reflow — import lists
over 255 characters, and callsites it would collapse or split differently. Whatever produced that
style, it was not `npm run format`.

**Why it matters** `npm run format` exists and `npm run format:check` exists, but neither is part of
`npm run check`, so the drift is invisible until somebody runs it — and then `--write` produces a
2400-line diff across 45 files, which buries whatever change they actually wanted to make. That
happened during decisions #50 and had to be reverted.

**Fix** Run `npm run format` once, as its own commit with nothing else in it, and add
`format:check` to `npm run check` so it cannot drift again. Not done here because a formatting-only
commit should not share a commit with a behaviour change.
