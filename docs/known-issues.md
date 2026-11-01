# Known issues

Open problems, verified against the current tree. Each entry says what breaks, why, and what the fix
is, so nobody has to rediscover it. Fixes that only need a command are written out as commands.

Fixed entries are not kept here: they are in [`decisions.md`](decisions.md), which is where the reason
for the fix lives. The last section lists the ones this file used to carry.

---

## 1. H3 video prompts ignore the requested output language

**Symptom** The prompt asks for Chinese (`输出语言：字段正文全部使用中文`) and the generated prompt still
comes back in English.

**State** Reproduced on a local `qwen3.8-27b` endpoint with thinking disabled in the model's `model.conf`:
the instruction is in the request, and it is not followed. Not diagnosed beyond that — either the model
ignores it or the H3 skill does not carry enough weight.

**Next step** Ask through the agent socket (`GET /api/agent/socket/{session}`, `?history=1` for a panel's
conversation) and read the frame log (`agent << SM`, `agent >> UX`): the instruction is in the prompt, so
the difference between the two outcomes is the skill's wording. The fix belongs in
`prompts/skills/h3-prompt-writing/`, which is the agent's to load.

---

## 2. Interpolating a silent clip fails inside VHS

**Symptom** `video-rife` on a clip with no audio track fails with `VHS failed to extract audio`. Clips the
canvas produces always carry audio; an imported or trimmed silent clip does not.

**Fix to decide** Detach the audio input when the source has no audio stream, at the cost of never carrying
audio through the interpolation. `sourceVideoFrames` and the rest of the media plan are declared in
`registry.json` (`video-rife`), so this is a `accepts`/binding question, not a code branch.

---

## 3. Radix hides the page from assistive tech while a focused button sits in it

**Symptom** Opening a confirmation (`kit/confirm`, radix's `AlertDialog`) over a button that currently has
focus logs a browser warning:

```
Blocked aria-hidden on an element because its descendant retained focus.
Element with focus: <button ... border-red-300 ...>        (the danger button that was clicked)
Ancestor with aria-hidden: <div class="flex h-dvh ...">
```

**Cause** Radix sets `aria-hidden` on the rest of the document when a modal layer opens, and does it before
focus has moved into the layer. Chrome therefore blocks the attribute (which is the outcome we want — the
page stays readable) and warns. Reproduced from the config page's delete buttons; not specific to one call
site.

**Effect right now** None that a user can see: the dialog is operable and focus lands in it. The warning is
once per open and only in the console.

**Fix** Either upgrade radix once it uses `inert` instead of `aria-hidden` for `hideOthers`, or move focus
to the dialog's cancel button in `onPointerDown` of the trigger. Not done: both are larger than the symptom.

---

## 4. `ComfyUI-MiniMaxH3-TimelineDirector` is unaccounted for

The directory exists in ComfyUI's `custom_nodes`, but no resource in `resources/index.json` references it
(0 matches), so the panel neither installs nor removes it and its origin is unverified. Either add it to the
index or leave a note about where it came from.

---

## 5. Narrower test coverage than it looks: `internal/logx` has no test

`internal/logx` has no `_test.go`: it formats a timestamp, a level and a message, and nothing else in the
package is worth a test. It is covered indirectly (every other test's output goes through it), which is
the reason it has stayed this way.

`internal/setup` and `internal/sysinfo` were in this entry with it and no longer belong here — both have
direct tests now (`paths_test.go`, `scan_test.go`, `sysinfo_test.go`).

---

## 6. A few strings stay Chinese in the source, on purpose — and they are data, not copy

Every string of *UI copy* goes through `t()`. What is still written in Chinese in the source is a short
list, and each line is data the gateway or the agent parses rather than text a user reads:

| Lines | File | What it is |
| --- | --- | --- |
| 10 | `components/canvas/canvas-node-prompt-panel.tsx` | the `PromptOptimizeMode` values sent to the agent |
| 3 | `lib/canvas/agent-references.ts` | the `【画布引用】` marker the citation block is built from |
| 2 | `components/canvas/canvas-node.tsx` | mascot slogans, printed beside their own English line |
| 2 | `components/canvas/canvas-node-moss-layout-dialog.tsx` | the caption prefix/regex the MOSS workflow parses |
| 2 | `lib/canvas/node-title.ts` | old default titles the migration recognises (`图片`, `音频`, …) |
| 1 | `services/api/prompts.ts` | the same `PromptOptimizeMode` union, as a type |
| 1 | `components/layout/user-status-actions.tsx` | the endonym on the language switcher |
| 56 | `lib/resource-copy-zh.ts` | the resource panel's **Chinese copy**, kept beside the panel by #67 |

**Why these are not a violation** #17 asks for user-visible copy to live in the dictionaries. The protocol
values are fields the gateway matches or feeds to a workflow (#9); the MOSS regex and prefix are an
agreement about the shape of the gateway's output; the mascot slogans are set in a CJK font stack and
printed next to their own English line, so translating them would show the sentence twice; a language name
is written in the language it names; and the migration titles are strings *stored in old blobs*, which is
the whole reason they are recognised. `resource-copy-zh.ts` is not copy that escaped: #67 decided the
resource panel's Chinese lives beside it as data, and `tests/resource-copy-zh.test.ts` keeps the two
languages in step.

**Not verified** The residual risk is a raw key or a `zh` fallback in a surface nobody clicked through. The
DOM scan covers every route in both locales; dialogs, drawers and context menus are opened by hand and are
not exhaustive.

---

## 7. Chinese prose remains in the vendored canvas and plugin subtree

**Where** `infinite-canvas/plugins/` — 63 tracked files, 454 Chinese lines: plugin `README.md`s, plugin
components, the SDK, the build scripts, the `SKILL.md` agent instructions and `packages.json` metadata.
Plus `infinite-canvas/CHANGELOG.md` (265 Chinese lines of release history).

**Why it is listed** [`decisions.md`](decisions.md) #17 requires English for documentation and comments.
These files came in with the upstream canvas subtree and predate the rule. The READMEs and the ~163
comment/JSDoc lines describe the plugin API — the part a third-party plugin author reads — and the plugins'
own UI strings are compiled into `web/public/plugins/*.js`, which the canvas dictionaries cannot translate
because each plugin ships as a standalone bundle.

**Fix** Translate the comments and READMEs first (mechanical). The plugin UI copy needs a decision before
code moves: either the plugins grow their own copy table (one per bundle, chosen from the canvas locale) or
the strings become English only. `CHANGELOG.md` is release history — leave the existing entries as written,
write new ones in English.

**Not affected** The `SKILL.md` files (agent input, same reasoning as #9), the LoRA name filter in
`workflows/registry.json` (`mj风格` and friends are patterns matched against real file names),
`web/src/i18n/locales/zh-CN.ts` and everything under `internal/prompt`.

---

## 8. Repository history is larger than the tree (operational)

The tracked tree is small, but the pack file is not: `git count-objects -vH` reports `size-pack` ≈ 15 MiB
over ~3,200 objects, and most of it is history of files that are no longer here (the upstream vendored
`web/dist` bundles, a ModelScope zip, a tokenizer directory). Cloning costs that once; nothing else does.

**Fix, if it ever matters** Keep the history and strip those paths:

```bash
pip install --user git-filter-repo
git filter-repo --invert-paths \
  --path infinite-canvas/web/dist \
  --path-glob '*/T5_tokenizer/*'
```

Deleting `.git` and re-initialising also works, but loses the history that documents why the code looks
the way it does.

---

## Fixed, and where the reason is

- **SeedVR2 pack did not import** — the vendored payload was missing `src/data/`; restored, and
  `internal/resource/payload_imports_test.go` now fails on any pack that imports what it does not ship,
  plus `TestVendoredPacksContainNoBytecode` for the `.pyc` files that same round left behind. #113.
  **Not the same as "the workflow runs."** ComfyUI on the reference machine (2026-11-02) reports five
  `SeedVR2*` classes — `Conditioning`, `Preprocess`, `PostProcessing`, `TemporalChunk`, `TemporalMerge` —
  and none of the three `video-seedvr2-upscale` declares (`SeedVR2LoadDiTModel`, `SeedVR2LoadVAEModel`,
  `SeedVR2VideoUpscaler`), so what is installed there is another SeedVR2 pack. The workflow stays
  unavailable until this payload is installed from the panel and ComfyUI restarted; the dependencies in
  [`runtime-deps.md`](runtime-deps.md) (`huggingface-hub<2` included) still apply.
- **Old 10-bit HEVC clips played audio only** — the two files this entry named are no longer in
  `data/outputs`, and the three H3 workflows have written `h264` / `yuv420p` since then; the guard is
  `internal/workflow/browser_playback_test.go`.
- **"Restart needed" for a missing custom node**, and **the inverted "canvas entry" tag** — labels fixed in
  #48.
- **Corrupt persisted state destroyed what it could not read** — `preserveUnreadable`, the recovery banner
  and the per-project migration; #49 and `tests/persist-migration.test.ts`.
- **The repo was not formatted with its own Prettier config** — `npm run format` once, and
  `format:check` is a gate; #50.
- **Thirteen routes with no caller, the one-shot agent path, the heartbeat, the unused locals and
  translation keys** — #107–#109, #114. The gates that let them live (#115) now fail on the next one.
