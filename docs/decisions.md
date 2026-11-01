# Design decisions

Why the code looks the way it does. Each entry states the decision, the reason, and what it
costs — so a future change can be measured against the original intent instead of guessed.

Format is deliberately short: decision, why, consequences, where it lives.

---

## 1. The gateway is Go, built as a static binary with CGO disabled

**Decision** `gateway` is a single Go binary, `CGO_ENABLED=0`, `-trimpath -ldflags '-s -w'`.

**Why** The gateway is the one component the canvas cannot work without. A static binary
starts in milliseconds, cannot break because a transitive npm package changed, and needs no
runtime installed on the target machine. `make verify-static` fails the build if `ldd` ever
reports a dependency.

**Cost** No cgo libraries. The race detector needs cgo, so `make test-race` re-enables it for
the test binary only; the shipped binary stays static.

**Where** `Makefile`, `docs/gateway.md` (Why Go).

---

## 2. Zero third-party dependencies

**Decision** `go.mod` has no `require` block. The WebSocket client is hand-written
(`internal/comfy/ws.go`), archive extraction uses `archive/zip` and `archive/tar` +
`compress/gzip`.

**Why** Every dependency is a supply chain and a version constraint. The protocol surface this
gateway needs is small: RFC 6455 frames without extensions, and plain zip/tar.

**Cost** The WebSocket client implements only what ComfyUI sends (text frames, ping/pong,
close). Reconnect logic lives in the caller.

**Where** `go.mod`, `internal/comfy/ws.go`, `internal/resource/jobs.go` (`extractArchive`).

---

## 3. The Go module is the repository root

**Decision** `main.go`, `internal/`, `go.mod`, `Makefile` and the runtime assets (`workflows/`,
`resources/`, `prompts/`) sit at the top level; the canvas frontend is a sibling subtree in
`infinite-canvas/`.

**Why** The repository _is_ a Go service with one vendored frontend. An extra `gateway/`
directory only made sense while a second, Node implementation lived next to it.

**Cost** `go build ./...` would walk `infinite-canvas/web/node_modules`, so the Makefile uses an
explicit package list (`. ./internal/...`). Measured difference: `go vet` 0.64 s vs 0.89 s, plus
protection from a stray `.go` file inside an npm package.

**Where** `Makefile` (`PKGS`), `README.md` (Layout).

---

## 4. Workflow graphs stay generic JSON

**Decision** Graph files are `map[string]any` end to end. Typed accessors (`Node.ClassType()`,
`Node.Inputs()`) sit on top; nothing models individual node classes.

**Why** There are nineteen ComfyUI graphs carrying node classes this codebase will never know
about, and users add more. A typed model would have to be extended for every new node.

**Cost** Field access is by string, so typos are runtime errors. Mitigated by confining raw map
access to `internal/workflow`.

**Where** `internal/workflow/graph.go`.

---

## 5. Bindings keep their raw JSON

**Decision** A binding is a thin typed view (`Binding.raw`) over the object in `registry.json`.

**Why** `registry.json` is user-editable. A closed struct would silently drop unknown fields
the next time a workflow uses a newer binding option — a bug that only shows up as a missing
parameter in a generated request.

**Cost** Reading a field goes through accessors, and the type system cannot prove completeness.

**Where** `internal/workflow/bindings.go`.

---

## 6. Request-scoped copies only

**Decision** `ApplyBindings` and `ApplyLoraChain` clone the graph before mutating it; graphs
loaded from disk are treated as immutable.

**Why** The registry is shared across requests. Mutating a loaded graph means two concurrent
generations can see each other's parameters — an intermittent, hard-to-reproduce wrong-image
bug.

**Cost** One graph copy per request (a few hundred KB of JSON), which is irrelevant next to
inference time.

**Where** `internal/workflow/bindings.go`, `internal/generate/lora.go`.

---

## 7. Order-preserving maps for anything the UI renders in sequence

**Decision** `variants`, `variantLabels` and `bindings` use `workflow.OrderedMap`, not Go maps.

**Why** Go randomises map iteration. Those keys drive dropdown order, so a plain map would
reshuffle the canvas image-mode selector on every refresh.

**Cost** A small custom type instead of the builtin.

**Where** `internal/workflow/ordered.go`.

---

## 8. Backend messages are English; localization lives only in the frontend

**Decision** Log lines, error messages and warnings are English. Translation happens in
`infinite-canvas/web/src/i18n`. Error `code` values are stable identifiers
(`model_not_found`, `missing_reference_image`, …).

**Why** One backend, two UI languages. Translating backend text would mean shipping a locale to
a process whose only consumer is a dashboard, and would make logs unreadable in bug reports.

**Cost** The frontend has to map codes to copy; unmapped errors surface in English.

**Where** `internal/httpx`, `docs/gateway.md` (Language policy).

---

## 9. Model-facing Chinese stays Chinese

**Decision** The H3 writing instructions, image/caption system prompts, Chinese-matching
regexes (`图片N`, `正向|负向`) and ComfyUI protocol values (`文生视频(T2VA)` …) are not
translated.

**Why** They are input to a model or to ComfyUI, not text for a human. Translating the prompts
would change generation behaviour; translating the regexes would break parsing; the mode
strings are values the frontend sends and the backend matches.

**Cost** The codebase contains Chinese even though it is English-only by policy. Each such site
carries a comment saying so.

**Where** `internal/prompt/*`, `internal/workflow/infer.go`.

---

## 10. Job statuses and progress are a wire contract with the panel

**Decision** Install jobs speak exactly what the canvas panel expects:

```
job.status  : running | done | error | canceled
item.status : queued | downloading | verifying | copying | extracting | baking | done | error | skipped
job.progress: 0..100, weighted by bytes
```

**Why** The panel was written against these values: it renders byte counters and speed only for
`downloading`, stops polling only on `done`/`error`/`canceled`, and passes `progress` straight
to antd's `percent` prop. Sending `completed` or a 0..1 fraction leaves the bar empty forever —
which is exactly what happened once.

**Cost** The vocabulary is frozen by the frontend, and a single large file must move the bar
(hence byte weighting rather than finished-item counting).

**Where** `internal/resource/jobs.go` (`jobProgress`, item status transitions), `internal/resource/bake.go`.

---

## 11. The gateway installs nothing with pip

**Decision** Runtime Python packages are reported (`runtimeDeps` in `/api/resources/status`) and
the UI shows the exact command; the gateway never runs `pip install`.

**Why** The old implementation wrote into the ComfyUI venv and regularly broke it: wrong wheel
for the CUDA/Python/torch combination, installs that reported success but failed on import, and
torch/numpy overwritten underneath ComfyUI.

**Cost** One manual step for the user, and the panel must tell them precisely what to run.

**Where** `internal/resource/resource.go` (`RuntimeDep`), `docs/runtime-deps.md`.

---

## 12. Only three external binaries, everything else in-process

**Decision** `nvidia-smi` (GPU), `zenity` (file picker), `ffprobe` (video duration). Downloads,
verification, archive extraction and file copies all happen in Go.

**Why** A missing external tool used to be able to fail an install. Anything that can be done
in-process is done in-process; the three that remain wrap information only the OS/driver has.

**Cost** Each of the three must degrade with an actionable message when absent — which they do
(`zenity was not found; type the model's relative path instead …`).

**Where** `internal/sysinfo/sysinfo.go`, `internal/setup/setup.go`, `internal/server/server.go`.

---

## 13. `type: "bake"` resources are never installed

**Decision** A bake resource (a checkpoint merged from two others) is excluded from install
jobs. `/api/resources/status` exposes the command that produces it as `manualCommand`, and the
panel shows that command with a copy button.

**Why** Merging needs torch and produces ~10 GB; neither belongs inside an HTTP install
request. Including it in a job meant every MOSS-pack install finished with a guaranteed failure
and a misleading "run the merge inside ComfyUI" message.

**Cost** One documented manual step, and the command must stay correct as the script's flags
change — pinned by `internal/resource/bake_test.go`.

**Where** `internal/resource/bake.go`, `tools/bake-moss-preset.py`.

---

## 14. The asset root is discovered, not hardcoded

**Decision** `GATEWAY_ROOT` → working directory → executable directory, accepting the first that
contains `workflows/registry.json` or `resources/index.json`.

**Why** The binary lives in `bin/`, which must not be mistaken for the asset root, and the
gateway is also started from the repository root by `start.sh`. A bare `workflows/` directory is
not enough of a marker — it produced a self-reinforcing wrong guess.

**Cost** Starting from an unrelated directory silently falls back to the working directory; if
that happens, state lands in `./data`.

**Where** `internal/config/config.go` (`resolveRootDir`).

---

## 15. Gates: `make check` must not need a C toolchain

**Decision** `make check` = gofmt check + `go vet` + tests (CGO off). `make check-full` adds
`-race` and therefore needs gcc. `make verify-static` asserts the binary is static.

**Why** The shipped binary must stay cgo-free, but the race detector is the only thing that
catches the concurrency bugs this code has actually had (shared task and job state read while a
worker mutates it).

**Cost** Two gates; contributors without gcc can still run the fast one.

**Where** `Makefile`.

---

## 16. The gateway never starts or stops ComfyUI

**Decision** ComfyUI is external: the gateway connects to `COMFY_URL` and reports when it is
unreachable. `start.sh` does not launch it either: ComfyUI may be on another machine, in a
container, or under a supervisor the user chose, so a start script that assumes it is local
would be wrong more often than right.

**Why** ComfyUI owns the GPU and is usually already running with a user's own configuration.
A gateway that restarts it would race with whatever the user is doing.

**Cost** "ComfyUI is not reachable" is a normal state the UI has to handle, not an error.

**Where** `internal/comfy/client.go`, `start.sh`, `README.md` (process table).

---

## 17. Everything written for a human is English

**Decision** Documentation, code comments, log lines, error messages and commit messages are
English. User-visible copy is not written in the source at all: it lives in the i18n
dictionaries (`infinite-canvas/web/src/i18n`) and is translated there. The two exceptions are
the ones already argued for: model-facing Chinese and protocol values (#9), and UI copy as
data (#8).

**Why** One prose language keeps `grep`, diffs, bug reports and commit history readable, and
the frontend is bilingual anyway — a Chinese string in a component is not "a Chinese UI", it
is a string that only ever renders in Chinese and cannot be reviewed by the half of the
audience that reads English. Writing the rule down is the only way to stop the next
contribution from adding one more.

**Cost** Chinese prose that predates the rule has to be translated (tracked in
`docs/known-issues.md` #10), and a reviewer has to reject inline copy even when it works in
the current UI language.

**Where** `README.md` (Language), `docs/gateway.md` (Language policy),
`docs/known-issues.md` #6 and #10, decisions #8 and #9.

---

## 18. `@ant-design/pro-components` is dropped, not worked around

**Decision** The canvas does not depend on `@ant-design/pro-components`. `AppProviders`
wraps the tree in antd's `ConfigProvider` + `App` and nothing else.

**Why** Every published build of that package declares `antd: ^4.24.15 || ^5.11.2` as a peer
while this app runs antd 6, so `npm ci` aborted with ERESOLVE and the only way to install was
`legacy-peer-deps=true` for the whole subtree — a flag that also silences the _next_ real peer
conflict. The single import was `ProConfigProvider`, a wrapper that only re-provides antd's
theme and locale context (plus an SWR provider nothing used); the app has no Pro component, so
there was nothing to keep. Removing it lets installs go back to strict peer resolution, drops
130 packages and ~29 kB of gzipped JS.

**Cost** Anyone who later wants ProTable/ProForm has to add the package back, and antd 6
support becomes a prerequisite for that work.

**Where** `infinite-canvas/web/package.json`,
`infinite-canvas/web/src/components/layout/app-providers.tsx`. The rendered output is
unchanged — the built page still emits `class="css-1tzwyuk ant-app"` and
`--ant-color-bg-base:#000`, i.e. the same antd hashed theme as before the removal.

---

## 19. `GATEWAY_HOST` is bound as given; there is no `ALLOW_REMOTE`

**Decision** The gateway listens on whatever `GATEWAY_HOST` says, loopback or LAN address, with no
second switch. `ALLOW_REMOTE` is gone, and so is the startup refusal it gated. What remains is
a warning when the address is reachable from the network and no `GATEWAY_TOKEN` is set.

**Why** A workbench whose gateway is on `192.168.3.232:8190` so a phone can use it _is_ the
local setup — calling that "remote" and making the user flip a flag to reach their own machine
was a gate with no bug behind it. It also failed quietly in the worst way: the canvas started,
the gateway exited, and the UI showed "gateway disconnected". The real risk is not the bind
address but an unauthenticated `/api/*`, and that is what the warning names.

**Cost** One keystroke less between a machine and the network. Anyone who wants the old
behaviour back has `GATEWAY_HOST=127.0.0.1`, which is still the default. `CORS_ORIGINS` no longer
accepts `*` under any condition — the canvas origin is matched by host, so the wildcard was
only ever an accident waiting to happen.

**Where** `internal/config/config.go`, `internal/httpx/middleware.go` (`sameHostOrigin`),
`main.go`, `.env.example`, `docs/gateway.md`.

---

## 20. Workflow metadata is English in the registry and translated by id in the canvas

**Decision** `workflows/registry.json` carries English `label`, `description`, `variantLabels`
and `parameters[].label/description` — it is data the gateway serves, so it follows #8. The
canvas translates the strings it displays, in `web/src/lib/gateway-labels.ts`, keyed by the
stable identifier the gateway already sends:

```ts
gatewayModelLabel(modelId, gatewayLabel); // gateway.models.<id>.label
gatewayVariantLabel(mode, gatewayLabel); // gateway.variants.<mode>
gatewayModelDescription(modelId, gatewayLabel);
```

An id the dictionary does not know falls back to the English text, so a hand-written or
newly added workflow still renders instead of showing a raw key.

**Why not bilingual registry strings** A `{ "zh": …, "en": … }` shape would have changed the
`/v1/models` contract, every consumer of `metadata.label`, and the registry schema, for
metadata that only the canvas displays. Ids (`image-moss-sft-8step`, `inpaint`) are already in
the response and cannot change without breaking stored channel configs, which makes them the
natural dictionary key.

**Why the lookup names the language explicitly** `src/i18n/index.ts` sets
`fallbackLng: "zh-CN"`, so a plain `t()` answers with Chinese in the English UI as well. The
helper asks `getFixedT("zh-CN")` and only when `resolvedLanguage` is `zh-CN`.

**Cost** Two places describe one workflow: the registry (English, API consumers) and the
dictionary (Chinese, this UI). A workflow added by hand shows English in both languages until
someone adds the zh entries — visible, not silent.

**Where** `workflows/registry.json`, `infinite-canvas/web/src/lib/gateway-labels.ts`,
`web/src/i18n/locales/zh-CN.ts` (`gateway.models`, `gateway.variants`), `use-config-store.ts`
(`modelOptionLabel`, `baseModelOptions`), `resource-center-panel.tsx` (variant list).

**Not translated on purpose** `parameters[].label/description` for the API and plugin consumers
stay English only (nothing in the canvas renders them yet), `bindings.prompt.default` is model
input (#9), and `name` / `source.name` / `lora.filter` are identifiers and patterns.

---

## 21. Model installs are a configured fact, and they are off until stated

**Decision** `INSTALL_INTO_COMFY=1` is required to install models. Without it
`POST /api/resources/install` answers `400 model_install_disabled`, the panel shows a banner and
disables the download buttons, and nothing is inspected to decide any of this. `.env.example` — the
single-machine template the README tells you to copy — ships the line set.

**Why not detection** The two ways to get it wrong are mirror images and both are cheap to hit. A
bind mount or NFS share at a path different from what ComfyUI reports looks exactly like a remote
machine, so a `stat` blocks a working install. Two hosts that both install to `/opt/ComfyUI` look
exactly like one, so the same `stat` allows one that cannot work. A gate that is wrong in either
direction is worse than no gate, and the operator settles it in one line.

**Why fail-closed, and why that was not true before release** The dangerous failure is not an
error: as root, writing into a path that happens to exist here succeeds, gigabytes land on the
wrong disk, and ComfyUI keeps reporting the model as missing with nothing in the log. A default of
"try anyway" would keep that hole open, and the only defence would be an operator who already knew
the answer. While the project had no installed base the trade was different — refusing by default
would have broken deployments that never set the variable — but before release there are none, so
the safe default costs one line in `.env` and nothing else.

**Cost** A first run that skips `.env` gets the panel with installs off; the banner names the
variable, so it is a one-line fix rather than a mystery. Anyone whose mounts are real but
unusual still has to say so, because the process cannot tell them apart from a remote machine —
that is the point, not a limitation to work around.

**Where** `internal/config/config.go` (`boolValue`), `internal/resource/install_policy.go`,
`internal/resource/jobs.go` (`Install`, `installHint`), `internal/resource/status.go`,
`resource-center-panel.tsx`, `web/src/services/api/resources.ts`, `docs/gateway.md`,
`.env.example`, `README.md`.

---

## 22. ComfyUI is the only backend, and workflow JSON is the product model

**Decision** The gateway speaks ComfyUI and nothing else. There is no backend abstraction, no
per-backend resource directory, no adapter contract and no install script. A ComfyUI API-format
graph _is_ how a model is defined here: `workflows/<type>-<slug>.json` plus an entry in
`workflows/registry.json` describing name, type, prompt binding and parameters.

**Why** The graph is already self-describing — `BuildSchema` renders the parameter card,
`InferBindings` finds the prompt and image slots, `InferType` decides image/video/audio,
`ImageInputCapacity` counts reference images. A hand-written workflow DSL would have to
reproduce all of that. Wrapping graphs as OpenAI models is the whole idea of the project, and
one backend keeps it small enough to hold in your head.

**Why not a backend abstraction, given a second backend is conceivable** The seam would have to
cut through 26 Go files (`/prompt`, `/history`, `/view`, `/object_info`,
`/internal/folder_paths`, `nvidia-smi`, `ModelLoaderSpecs`, the graph format itself), not just
the resource installer. Abstracting only `resources/` would leave a per-backend resource system
inside a ComfyUI-only gateway: two vocabularies, no payoff. The cheap path for a future backend
is to make _it_ speak ComfyUI's protocol — that costs a compatibility layer in the new thing
instead of a permanent indirection in this one.

**Cost** The graph format's sharp edges stay exposed: two encodings (API vs UI, the UI one is
rejected with an explanation), and the heuristics listed above can be wrong (no prompt binding
inferred, a node class renamed upstream). Those are handled where they occur — a registration
warning, a naming rule, a `ModelLoaderSpecs` edit — rather than behind an abstraction that would
only hide them.

**Where** `internal/workflow` (registry, inference), `internal/comfy`, `internal/resource`,
`workflows/*.json`.

---

## 23. antd stays; the two ways it leaks are quarantined

**Decision** The canvas keeps antd 6 as its component library. Two kinds of knowledge about
it are not allowed to spread past one file each:

- _antd's internal DOM._ Every `.ant-*` class name used by application logic now lives in
  `src/lib/canvas-overlay.ts`. The overlay guards match our own `data-canvas-overlay` marker
  and the existing `data-canvas-no-zoom` first; the antd class list is a documented fallback
  array beneath them. When antd goes, that array goes and the canvas is not touched.
- _The palette._ Colour values live in `src/lib/design-tokens.ts`; `src/lib/app-theme.ts`
  only maps them onto antd's `ThemeConfig`. `src/lib/canvas-theme.ts` keeps its own hex on
  purpose — it is feature data (six card skins handed to node renderers and the plugin
  context), not app chrome.

**Why** Measured before this decision: 60 of 167 frontend files import antd, ~530 JSX
instances and 121 `message.*` calls go through `App.useApp()`, and the antd chunk is 916 kB
(298 kB gzip), the single largest third-party block. The number that made this worth writing
down is not the byte count though, it is the fighting ratio: `globals.css` spends 323 of its
897 lines overriding `.ant-*`, with 155 `!` utility prefixes across 13 components and 23
`[&_…ant-…]` arbitrary variants in 3. A library that 36% of the file tree depends on and a
third of the stylesheet fights is one we can neither swap nor leave alone.

**Why not remove it now** Replacing it is roughly 14–22 engineer-days, and the expensive part
is exactly the part antd has earned: `Form`, `Modal` and `message`, i.e. focus trapping,
nested dialogs, validation ordering and toast key reuse. The seams above turn removal into a
later, cheaper decision rather than an immediate, riskier one. The real blocker was the
`Modal`–canvas coupling: while the guards read `.ant-*`, any migration would have produced
silent drag/zoom regressions instead of compile errors.

**Verified** `CANVAS_CLICK_GUARD` and `CANVAS_WHEEL_GUARD` reproduce the two previous
selectors byte for byte once the new marker is stripped, and `appPalette` matches the old
inline `neutral` object on all 20 colour slots — the quarantine is a refactor, not a
restyle. Note that `#fafafa` in the dark theme is a cold `neutral-50` while its elevated
surface is a warm `stone-900`; that mix predates this decision and is preserved.

**Cost** Whoever finishes the migration must delete the `ANTD_*` constants in
`canvas-overlay.ts`; leaving them behind means the canvas keeps a hidden dependency on a
library that is no longer installed. `design-tokens.ts` and the `:root` / `.dark` block in
`globals.css` cannot import each other, so changing a semantic colour is two edits — both
file headers say so.

**Where** `infinite-canvas/web/src/lib/canvas-overlay.ts`, `design-tokens.ts`, `app-theme.ts`,
`canvas-theme.ts`, `styles/globals.css`, `components/canvas/infinite-canvas.tsx`,
`canvas-context-menu.tsx`, `canvas-size-picker.tsx`, `canvas-resource-mention-textarea.tsx`.

---

## 24. Transient messages go through one facade, not antd

**Decision** Showing a transient message is `toast.success / error / warning / info / loading`
from `@/components/kit/toast`. That module owns the implementation — currently sonner, which
has no dependencies and only a React peer — and is mounted once, in `AppProviders`. Call sites
deliberately keep antd's shape: `toast.loading(text)` returns its own dismiss handler instead
of an id, so the three export/import flows that do `const hide = …; hide()` were not rewritten.
Do not "simplify" that away; it is the seam that keeps the library choice out of the callers.

**Why** antd's `message` was reached 121 times from 20 files, 19 of them through
`const { message } = App.useApp()`. That hook was the only reason those files imported `App`
at all. After the move, `hooks/use-copy-text.ts` and `components/layout/client-root-init.tsx`
import nothing from antd, the file count went 60 → 58, and `App.useApp()` now survives only in
the three files that still need `modal.confirm` (`pages/canvas/project.tsx`,
`layout/app-config-modal.tsx`, `layout/config-prompt-sources.tsx`) — which is the next slice.
It also fixed a real inconsistency: `canvas-node-prompt-panel.tsx` used antd's _static_
`message`, which does not read `ConfigProvider` and therefore rendered with antd's default
theme rather than ours.

**Cost** The toast looks slightly different — sonner's pill, not antd's, and it shows at most
three at a time where antd showed unbounded. The copy and the timing did not change:
`duration: 3000` matches antd's default, and `loading()` is still sticky. The bundle barely
moved (antd 916 kB → 912 kB) because `message` was a thin slice of a library that is still
installed — the byte win only arrives when antd goes entirely, so do not read this migration
as a size optimisation.

**Verified** `tsc --noEmit` and `vite build` clean; the 121 call sites are accounted for
one-for-one, no bare `message` identifier is left in application code, and the three files
that keep `modal` still destructure it correctly.

**Where** `infinite-canvas/web/src/components/kit/toast.ts`,
`components/layout/app-providers.tsx`, `package.json` (sonner), and the 20 migrated files.

---

## 25. Confirmations are a kit primitive, and antd's static helpers are gone

**Decision** Asking "really?" is `confirm({ title, content, okText, cancelText, danger, onOk })`
from `@/components/kit/confirm`, rendered by a single `<ConfirmHost />` in `AppProviders`. It is
imperative on purpose: all three call sites fire from an event handler deep inside a component,
and threading an `open` boolean through them would cost more than the question is worth. The
dialog is radix's `AlertDialog` — it supplies the focus trap, ESC handling and aria wiring that
made antd's version trustworthy — while every class name is ours. `AppProviders` no longer wraps
the tree in antd's `App`; nothing reaches `App.useApp()` any more (measured: 0 call sites, and
the only remaining `.ant-app` reference in the repo is gone too, so no styles depended on it).

**Why** `App.useApp()` was down to the three files that needed `modal.confirm`
(`pages/canvas/project.tsx`, `layout/app-config-modal.tsx`, `layout/config-prompt-sources.tsx`),
i.e. the last thing keeping those files tied to antd's context. With it gone, the antd chunk fell
from 912.9 kB to 883.5 kB purely by deleting the `App` wrapper, and a future migration no longer
has to reason about a context layer.

**Cost** The `onOk` contract is antd's, not radix's: returning a promise keeps the dialog open
with OK disabled until it settles, and a rejection leaves it open rather than closing silently.
That behaviour had to be reimplemented by hand, so if someone later "cleans up" `handleOk` the
async cases will start closing early. The styling is also ours now — it is meant to look like the
antd dialogs around it, and will need a touch-up if the surrounding design moves.

**Where** `infinite-canvas/web/src/components/kit/confirm.tsx`,
`components/layout/app-providers.tsx`, the three call sites above.

---

## 26. Unit tests run on vitest, because bun was never installed

**Decision** `npm test` is `vitest run`. The three suites under `tests/` import from `vitest`
instead of `bun:test`, and `vitest.config.ts` exists so the tests do not drag in the app's build
plugins. `bun.lock`, `pnpm-lock.yaml` and `pnpm-workspace.yaml` are deleted: `Makefile` sets
`NPM ?= npm`, so `package-lock.json` is the only lockfile that was ever authoritative — the
other two were stale duplicates that would have installed different trees.

**Why** `bun test` could not run here at all (`bun: command not found`), so the suite was
vestigial: it had never been executed, and its state was unknown. Running it once made that
concrete — **4 of 15 assertions were failing**, in `tests/model-selection.test.ts`. They assert
the _cloud-mode_ contract (an explicit but unavailable model selection is preserved so readiness
blocks the request), while `resolveModelForCapability` implemented the opposite: a model the
registry no longer knows falls back to its capability default. Since `LOCAL_ONLY` defaulted to
true, those four were wrong on every build this repository ever produced. They were first gated
behind `test.skipIf(LOCAL_ONLY)`; decision #27 then deleted cloud mode outright, so the four
assertions and the gate are gone too. The suite is 11 passing, no skips.

**Cost** The suite covers only pure functions — viewport focus, thumbnail choice, model
selection. There are no component or canvas interaction tests, so the overlay guards and the
kit primitives (toast, confirm) are still verified by hand.

**Where** `infinite-canvas/web/package.json`, `vitest.config.ts`, `tests/*.test.ts`.

---

## 27. Cloud mode is deleted, not hidden

**Decision** `LOCAL_ONLY` and everything behind it are gone. There is no `VITE_LOCAL_ONLY`, no
branch to restore the cloud interface, and no dead UI kept "just in case". Deleted: the
`/image`, `/video` and `/prompts` workbenches, the cloud model-channel editor paths, the
audio capability in the canvas, config import/export, the prompt library and prompt-source
scheduler, the model-script editor, and the cloud default config. Routes and the navigation
list now contain only what this build can actually serve.

**Why** The flag defaulted to on and nothing anywhere — not `.env`, not `.env.example`, not
`start.sh`, not `Makefile` — ever set `VITE_LOCAL_ONLY=0`, so no build this repository produced
could reach the cloud interface. It was inherited from the upstream fork, where it _was_ the
main interface, and kept as an escape hatch that only a source-code comment mentioned. A branch
nobody builds is a branch nobody validates: it had already produced four permanently failing
tests (#26), and two of its pages were the largest single consumers of antd. The rule applied
here is the one from #23 — an unsimplified fork is not a smaller dependency, it is a second
product that silently rots.

**How the deletion was bounded** Not by reading: by reachability. A script walked imports from
`main.tsx`, simulating the removed routes as absent, and reported modules nothing could reach.
That found 8 modules (956 lines) already dead _before_ any decision, and after each removal it
was re-run until only `vite-env.d.ts` remained — an ambient-types file, not dead code.
`tsc --noUnusedLocals --noUnusedParameters` then caught the second class of leftovers the
graph cannot see: unused imports and unused locals (45 of them). Three Chinese UI strings that
the local branches already carried moved into the locale files, because inline copy violates
#17 whether or not a flag guards it.

Neither of those tools can see a third class: a JSX expression whose braces were lost, which
compiles and renders the _source_ of the call instead of its result. Two of those shipped into a
build — `t("config.channels.localOnlyDescription")` printed literally in the settings panel —
and only a click-through found them. Any rewrite of this shape needs one pass through the app
afterwards; type checks prove the code is well formed, not that it is right.

**Cost** The upstream fork's interface is gone, so re-adopting cloud providers later means
rewriting those pages rather than flipping a flag. That was an explicit trade: this repository
is maintained on its own and may be rewritten. The cascade also removed `services/config-file`
(settings import/export) and the prompt library, which are user-visible features — they were
unreachable in every build, but someone who only ever ran `VITE_LOCAL_ONLY=0 npm run build` by
hand will notice their absence.

**Result** Frontend 167 modules / 34,112 lines → 145 modules / 29,620 lines. Files importing
antd 60 → 44. antd chunk 916 kB → 739 kB (298 → 240 kB gzip); vendor chunk 1,090 kB → 571 kB
(366 → 192 kB gzip), the latter from dropping the prompt-source and config-file graphs.

**Where** `src/constant/app-mode.ts` (`LOCAL_ONLY` removed), `src/router.tsx`,
`src/constant/navigation-tools.ts`, `src/stores/use-config-store.ts`, and every file that used
to branch on the flag.

---

## 28. Model call scripts are gone; everything is OpenAI-compatible

**Decision** `services/api/model-plugin.ts` is deleted, along with the `script` field on
`ChannelModel`, `resolveModelScript()`, the eight per-capability templates, the LLM authoring
prompt, the full-screen CodeMirror editor, and the `modelPlugin` / `config.scriptEditor` /
`apiErrors.script*` copy. The client no longer evaluates user-authored JavaScript to perform a
model call.

**What it was** An escape hatch for providers whose request shape matched nothing the app knew.
You wrote an async function body; the app injected `prompt`, `images`, `params`, `model`,
`baseUrl`, `apiKey`, `http`, `request`, `poll`, `sleep`, `signal`, `onDelta`, and your code did
the HTTP itself. An empty script meant "use the built-in OpenAI-compatible call", so it was an
override, never a requirement.

**Why it could not fire** Nothing could set a script. The editor's open button was rendered under
`!LOCAL_ONLY`; the local gateway sync maps `name`, `capability`, `label`, `workflowId`,
`baseModel`, `imageInputs`, `variantModes`, `variantLabels` — never `script`. So
`resolveModelScript()` returned `""` on every code path, and all five `if (script)` branches
(image ×3, video, audio) were unreachable. Only a localStorage blob saved by an older cloud build
could have carried one.

**Why not keep it** Three reasons, in order of weight. First, it contradicts the project's core
invariant: the gateway exists to present every ComfyUI workflow as an OpenAI-compatible model, so
the canvas "only changes its base URL" (README). A client-side override is a hole in that
guarantee. Second, unreachable code is untested code — including five branches nobody had run and
an editor nobody could open. Third, it was a `new Function()` evaluation surface shipped in the
bundle for zero reachable benefit.

**Cost** A provider needing a bespoke request shape must now be adapted in the Go gateway. If
client-side scripting is ever wanted again it has to come back deliberately, with an authoring UI
and tests; `git log` keeps the deleted version.

**Result** 991 + 184 lines removed plus the branch bodies; `src` is 145 modules / 29,620 lines →
144 modules / 28,377 lines. Vendor chunk unchanged (571 kB) because the runtime was tree-shaken
out of reachable paths already; the win is comprehensibility, not bytes.

**Where** `services/api/model-plugin.ts`, `services/api/image.ts`, `video.ts`, `audio.ts`,
`stores/use-config-store.ts`, `components/layout/channel-editor-drawer.tsx`,
`i18n/locales/{zh-CN,en-US}.ts`.

---

## 29. Dialogs are a kit primitive too; no antd `Modal` is left

**Decision** All 27 `<Modal>` call sites now render `@/components/kit/dialog`. Its props are the
subset antd's call sites actually used — `open`, `onClose`, `title`, `label`, `footer`, `width`,
`bodyStyle`, `bodyClassName`, `closable`, `maskClosable` — so a call site cannot depend on a
feature that does not exist. `kit/button.tsx` was extracted at the same time, because the confirm
dialog and the two antd-free dialogs needed the same three button looks.

Three behaviours are decided deliberately rather than inherited:

- **Always centred.** 23 of the 27 asked for `centered`; the remainder are content panels where
  centring is not a regression.
- **Closing unmounts the children.** That is antd's `destroyOnHidden`, which 13 sites requested
  explicitly. The others were keeping transient form state alive across opens, which is not worth
  preserving.
- **Footers are explicit.** antd's `okText`/`onOk`/`confirmLoading` are gone; the six sites that
  used them build their own row. More lines, but the loading and disabled states are now visible
  where they are decided instead of inferred from antd props.

**Why this had to carry attributes** This is the part that would have broken silently. The canvas
overlay guards used to match `.ant-modal-root` and `.ant-modal-content`; those class names are
gone, and `lib/canvas-overlay.ts` resolves overlays through `data-canvas-overlay` and
`data-canvas-dialog-content`. The kit dialog emits both, so a dialog opened over the canvas still
swallows clicks and wheel, and the prompt mention menu still measures its boundary. Remove those
attributes and the canvas pans behind its own dialogs — a bug with no compile error anywhere.

**Cost** antd's dialog animated in and out; this one appears instantly. Six call sites had already
opted out of that animation with `transitionName=""`, so for them nothing changed, and the rest
lose a 200 ms transition. There is also no `width` default beyond 520 for panels that omit it.

**Verified** `tsc`, `tsc --noUnusedLocals`, 11 tests and `vite build` clean; in the browser the
resource gate, the asset editor, the system panel, the 980 px settings dialog and the destructive
"clear canvas" dialog all open, close via the X / Escape / their buttons, and the DOM after
closing contains no `ant-modal` at all. The antd chunk went 739 kB → 714 kB (240 → 232 kB gzip).

**Where** `components/kit/dialog.tsx`, `components/kit/button.tsx`,
`components/kit/confirm.tsx`, and the 23 files that used to render `<Modal>`.

---

## 30. Buttons keep antd's prop names, and antd's reset stylesheet is gone

**Decision** `kit/button.tsx` takes `type` / `size` / `icon` / `iconPlacement` / `danger` /
`loading` / `shape` — antd's names — so moving 116 `<Button>` call sites off antd was 30 import
swaps and zero call-site edits. Sizes keep antd's metrics exactly (24 / 32 / 40 px) because these
buttons sit in toolbars measured against those heights. Children render inside
`<span data-slot="text">` and the icon inside `<span data-slot="icon">`, because `globals.css`
collapses the canvas composer's label at narrow widths and needs to select one without the other;
the four `.canvas-composer-icon.ant-btn` rules became `.canvas-composer-icon` plus
`> span:not([data-slot="icon"])`.

**Why keep antd's names** They were never antd's invention: `type`, `size`, `icon` and `danger`
are the words these call sites already think in, and a rename pass would have touched 116 lines to
make them say `variant` instead. The shim is documented at the top of the file so nobody mistakes
it for antd.

**The bug this uncovered** With the kit button in place, the home page's primary button rendered as
an empty white pill. The cause had nothing to do with the button: `antd/dist/reset.css` was
imported from `main.tsx`, so it loaded as an **unlayered stylesheet**, and it contains
`input, button, select… { color: inherit }`. Unlayered CSS outranks _every_ cascade layer, so that
one declaration silently beat every Tailwind text colour on every `<button>` in the app —
including the ones that had been antd's until now, where it made no difference because antd styled
its own internals. `text-white` simply never applied to a button. Tailwind's preflight already
normalises the same elements, so the import is deleted rather than layered; verified afterwards
that antd's Select, Input, Tabs and Switch still render correctly without it.

That failure mode is worth remembering: type checks, unused-symbol checks and the reachability
graph all passed while a primary button was invisible.

**Result** antd component instances 238 → 122; files importing antd 42 → 36. The antd JS chunk did
not shrink (714 kB) because Button is imported internally by the components that remain, so this
step buys removability rather than bytes.

**Where** `components/kit/button.tsx`, `styles/globals.css`, `main.tsx`, and the 30 files whose
antd import lost `Button`.

---

## 31. `Select` is the radix primitive, and `mode="tags"` was never a select

**Decision** `kit/select.tsx` keeps antd's prop shape — `value` / `onChange` / `options` /
`placeholder` / `size` / `loading` / `notFoundContent` — but renders the radix primitive already in
`components/ui/select` (the one `model-picker` uses). The popup portal is marked
`data-canvas-overlay`, which is what lets the canvas stop zooming and deselecting under an open
dropdown without knowing what drew it.

`<Select mode="tags">` at the asset form is a different control and is now `kit/tag-input.tsx`: a
chip list plus a text field, with `,` `，` as separators. Routing it through a listbox bought a
portal, an aria listbox and a hidden search input for a field that has no options to list. It keeps
antd's `value` / `onChange` contract, so `Form.Item` binds to it unchanged — verified by watching
the live preview panel pick up the tags as they are typed.

**Two antd features deliberately not carried over**

- `showSearch` / `optionFilterProp` — the radix listbox has built-in typeahead driven by
  `textValue`. Typing a prefix jumps to the matching option. This is slightly weaker than antd's
  substring filter (it matches from the start), and it is the only behaviour difference from antd
  in this migration. The one call site was a handful of upscale models, so it was not worth a second
  combobox implementation with its own keyboard and aria handling.
- Empty string as a value — radix rejects `value=""`. Both directions travel through a sentinel that
  maps back to `""`, so `value=""` still renders as the placeholder, which is what antd did when no
  option carried `""`.

**The bug the browser caught** The first build rendered **two chevrons**: `kit` drew one so it could
swap it for a spinner while loading, and `SelectTrigger` drew its own. Nothing about that is
type-visible. Fixed with `hideChevron`.

**Dead CSS removed here** The last `.ant-select` users were the three `.canvas-control-select`
workflow rules and `.canvas-compact-control.canvas-control-select`, which had had no matching
element since an earlier pass; plus `.canvas-image-settings-popover .ant-popover-inner`, which had
had none since that panel became a hand-styled portal. `globals.css` now contains no third-party
class name at all.

**Result** files importing antd 10 → 8, and the component kinds left are `Form`, `Drawer`,
`ConfigProvider` and `theme`; antd JS chunk 714 kB → 249 kB (gzip 232 → 87). These are the
first bytes this migration has actually returned, because `Select` was the last component pulled in
from outside the `Button`/`Modal`/`Tooltip` subtree that the remaining components drag along.

**Where** `components/kit/select.tsx`, `components/kit/tag-input.tsx`, `styles/globals.css`, and the
four call sites (`pages/assets/index.tsx`, `components/canvas/canvas-side-panel.tsx`,
`components/canvas/canvas-node-super-resolve-dialog.tsx`, `components/layout/app-config-modal.tsx`).

---

## 32. Drawers are a side of `kit/dialog`

**Decision** `kit/drawer.tsx` reuses the dialog's header / scrolling body / optional footer and
adds `placement` and a width. antd expressed width three ways — `width={640}`, `size={280}` and
`size="large"` — so both `width` and `size` are accepted and resolved to a number (`large` was 736).
`extra` keeps antd's header-right action slot, which the channel editor fills with its save/cancel
pair.

**z-index** 1350/1351: above a dialog (1300) because the channel editor opens _from_ the config
modal, and below a confirmation (1400) because deleting an asset from the detail drawer must still
raise one.

**Where** `components/kit/drawer.tsx` and the three call sites.

---

## 33. antd is gone; here is what that cost and what it returned

**Decision** antd, `@ant-design/icons` and `dayjs` are no longer dependencies. Every component the
app used has a kit equivalent in `components/kit/`, and `Form` has no equivalent at all — its two
real uses hold their values in plain `useState` and its 15 label-only uses became `kit/field`.

**Why reverse #23** #23 said antd stays and its two leaks get quarantined. That was the right call
while the goal was to keep merging upstream. Once the goal became long-term maintainability of a
small local app, quarantine turned out to be a way-station: the seams (#23, #24) and the prop-name
shims (#30) made each component cheap to replace one at a time, and the last four (`Select`,
`Drawer`, `Form`, theme) went the same way. The bundle numbers say the quarantine was doing real
work — the antd chunk stayed at 714 kB from 42 files down to 6, then collapsed 714 → 249 → 219 → 93
→ nothing as `Select`, `Form`, and the providers left.

**What it returned** antd JS chunk 916 kB → 0; vite modules 4126 → 2564; dependencies 32 → 29.
`lib/canvas-overlay.ts` no longer contains a single third-party class name — the contract #23 wrote
("when antd goes, that array goes and the canvas is not touched") is now literally true: the array
went, and the canvas code above it did not change.

**What it cost** Three behaviours are narrower than antd's, and they are worth knowing:

- `Select` typeahead matches a prefix, not a substring (antd's `showSearch` filtered on any
  substring). One call site had it: the upscale model list.
- `Form`'s validation is now written per form, so a _new_ form means writing its own checks rather
  than declaring `rules`. There are two forms left.
- The image toolbar settings dialog renders a preview built from antd tokens; it now uses Tailwind
  classes over `design-tokens`. Nothing reads antd's CSS variables any more, which is why removing
  `ConfigProvider` changed no pixel.

**What the browser caught that the compiler could not** In this pass: a duplicate chevron in
`Select` (the kit drew one and the trigger drew its own), and — after `Form` stopped rendering a
`<form>` element — a browser hint that the API key field was no longer inside a form, i.e. no
password-manager prompt. Both were invisible to `tsc`, `--noUnusedLocals`, the tests and the
reachability graph. The second is the same class of failure as #27's missing JSX brace: deleting a
component deletes the semantics it silently provided.

**Left alone deliberately** `known-issues.md` still describes the aria-hidden warning radix raises
when a confirmation opens over a focused button. It predates this work, it is a browser hint rather
than a fault, and fixing it means patching radix's `hideOthers`.

**Where** `package.json`, `components/kit/*`, `components/kit/field.tsx`, `lib/canvas-overlay.ts`,
`lib/design-tokens.ts`, `components/layout/app-providers.tsx`.

---

## 34. Unused translations are pruned by namespace, never by key _(superseded by #38)_

**Decision** A locale namespace is deleted only when its feature was deleted outright _and_ no
template key can reach it. Everything else unreferenced stays.

**Why** After the deletions there are ~150 translation keys no literal `t("…")` refers to. A script
can find them, but it cannot tell a dead string from one reached through a computed key —
`t(\`settingsPanels.common.${labelKey}\`)`, `t(\`canvas.sidePanel.filter.${value}\`)`and`t(\`assets.kinds.${kind}\`)`all resolve keys that look unreferenced. Deleting one of those shows the
user a raw key like`assets.kinds.image`, which is the same class of bug as decision #27's missing
JSX brace: invisible to the compiler and obvious only in the browser.

So the pruning pass took `prompts`, `imageWorkbench`, `videoWorkbench` and the dead half of
`workbench`, and stopped. The verification is a DOM scan for text matching a key shape, run on every
route.

**Where** `src/i18n/locales/{zh-CN,en-US}.ts`.

---

## 35. What the dependency audit removed, and what it kept against its own rule

**Removed** `@uiw/react-codemirror`, `@codemirror/lang-javascript`, `@codemirror/lang-json` (the
model script editor that used them was deleted in #28), `@tanstack/react-query` (only the provider
was rendered; no `useQuery` anywhere, so the client was a cache that always stayed empty),
`class-variance-authority` (no `cva()`), `react-router` (redundant — `react-router-dom` 7 declares
it as a regular dependency), and `streamdown` (a markdown renderer whose components were never
used; only its stylesheet was imported, for a `.agent-streamdown` element that no longer exists,
which also took 147 lines of dead CSS with it).

**Moved to `devDependencies`** `tailwindcss`, `tw-animate-css`, `shadcn` — resolved when the CSS is
built, never at runtime.

**Kept even though each is small and single-use**, because replacing them is a rewrite, not a
deletion, and each one earns its place:

- `copy-to-clipboard` — `navigator.clipboard` is **undefined on a plain-HTTP origin**, which is what
  this app is served from (`http://192.168.3.232:3000`). Its `execCommand` fallback is load-bearing.
- `nanoid` — for the same reason the codebase avoids `crypto.randomUUID`: that API only exists in a
  secure context. `nanoid` works over plain HTTP.
- `three` — the angle dialog's camera-orbit preview.
- `motion` — the canvas side panel's animation.
- `fflate` — the asset package zip. `axios`, `file-saver`, `localforage` — the network, download and
  storage layers.

**Also removed while auditing** two analytics IDs in `constant/runtime-config.ts`. Nothing imported
them, no script was ever assembled, and no docs or `.env` entry mentioned them, so the feature was
never wired to anything.

**The rule applied to the kit's own props** Three props were declared in a component's type, passed
by a call site, and never read by the implementation: `Alert.showIcon`, `Empty.image`,
`Dropdown.trigger`. A prop that lies is worse than a missing one — the call site looks configured.
Five more were implemented but never exercised (`Space.direction`, `Field.htmlFor`,
`TagInput.disabled`, `Tag`'s red and purple) and were deleted so the branches cannot rot.

**The list this audit left alone, and what became of it** ~120 exported symbols appeared to have
no importer. Most are types in `types/canvas-plugin.ts`, which is the **published plugin contract**
mirrored by `plugins/canvas/sdk/src/types.ts` — plugin authors import them, so "unused in this
repo" means the opposite of dead, and they are still there. The rest were resolved in #37, which is
why that entry insists on the reference graph: several of them were not dead, only unexported.

---

## 36. One HTTP layer, replacing axios and three copies of the error reader

**Decision** `services/api/http.ts` owns the transport. `httpJson` / `httpBlob` / `httpVoid` wrap
`fetch`; `readApiErrorMessage` and `apiErrorMessage` own the failure-to-sentence mapping. The four
service files that used axios now call these, and their three copies of
`readApiErrorMessage` / `readAxiosError` / `statusMessage` are gone.

**Why it was not necessary** Nothing used a capability only axios has. The one thing that would
have justified it — `onUploadProgress`, which `fetch` cannot do — appears nowhere. No interceptors
beyond the one below, no `CancelToken` (the app already passes `AbortSignal`), no transform
pipeline. Meanwhile the codebase was already split: 32 axios calls in four files against 17 `fetch`
calls elsewhere, including SSE streaming and blob downloads.

**The part that had to be carried over, not dropped** `image.ts` registered an axios response
interceptor that called `noteGatewayError` for requests to the local gateway. That is what flips the
shared "reconnecting" banner instead of raising one toast per node when the gateway restarts. It now
lives in `http.ts`'s `send()`: any network failure reports its URL, and `noteGatewayError` itself is
selective about which URLs and which errors count. A test pins it. This is the second time in this
migration that the risk was a behaviour hiding in library-specific plumbing rather than in our code
(see #27's missing JSX brace, #33's `<form>`).

**Two behaviours improved** A timeout is now distinguishable from a cancel: `fetch` is aborted with a
`TimeoutError` reason, so the image service keeps its `imageTimeout` wording instead of the axios
path's `ECONNABORTED` special case in one file only. And a non-2xx that a caller expected to be a
blob is still read as text, where axios produced an unreadable `Blob`.

**Verification** 28 tests in `tests/api-http.test.ts` pin every branch of the mapping — cancel,
timeout, API-supplied message, each status, the fallback, HTML error pages, and the gateway report —
because a browser can demonstrate the result but cannot enumerate the branches.

**Result** vendor chunk 556.7 → 514.4 kB (≈16 kB gzip), runtime dependencies 19 → 17.

**Where** `services/api/http.ts`, `services/api/{image,video,audio,skin-unlocks}.ts`,
`tests/api-http.test.ts`.

---

## 37. An export is a promise, and ninety-eight were not keeping one

**Decision** A symbol is exported only when another module imports it. Where nothing outside the
declaring file referred to a symbol that was exported anyway, the keyword was removed and the
symbol left exactly where it was; where _nothing at all_ referred to it, it was deleted. 583
exports under `src/` became 464.

**Why** An export is a contract with a caller who may not exist. It pins a name, a signature and
a shape, and every later reader has to establish whether something out there depends on it before
changing it. Most of these were leftovers rather than contracts: the canvas upscale and
angle-transform routines lost their callers when the gateway took over those workflows, and
`setPackMainModel` lost its when the resource panel stopped offering a main-model picker.

**How the two cases were told apart** With the TypeScript language service, not with grep.
`findReferences` follows barrel re-exports, resolves aliases, and reports whether each reference
comes from the declaring file or from elsewhere — three things a text scan cannot do. `tsc
--noUnusedLocals` then supplied the other half: a symbol used only inside its own file is not dead
yet, but once it stops being exported it may be, and the compiler says so.

**What grep would have got wrong** `fetchImageModels` has no caller by name outside `image.ts`.
Its caller is `fetchChannelModels`, one file down. A name-only scan deletes a live function here.
The reverse error is just as likely: `components/ui/select.tsx` lists every part in a trailing
`export { … }` block, so each part looks referenced — the file appears to use itself. Removing
those entries exposed the truth, and `SelectGroup`, `SelectLabel` and `SelectSeparator` had no
caller inside the file either; they were deleted rather than narrowed, because a component nothing
renders is not an API.

**Cost** Anyone who wants one of these helpers from outside has to add `export` back — a one-word
change made where its intent is obvious. The cost of the alternative is permanent: a reader cannot
distinguish a narrower module from one that happens to have no callers today.

**Where** `infinite-canvas/web/src/**`, and the commit that removed the sixteen dead symbols.

---

## 38. Unused translation keys are pruned by key, with reachability decided on the AST

**Decision** A key is deleted when no call site can ask for it. This replaces #34, which pruned
only by namespace because a script could not separate a dead string from one reached through a
computed key.

**Why the earlier reasoning could be finished rather than accepted** #34's warning was right about
the _naive_ script, which is why it stopped. The cases it named are all decidable if the analysis
reads the syntax tree instead of the text: `t(\`settingsPanels.common.${labelKey}\`)` and
`t(\`assets.kinds.${kind}\`)`mark a subtree reachable, and`t(cond ? "a.b" : "a.c")`— the shape
this codebase uses for tooltips — contributes both literals, where a regex sees only the
condition. Keys built by concatenation and by`getFixedT("zh-CN")(…)` are handled the same way.

**The four call sites that stop an analysis dead** `t(item.labelKey)` appears four times. Each
source is a local constant list in the same file, so the eight keys are read out by hand and listed
in the analysis. They are the only place the tool is told the answer instead of deriving it, and
that is the stated risk: a new `t(someVariable)` invalidates the pass.

**Verification, because "nothing looks broken" is not one** The new key set must equal the old set
minus the dead set, key for key, in both dictionaries — a set comparison rather than a diff that a
human skims. It does: zh 1118 → 980, en 1099 → 961. Afterwards the analysis reports zero
unreachable keys, and a DOM scan across every route in both languages returns no rendered key.

**What it found** Two whole namespaces: `config.promptSources.*` (~50 keys for a prompt-library
panel that no longer exists) and `canvas.agentOps.*` (labels for an agent feature that is gone).
Two keys that this migration itself had orphaned — `common.durationMinutes` / `durationSeconds`,
whose only caller was `formatDuration`, deleted two commits earlier — which is the analysis
agreeing with the code rather than with its own assumptions.

**Cost** ~200 lines out of the dictionaries, 18 kB of the index chunk. Deleting the wrong one
shows a raw key to the user, so the two guards above are not optional: the set comparison catches
an over-delete _before_ the build, and the DOM scan catches whatever it cannot see.

**Where** `src/i18n/locales/{zh-CN,en-US}.ts`.

---

## 39. All UI copy lives in the dictionaries — including the panel that kept its own

**Decision** `resource-center-panel.tsx` no longer holds a `copyByLocale` object with `zh` and `en`
side by side. Its 102 strings moved to `resources.panel.*` and are read with
`t("resources.panel", { returnObjects: true })`.

**Why, given the old shape was not wrong** It was correct, bilingual and type-checked, which is
why it survived this long. But it was the only copy in the app that `grep resources.panel`-style
maintenance could not find: someone translating a string had three places to look, and the whole
point of #17 is that there is one. The values were extracted from the component's own object by
script and re-emitted verbatim, so the migration could not retype or re-translate anything — the
check was a value-by-value comparison against the originals, 102 of 102 identical in both
languages.

**What had to be preserved** The type. `type Copy = typeof copyByLocale.zh` made `copy.titlex` a
compile error and, more importantly, checked the two `copy[\`runtimeDep\_${id}\`]`lookups. That is
now`type Copy = typeof import("@/i18n/locales/zh-CN").default.resources.panel`: the dictionary is
the source of truth, and a `Record<string, string>`would have silently allowed a typo through as`undefined` at render time.

**The Chinese that stays, and why each one does** Thirteen lines across six files, all deliberate:

- **Protocol values** `PromptOptimizeMode` (`文生视频(T2VA)` …) and the `referenceNote` hint in
  `canvas-node-prompt-panel.tsx` are form fields sent to the gateway; the backend matches the
  former and feeds the latter to a workflow (#9).
- **Parsed data** the `^(物体|人物|文字)` regex and the `人物：` caption prefix in the MOSS layout
  dialog are an agreement with the gateway about the shape of its output, not copy.
- **Decorative art** the mascot slogans in `canvas-node.tsx` are set in a CJK font stack and
  paired with their own English line (`PROCESS GUARDIAN`, `LIGHT BE WITH YOU`). Translating them
  would print the same sentence twice.
- **Endonyms** `中文` in the language picker and `中` on the switcher name a language in that
  language, which is what every language switcher does.

**Cost** One `as unknown as Copy` cast at the boundary, and a component that now depends on the
dictionary's shape for its types. Both are visible where they are written down.

**Where** `components/layout/resource-center-panel.tsx`, `components/layout/resource-setup-gate.tsx`,
`src/i18n/locales/{zh-CN,en-US}.ts` (`resources.*`).

---

## 40. There is no landing page; `/` is a redirect to a canvas

**Decision** `pages/home` is deleted. `/` renders `CanvasEntry`, which resolves to a canvas
address and replaces the URL with it. The app has exactly one kind of screen — a canvas —
and no page that exists only to be looked at on the way in.

The resolution order is deliberate:

1. the canvas you were **last inside**, from a stored `lastProjectId`;
2. otherwise the most recently modified one;
3. otherwise a new canvas.

**Why not "most recently modified" for step 1** Opening a canvas to read it does not touch
`updatedAt`. Look at canvas A for ten minutes, edit B, close the browser: `updatedAt` says B
but you left A. The remembered id is the only thing that actually answers "where was I".

**Why `replace`, and why it matters more than it looks** Without it the history keeps `/` and
every Back press bounces through the redirect again. This single attribute is the difference
between "the URL is the canvas" and a browser that fights you.

**What it costs** The app has no brand surface left: the toolbar title shows the canvas name,
and `无界工作台` now appears only in the browser tab, appended to the canvas name. That was
the trade the request asked for — the tool is the work, not a front door — but it should be
named rather than discovered.

**Two bugs this surfaced, both pre-existing** The dictionary never had `assets.kinds.audio`,
which the built-in Audio node asks for by name, and it never had `agent.composer.mentions.*`,
which two error paths in `canvas-resource-references.ts` still threw. Both printed raw keys
to the user. The node title was wrong since audio nodes existed; the error messages only fire
when a reference image cannot be read, which is why neither was noticed. They were found by a
check that compares every literal `t("…")` against the flattened dictionary — the inverse of
the dead-key pass in #38, and worth running whenever keys move.

**Follow-up: the list itself became a panel** `pages/canvas/index.tsx` (116 lines) is gone and
`/canvas` no longer renders a page. The list floats over the canvas you are already in, opened
from the switcher or the canvas menu. It is the same body — multi-select, batch export, import,
delete — because that part was always right; only the container changed.

The test that settled it: is this worth a URL? A canvas is — it can be shared, bookmarked, and
reloaded into the same state. A list of canvases cannot: nobody shares "my canvases", refreshing
it means nothing, and Back is worse than Escape. What a route bought was an unmount of the canvas,
a route change, and a full remount to get back to where you already were.

Two details worth keeping. `CanvasDeleteProjectsDialog` renders as a sibling of the manager, not
a child, so the two are independent radix roots rather than a nested focus trap. And `/canvas`
still resolves — to a redirect, so old bookmarks land on a canvas instead of a 404.

**Where** `src/pages/canvas/entry.tsx`, `src/router.tsx`, `stores/canvas/use-canvas-store.ts`
(`lastProjectId`), `components/canvas/canvas-top-bar.tsx` (the switcher this made obvious:
switching used to mean leaving the canvas for the list page), `components/canvas/canvas-manager-dialog.tsx`.

---

## 41. `npm run check` is the third gate, because the compiler cannot see these two bugs

**Decision** Two scripts in `infinite-canvas/web/scripts/` run as `npm run check:i18n` and
`npm run check:exports`, together with `tsc --noEmit` under `npm run check`.

**Why they exist** Both catch a class of bug that type-checks, builds, and passes tests while
being visibly broken:

- **A missing translation key.** `fallbackLng` is `zh-CN`, so an absent English key renders
  Chinese — no error, no warning, just a sentence in the wrong language. The inverse is worse:
  deleting a key that is still asked for prints the key itself on screen. This has happened
  three times in this repository — `assets.kinds.audio` (the built-in Audio node's title, wrong
  since audio nodes existed), `agent.composer.mentions.*` (two error paths), and
  `navigation.config` (the settings button's aria-label, deleted as a dead key by the very commit
  that made it dead).
- **An export nothing imports.** An export is a promise to a caller who may not exist, and a
  `tsc` pass cannot tell an unused export from a used one. See #37 for why grep is not enough.

**What makes them checks and not trivia** They are not advice in a document; they exit non-zero.
`check:i18n` compares every literal `t("…")` against the flattened dictionaries in both
directions, compares the two dictionaries to each other, and treats a `t("…", { returnObjects })`
object path as a branch rather than a missing leaf. `check:exports` uses the language service.

**The two exceptions, written down where the script can see them** `chinese()` in
`lib/gateway-labels.ts` asks only the zh dictionary, so `gateway.models.*` and
`gateway.variants.*` are zh-only on purpose — enumerating them in `check-i18n.cjs` stops the
"only in zh-CN" check from failing every run. And four `t(variable)` call sites read their key
from a local constant list; the eight resulting keys are listed by hand in `HAND_RESOLVED`,
which is the one place the tool is told the answer instead of deriving it. The script prints any
`t(variable)` it meets so that list stays honest.

**Renamed while passing through** `canvas.projectPage.*` (37 keys) became `canvas.runtime.*`.
`projectPage` named a page that no longer exists — the list is a panel now — and these keys were
never page furniture: they are stop-generation, export, clipboard and failure messages emitted
_by_ the canvas. `canvas.project.*` keeps the list-management keys, which is what "project"
actually means here.

**Where** `infinite-canvas/web/scripts/check-i18n.cjs`, `scripts/check-exports.cjs`,
`package.json`.

## 42. Ten "which node has which dialog open" states become one

**Decision** `project.tsx` held ten independent `useState<string | null>` — `cropNodeId`,
`maskEditNodeId`, `outpaintNodeId`, `mossLayoutNodeId`, `splitNodeId`, `upscaleNodeId`,
`superResolveNodeId`, `videoUpscaleNodeId`, `angleNodeId`, `infoNodeId` — each meaning "this
editor is open on this node". They are now one nullable record:

```ts
type CanvasNodeTool = "info" | "crop" | … | "angle";
const [activeNodeTool, setActiveNodeTool] = useState<{ kind: CanvasNodeTool; nodeId: string } | null>(null);
```

with `openNodeTool(kind, nodeId)` / `closeNodeTool()`, and the ten `xxxNode` render variables
derived from it by a one-line kind check each.

**Why not leave it alone** It was not merely verbose, it was a bug generator. Three code paths
reset "the open editor" and each cleared a different, incomplete subset:

| path                 | cleared                                                                                                |
| -------------------- | ------------------------------------------------------------------------------------------------------ |
| `deleteNodes`        | info, crop, maskEdit, angle — **not** outpaint, mossLayout, split, upscale, superResolve, videoUpscale |
| `clearCanvas`        | the same four                                                                                          |
| the `Escape` handler | info, crop, maskEdit only                                                                              |

So pressing Escape with the split dialog up did nothing. Opening the outpaint dialog and then
deleting the node from the keyboard left `outpaintNodeId` pointing at an id that no longer
existed — invisible only because every render site guards on `node.metadata?.content`, so the
stale id silently rendered nothing and stayed set until the next open. With one state the resets
are one line each and cannot drift apart again.

**The invariant it buys** "At most one node editor is open" was previously a convention nobody
enforced; nothing in the type system prevented `cropNodeId` and `splitNodeId` from both being
set. Now it is the shape of the state. `dialogNodeId` deliberately stayed separate: that one is
the composer panel, which is long-lived and _does_ coexist with a modal (you can have the prompt
panel open while a crop dialog floats above it). Only the modal ten were collapsed.

**Cost** 52 `useState` in the component became 43. The diff is 59 insertions and 64 deletions,
almost all of them mechanical renames the compiler checked.

**Where** `infinite-canvas/web/src/pages/canvas/project.tsx`.

## 43. The node editors are a module with a nine-key contract, not a closure

**Decision** Nine editors moved out of `project.tsx` into
`pages/canvas/hooks/use-image-editors.ts` (crop, split, mask, outpaint, MOSS layout, super-resolve,
fast upscale, multi-angle) and `pages/canvas/hooks/use-video-editors.ts` (video upscale, frame
interpolation). `project.tsx` was 3,981 lines; it is now 3,459. Five modules appeared:
the two hooks, `hooks/node-editor-deps.ts` (a type), `lib/canvas/canvas-generated-video.ts` and
`lib/canvas/canvas-node-status.ts`.

**Why these nine and not "generation"** They were the only large, genuinely closed region left.
Every one of them follows the same shape — take the node you were handed, append a child beside it,
wire a connection, select the child, open its composer, and (where it generates) drive exactly one
request. Not one of them _reads_ the graph. That is what made the extraction honest: the whole
surface they need is six setters, two request-registry calls and `closeNodeTool`. Everything else —
`effectiveConfig`, `isAiConfigReady`, `openConfigDialog`, `t` — they can read themselves from the
stores, so it is not in the contract.

`dialogNodeId` and the ten-entry `activeNodeTool` record stayed in the page: the page owns _which_
editor is open, the editor module only owns what happens once you press confirm. Rendering the ten
dialogs stayed in the page too — they are page furniture, positioned against page state.

**Two things fell out of the move**

- `applyGeneratedVideo` and the 420 px video cap were used by both the poller (which stays) and the
  video editors (which left), so they went to `lib/canvas/canvas-generated-video.ts` rather than
  being duplicated or imported page-ward from a hook.
- `NODE_STATUS_*` were four `"idle" | "loading" | "success" | "error"` literals declared in
  `project.tsx` and re-typed by hand in the node components. Now that two more modules needed them
  they moved to `lib/canvas/canvas-node-status.ts`.

**What this is not** It is not a line-count win: 522 lines left the page and 603 arrived elsewhere,
because each hook carries its own imports and the contract type is written down instead of being
implicit. The win is that "what a node editor may touch" is now a 23-line type you can read in one
screen, and that proving an editor correct no longer means standing up a 3,459-line component.

**Where** `infinite-canvas/web/src/pages/canvas/hooks/`, `infinite-canvas/web/src/lib/canvas/`.

## 44. `handleGenerateNode` was 440 lines and four of them did not talk to each other

**Decision** The four generation modes — image, video, audio, text — moved out of `handleGenerateNode`
into `pages/canvas/hooks/use-generation-runs.ts`, one function each. `handleGenerateNode` is now a
123-line orchestrator: resolve config, hydrate the prompt, build a `GenerationRun`, dispatch on mode,
own the shared `catch` and `finally`.

`project.tsx`: 3,459 → 3,150 lines.

**What the four actually shared** Almost nothing. A 440-line function was: ~45 lines of plugin
`writeBackToSelf` special case, ~40 lines of shared setup, then four branches of 143 / 47 / 29 / 115
lines, then a `catch`/`finally`. The branches agreed only on inputs — which node, the hydrated
prompt, the resolved config, whether to overwrite the source, and the run controller. That is the
whole of `GenerationRun`, and it is why the split is a move and not a rewrite.

**The one thing that is not a pure move** `pendingChildIds`. Each mode records the nodes it created
so the caller's `catch` can stamp them as failed; it has to survive the mode returning early, so it
is a mutable field on the run rather than a return value. Four `pendingChildIds = [...]`
assignments became `run.pendingChildIds = [...]`. Everything else in the four bodies is
byte-identical to what was there before — verified by diffing the moved ranges against
`git show HEAD` with indentation normalised, which leaves only the `}, [deps]);` closer.

**Why not also `handleRetryNode`** It looks like the same function and is not. Retry writes into the
node that already exists instead of spawning a sibling, has no `mode`, and branches on
saved-metadata-vs-hydrated-context instead. Merging the two would have meant a flag for every
difference. It stays where it is, at ~340 lines, and is the next thing worth its own pass.

**Which state stayed in the page** `runningNodeId`, `setExpandedBatchNodeIds` and the error
stamping in `catch` — because they are about the canvas, not about one request. The five setters and
the two registry calls go in as `GenerationRunDeps`, the same shape as `NodeEditorDeps` (#43).

**Where** `infinite-canvas/web/src/pages/canvas/hooks/use-generation-runs.ts`.

## 45. Retry joins the generation module, and `project.tsx` drops under 3,000

**Decision** `handleRetryNode` (156 lines) moved to `use-generation-runs.ts` as `retryNode`.
The page now holds no generation logic at all: `handleGenerateNode` dispatches, `handleRetryNode` was
a second entry point, and both live behind `useGenerationRuns`. `project.tsx`: 3,150 → 2,990 lines.

**Why it belongs there even though it shares no code** It looked like a duplicate of `handleGenerateNode`
and is not. Retry has no `mode`, spawns nothing, and writes into the node that already exists — into
the image slot that failed, if one was named. Its four type branches total 88 lines where generation's
total 334. What they share is not code but _ownership_: both resolve a config, both check it is ready,
both drive exactly one request into exactly one node, both stamp the outcome. Keeping one in the page
and one in the module was the wrong seam; the seam is "the page owns the canvas, the module owns
generation", and it is now consistent.

**`GenerationRunDeps` grew by four** `nodesRef`, `connectionsRef`, `setRunningNodeId`, `pollVideoNodeTask`.
The first two are the only place the module reads the graph rather than appending to it —
`findRetrySourceNode` has to walk backwards from the failed node to find what produced it. That is a
real difference from the editors (#43), which never read, and it is why retry needed refs where they
needed nothing.

`effectiveConfig`, `isAiConfigReady` and `openConfigDialog` are read from the stores inside the hook,
as in #43, so they are not in the contract.

**Verified the same way** The moved body diffs against `git show HEAD` at 146 non-blank lines in,
146 out, zero differences.

**The gap** `retryNode` was not exercised in a browser. Retry only appears in the node toolbar and
context menu for a node in `error` status, and there is no way to manufacture one without a failing
API call. What was checked live: the page loads on the new bundle, nodes render, selection and the
context menu work, and the console is empty. The body is a byte-identical move and the contract is
type-checked, but the runtime path itself remains unexercised — worth one click from a human on a
node that has actually failed.

**Where** `infinite-canvas/web/src/pages/canvas/hooks/use-generation-runs.ts`.

## 46. Three pieces of housekeeping, and what two of them turned out to be

All three came out of the same audit as #41–#45 and all three changed shape once I read the code.

### `components/layout/` was 80% settings

Ten files lived under a name that described two of them. `app-providers` and `client-root-init`
are the app shell; the rest are the settings feature — the config modal (610 lines), the resource
centre (881), the channel editor, the model picker, the storage-usage panel and the setup gate that
opens the resource centre when nothing is configured.

Six files moved to `components/settings/`. `components/layout/` keeps four: providers, root init,
the reconnect banner and the status actions. The split is by _what the file is_, not by where it
renders — all ten render from the same two places.

### The stores had no rule

`stores/` held `use-panel-store` and `use-canvas-side-panel-store` next to `use-asset-store` and
`use-theme-store`, while `stores/canvas/` held the graph and the plugin registry. Two of those four
are about a canvas and two are about the app, and nothing said which was which.

The rule now holds and is visible from the directory listing: **`stores/` is app-wide** (assets,
config, theme); **`stores/canvas/` is everything about a canvas** (the graph, plugins, the floating
panels, the side panel, the canvas list). Three files moved and two were renamed:

| was                                     | is                                             |
| --------------------------------------- | ---------------------------------------------- |
| `stores/use-panel-store.ts`             | `stores/canvas/use-canvas-panel-store.ts`      |
| `stores/use-canvas-side-panel-store.ts` | `stores/canvas/use-canvas-side-panel-store.ts` |
| `stores/canvas/use-canvas-ui-store.ts`  | `stores/canvas/use-canvas-list-store.ts`       |

**The merge I proposed and did not do** The audit said to fold `useCanvasUiStore` into
`usePanelStore`. Reading them, that is wrong: `useCanvasPanelStore` answers "which floating panel is
open", which is app-level navigation state that outlives any panel; `useCanvasListStore` holds which
canvas rows are ticked and which one is being renamed, which is widget state that belongs to one
open panel. Merging them would have produced a store with two different lifetimes inside it. The
name was the problem — `ui` said nothing — not the split.

### The storage panel reported a number that was quietly wrong

`local-storage-usage.ts` measured one database, hardcoded, named `"infinite-canvas"`. But plugins
write to a second one, `"infinite-canvas-plugins"`, one object store per plugin id. A canvas with
heavy plugins therefore showed less than the browser was actually holding against the quota, and
nothing anywhere said so.

All the database and store names are now in `services/localforage-databases.ts`, which four files
import instead of repeating the strings, and the reader measures both databases.

It also no longer creates what it measures. `indexedDB.open(name)` _creates_ a database that is not
there, so the old reader would have materialised an empty `infinite-canvas` on a fresh profile just
by visiting the settings tab. `indexedDB.databases()` answers "which exist" without touching
anything; the open is only reached for names the browser confirms.

**Two labels were pointing at stores that no longer exist** `agent_chat_messages` and `prompt_cache`
were in `storeLabelKeys` — and their translations in both dictionaries. `check:i18n` could not see
they were dead because the lookup is a template literal, and a template literal registers as a
_prefix_, so everything under `config.localStorage.stores.` counted as reachable. Removed from both
locales along with the labels: 1108 → 1107 keys.

**Verified** `npm run check`, strict `tsc`, 39 tests, build. In the browser the panel renders
`infinite-canvas · v7` with the real per-store breakdown, and `indexedDB.databases()` after visiting
the tab still returns exactly one database — the absent plugin one was reported as absent rather
than created and shown empty.

**Where** `components/settings/`, `stores/canvas/`, `services/localforage-databases.ts`,
`services/local-storage-usage.ts`.

## 47. Content ingress is a module: `File | string` in, node out

**Decision** Eight callbacks that turn something the user handed us into a node moved to
`pages/canvas/hooks/use-content-ingress.ts` (310 lines): the three `create*FileNode`, the clipboard
text node, the system-clipboard paste, the file-picker request, the file-picker handler, and drop.
`project.tsx`: 2,990 → 2,735 lines.

**The line that makes it a module** Not one of them reads the graph. They take a `File` or a string
and append. The only thing they need from the page is where the viewport is looking
(`screenToCanvas`, `getCanvasCenter`, `size`, `containerRef`) plus five setters. That is
`ContentIngressDeps`.

**The picker owns its input** `imageInputRef` and `uploadTargetRef` moved into the hook rather than
being passed in, because the module owns the behaviour that clicks the hidden `<input type="file">`.
The page still renders the element — it destructures `imageInputRef` and attaches it — but it no
longer knows what the ref is for. Passing it in would have been the page owning a piece of the
module's mechanism.

**What stayed behind** `pasteCopiedNodes` (copy/paste of _canvas nodes_, which is graph logic with
connection re-mapping, not content ingress) and `handleAssetInsert` (which inserts from the asset
library and asks the page where the selection is).

**A line was almost lost** The first attempt sliced one line too far and left `handleNodeResize`
with an empty body — a callback that silently does nothing. The compiler caught it only because the
destructured parameters became unused under `--noUnusedParameters`. Every move since then is
checked by reconstructing HEAD minus the moved ranges and diffing it against the result; that diff
is now exactly zero, which is a stronger statement than "tsc passes".

**Verified end to end, not just compiled** The hidden input was driven from the browser with a real
`File` built from a canvas and a `DataTransfer`: a node titled `ingress-test.png` appeared with its
image, selected, composer open, and was deleted afterwards. Console empty.

**Where** `infinite-canvas/web/src/pages/canvas/hooks/use-content-ingress.ts`.

## 48. Three small correctness fixes found by a full read of the frontend

The audit that produced #41–#47 stopped after the first four items. This is the pass that finished
it: 168 files, 29,850 lines, read rather than grepped for keywords.

**What the scan said about the codebase overall** Zero `any`, zero `@ts-ignore`, zero empty
`catch`. Seven non-null assertions, five `eslint-disable`, and every one of the five carries a
comment saying why — three of them in `canvas-node-prompt-panel.tsx` exist to keep a text input from
being reset mid-typing. There is no category of "type system bypassed everywhere". The problems are
in the seams between files, which is exactly what a compiler cannot see.

### The side-panel width had two owners

`use-canvas-side-panel-store.ts` declares `WIDTH_KEY = "canvas-side-panel-width"` and reads it at
boot. The _write_ was a bare literal in `canvas-side-panel.tsx`, on pointer-up:

```ts
localStorage.setItem("canvas-side-panel-width", String(nextWidth));
```

Two files, one key, no link between them. Change the constant and the panel silently stops
remembering its width; change the literal and the same. And `setWidth` was the only setter in that
store that did not persist — `openPanel`, `closePanel` and `setNodeViewMode` all do — so the store
was internally inconsistent about what "set" means.

Writing on every pointer-move would hammer localStorage, which is presumably why it was done at
pointer-up in the component. So the store grew `commitWidth()`, which writes the current width, and
the component calls that instead. Same write frequency, one owner, and `setWidth` is again just the
live setter it looks like.

### Two known bugs, both in the resource centre

Known issues #4 and #5 had been sitting there with the fix written down. #4: the workflow status tag
fell through to "Restart needed" for _any_ unavailable workflow, including one blocked by an
uninstalled custom node — a label promising something a restart will not deliver. It is now a
three-way branch, and a workflow carrying `problems` says "Load failed".

#5: the "Canvas entry" tag rendered exactly when `modelVisible === false`, i.e. when the workflow is
hidden from the picker. The audit offered "invert the condition or rename the label"; the label was
renamed to "Hidden from picker", because inverting a condition on a guess about intent changes
behaviour, and this one does not.

Both were one file away from the `components/layout` → `components/settings` move in #46, which is
how they got found again — known issue #4 still pointed at the old path.

### One thing found, documented, not fixed

`use-canvas-store` and `use-asset-store` both `JSON.parse` persisted state with no guard. A corrupt
blob makes the parse throw, zustand swallows it into `onRehydrateStorage`, that listener ignores the
error, and the app comes up with empty defaults over data that is still in IndexedDB. Recorded as
known issue #12 rather than fixed here, because the half that matters — telling the user their saved
state could not be read — is a product decision, and the parse half on its own changes nothing
anybody can see.

**Verified** `npm run check`, strict `tsc`, 39 tests, build. The width fix was checked end to end in
the browser: drag the resizer 280 → 400, reload, the panel comes back at 400. The two tag fixes
were not exercised, because they need the gateway to report a workflow with problems or a hidden
one, and the local gateway reports neither.

**Where** `stores/canvas/use-canvas-side-panel-store.ts`, `components/canvas/canvas-side-panel.tsx`,
`components/settings/resource-center-panel.tsx`, `i18n/locales/`.

## 49. An unreadable store is preserved, refused, and explained

**Decision** Both `persist` storage adapters catch their own parse failure instead of letting it
escape. On failure they copy the raw bytes to `<key>.corrupt-<timestamp>`, record the failure in
`stores/use-storage-recovery-store.ts`, return `null` so hydration proceeds with defaults on
purpose, and from then on refuse every `setItem`. A banner
(`components/layout/storage-recovery-banner.tsx`) says what happened and offers three things.

**Why catching is not enough on its own** Known issue #12 was reproduced before this was written. A
2314-byte `canvas_store` holding one canvas with two nodes was replaced with the string
`{oops truncated` and the page was hard-reloaded. The app came up on a brand-new project id with
"No nodes on this canvas", the console was **empty**, and within seconds the stored blob was 378
bytes — the empty project. The original was gone from the browser. So the failure mode was not
"boots empty", it was "boots empty and then destroys what it could not read".

That is the reason for the write refusal. Returning `null` alone would have left the second half
intact: zustand's `persist` wraps `setState`, `hydrated: true` is a state change, and the canvas
store's dedupe guard is skipped when `queuedPersistState` is `null` — which it is, precisely
because the parse failed.

**The three actions, in the order that protects the user** Export the raw bytes first: it is the
only action that changes nothing and loses nothing, and it turns an opaque failure into a file the
user can keep. Retry second: it re-reads, which genuinely helps the transient case (a write caught
mid-flight, a quota hiccup) and is harmless in the permanent one. "Clear and start over" last and
styled as a link, not a button, because it re-enables writing over the original key — though it
cannot lose anything, since the copy is already on disk.

**Two details that only showed up in the browser**

- The banner was `top-4` and overlapped the canvas title and the top-bar controls. Moved to
  `top-16`, which is where the reconnect banner already sits.
- "Clear and start over" dismissed the banner but wrote nothing, because `setItem` only runs on a
  state change and dismissing is not one. Reloading immediately after showed the banner again, which
  reads as a broken button. The handler now nudges both stores with `setState({})` so the cleared
  state actually replaces the blob.

**Scope** The banner renders in `layouts/user-layout.tsx`, not in the canvas list, because `/`
resolves straight to a canvas — most sessions never open the list, and a message that only appears
there would be seen by almost nobody.

**Verified** The reproduction was repeated against the fix: banner appears, main key still holds
`{oops truncated`, a `.corrupt-2026-10-30T19-28-33-388Z` copy exists, "clear and start over" then
rewrites the main key to a valid 378-byte blob. The origin's data was restored from a backup
afterwards and checked: two nodes, correct title, no banner, console empty.

**Where** `stores/use-storage-recovery-store.ts`, `lib/persist-recovery.ts`,
`components/layout/storage-recovery-banner.tsx`, both persisted stores.

## 50. Every floating panel is 1000×646, or it is not a panel

**Decision** `Dialog` no longer takes `width`. It takes a required `size` from a closed set, and
`size` is not a hint:

```ts
const SIZE_CLASS = {
  panel: "h-[min(760px,88dvh)] w-[1000px]",
  form: "max-h-[90dvh] w-[520px]",
  auto: "max-h-[90dvh] w-auto",
};
```

Thirty call sites were classified. Nineteen are `panel` — the canvas list, the asset library, the
settings modal, the plugin manager, the asset picker, the model picker, the quick-tools settings,
the prompt editor, and all eight image/video editors. Nine are `form` — confirmations, the shortcut
sheet, system status, the cloud-API editor, the card-skin picker, runtime help. Two are `auto`, and
only two: the image preview and the media preview, whose size _is_ the image.

**The problem it fixes** Before this, every dialog's width was a literal between 440 and 1100 and
**not one of the thirty set a height** — height was whatever the content happened to be, capped at
90dvh. So:

- settings (980) → asset library (1100) → canvas list (920) → plugin manager (640): the frame moved
  sideways by 180px between any two of them, and vertically by whatever the content did;
- inside settings, switching between Providers, Prompt API, Preferences, Add workflows and Local
  storage resized the same window five times, because those five tabs have very different content.

A window that resizes to its contents stops reading as a window. The user's sense of "where I am" in
a spatial UI depends on the frame staying put.

**Why not one size for everything** A confirmation is one sentence and a paragraph. Forcing it to
1000×646 would be its own kind of wrong — a giant box that shouts about something small. Keeping
`form` content-sized preserves that, and nothing is lost, because a confirmation is not a place you
_are_, it is a question you answer. `auto` stays for the same reason from the other direction: a
preview's size is information, not layout.

**Why `size` is required and there is no default** A default would have let an unclassified call
site keep its old behaviour silently. Making it required turned the compiler into the checklist:
after the kit changed, `tsc` listed all thirty sites and could not be satisfied until each was
classified.

**Accepted cost** A panel with little content leaves space at the bottom. The video upscale editor
is the clearest case. Measured on a 734px-tall window the panel is 646 tall (`88dvh`); on a 1080p
screen it is 760, and only the tallest editors scroll.

**Measured after the change** Settings across all five tabs: 1000×646, 1000×646, 1000×646, 1000×646,
1000×646. Canvas list: 1000×646. Asset library: 1000×646. Crop editor: 1000×646. Clear-canvas
confirmation: 520×161, unchanged.

**Where** `components/kit/dialog.tsx` and the thirty call sites.

## 51. The editors' canvases fill the panel instead of guessing at the viewport

**Decision** The four editors that sized their image area against the _viewport_ now size it against
the _panel_. The dialog body becomes a flex column (`bodyClassName="flex flex-col"`) and the image
area takes the remaining space (`flex-1 min-h-0`), with a small `min-h` as a floor rather than a
typical value.

| editor    | was                                 | is                                          |
| --------- | ----------------------------------- | ------------------------------------------- |
| crop      | `h-[min(62vh,620px)] min-h-[340px]` | `flex-1 min-h-[260px]`                      |
| mask edit | `h-[min(68vh,720px)] min-h-[360px]` | `min-h-[320px]` (stretched by the grid row) |
| split     | `h-[340px] min-h-[300px]`           | `flex-1 min-h-[240px]`                      |
| node info | `h-[56vh] min-h-[360px]`            | `flex-1 min-h-0`                            |

**Why this was needed at all** Prettier-adjacent: #50 made every panel a fixed height, and these four
had heights expressed in `vh`. On a viewport where `88dvh` is smaller than `62vh` plus chrome — which
is exactly what a 734px-tall window gives — the editor content came out taller than the panel and the
body scrolled. Measured before: the crop dialog needed a scrollbar and its footer was below the fold.
Measured after: `scrollHeight - clientHeight` is 0 in all four, and the crop dialog's Reset / Cancel /
Crop row is visible without scrolling.

The numbers were also the wrong kind of number. `62vh` is a guess about how tall an image wants to be
on somebody else's monitor; `flex-1` is the actual answer, because the panel already knows its height.

**A predating bug found while measuring** In the mask editor the action row needs about 360px for
Reset, Export to canvas and Generate, and sits in a fixed `320px` grid column — so Generate was
clipped by the dialog's `overflow-hidden`. This was true at the old 980px width as well, since the
column was 320 then too; the arithmetic, not the change, is what shows it. The row now wraps
(`flex-wrap`), so the three buttons reflow instead of the last one disappearing.

**Where** `components/canvas/canvas-node-{crop,mask-edit,split}-dialog.tsx`,
`components/canvas/canvas-node-hover-toolbar.tsx`.

## 52. The persisted format has a version, and a migration that can refuse

**Decision** Both persisted stores carry `version: 1`, and both read their blob through a module that
checks the version, migrates an older blob, and then asserts the result has the current shape. A
throw from any of those three steps is the same signal as invalid JSON: copy the blob aside, refuse
to write, show the banner.

**What was wrong without it** zustand's default `merge` is

```js
(persistedState, currentState) => ({ ...currentState, ...persistedState });
```

which is **shallow at the top level**. `projects` is replaced wholesale by whatever was on disk, so
nothing inside a project ever receives a default. A field added to `CanvasProject` comes back
`undefined` on every canvas that predates it. `viewport` is the sharp case: it is `{x, y, k}` and
every coordinate on the canvas passes through `k`, so a missing `k` is not a cosmetic gap, it is
`NaN` in the transform and a blank canvas.

And nothing checked. Both stores left `version` at zustand's default of `0`, so the stored version
and the expected version were always equal and the comparison always took the branch that uses the
state unexamined.

**Why the migration lives in `getItem`, not in zustand's `migrate`** zustand runs its migration
_after_ `getItem` returns. A failure there cannot reach this app's recovery: it logs
`State loaded from storage couldn't be migrated` and discards the state, and the store then persists
defaults over the blob — the data-loss path #49 was written to close. Worse, it would have got
through even with the write refusal, because `getItem` had succeeded. Doing the work inside
`getItem` means there is exactly one failure path, and it is the one with a banner on it.

**The three steps**

1. _Version check._ A blob from a **newer** build throws rather than being guessed at. There is no
   safe interpretation of a format we have never seen.
2. _Migration (`version < 1`)._ Backfills what older blobs lack — `lastProjectId`, and per project
   `chatSessions`, `activeChatId`, `backgroundMode`, `showImageInfo`, `viewport`. The per-project
   pass is the part zustand's shallow merge structurally could not do.
3. _Assertion._ Shallow, but it guarantees the canvas can render: every project has an id, a title,
   array `nodes`, array `connections`, and a viewport with finite `x`, `y` and a positive `k`. This
   is what catches a shape change nobody migrated.

**Tested both ways**

`tests/persist-migration.test.ts` — 14 cases against the real read path: a current blob passes
through, a versionless blob gains `lastProjectId`, a project missing the newer fields is backfilled,
a viewport with no `k` becomes `k: 1`, a zero zoom is treated as unset, a newer version throws,
malformed JSON throws, `nodes` that is not an array throws, an asset with no `data` is dropped.

End to end in the browser: a forged versionless blob whose viewport had only `x` and `y` was written
to IndexedDB and the page reloaded. It came back at `version: 1` with `k: 1`, both nodes intact,
rendering at 100% — no `NaN`, no blank canvas, no banner (it was migratable, not unreadable). The
original blob was restored afterwards and checked.

**Where** `stores/canvas/canvas-persist.ts`, `stores/asset-persist.ts`, both stores,
`tests/persist-migration.test.ts`.

## 53. One run protocol; media type is data, not routing

**Decision** Every generation is a `run`: `POST /api/runs` returns an id, and status, progress,
outputs and cancellation all hang off that id. Image, video and audio are three workflows, not
three families of endpoints. `wait`-style blocking endpoints do not exist. The target is written
down in [`protocol.md`](protocol.md); this entry records why, and what lands first.

**What was wrong without it**

- The API grew as _media × action_: `images/generations`, `images/edits`, `images/upscale`,
  `videos`, `videos/upscale`, `videos/interpolate`, `audio/speech`. Every new capability adds a
  route, a handler and a test, and the handlers drift apart.
- Execution model was chosen by media type, not by how long the work takes. Images and audio
  blocked inside one HTTP request; video got a job. Music generation takes minutes, so it ended up
  on the blocking path — exactly the case where a dropped connection loses the work.
- Handler code contained workflow identity: `entry.ID == "video-h3-t2v"`, `"video-h3-fast"`,
  `"video-h3-frames"`, `"video-h3-ref2va"`, `image-qwen21`, `image-moss-*`. Adding a workflow meant
  editing Go, which contradicts `registry.json` being the workflow data.
- One concept, several spellings: duration is `seconds` for video and `duration` for audio;
  resolution is `size`, `quality`, `vquality` and `resolution_name`; everything else went into a
  `params` field that accepts both an object and a JSON string.
- Uploads were classified by magic field names (`contains "image"` or `"frame"`), so an OpenAI
  `mask` could not be expressed at all, and roles depended on field order.
- Outputs had three retrieval paths: base64 in the body, `/files/{name}`, and
  `/v1/videos/{id}/content`.

**Why the id, and why no `wait`** A blocking variant returns two different success shapes (`200`
with the artifact, `202` with a run), so clients need polling anyway — while the blocking call
brings back the failure the id exists to prevent: the connection dies and the finished work is
unreachable. One shape, one code path, and the work survives reloads and disconnects.

**What lands first** The execution core, because it is shared by every later phase: submission,
queueing, persistence, progress and cancellation move into one helper over the existing
`internal/task` store, and audio joins video on it. Audio then stops being a blocking generation —
its `/v1/audio/speech` adapter submits a run and waits on the run, and the run survives the client.
`/v1/*` stays as a thin adapter so the canvas keeps working and the existing e2e tests keep
guarding the behaviour until their node type migrates.

**Order** (each phase leaves `make check` green): execution core → `accepts`/`inputs`/`outputs` in
`registry.json` plus a generic validator, deleting the per-workflow branches → `/api/runs` and
`/api/uploads` next to the adapters, canvas migrating node type by node type → delete adapters,
`/v1/*` and this table's "today" column.

**Where** `docs/protocol.md`, `internal/server/runs.go`, `internal/task`, `internal/server/routes_*.go`.

## 54. Media rules are registry data, not handler branches

**Decision** What a workflow may accept from a request — how many images, one or none, which media
kinds, that first/last-frame mode needs two images for `both` but one otherwise — is declared per
workflow in `registry.json` (`accepts`, `frameMode`, `mode`, `promptInject`, `promptReorder`,
`sourceVideoFrames`) and resolved by one function, `workflow.PlanMedia`. The handlers no longer
mention a workflow id.

**What was wrong without it** `handleVideoCreate` carried a `switch entry.ID` with four arms
(`video-h3-t2v`, `-fast`, `-frames`, `-ref2va`), then a second `switch` for the same ids to build
the error hint, plus `requiredImageCount` reverse-engineering minima out of bindings.
`handleImageEdits` hardcoded `entry.ID == "image-qwen21"` with three count rules, and
`handleImageGenerations` matched `strings.HasPrefix(entry.ID, "image-moss-")` to decide whether to
inject layout JSON. Adding a workflow meant editing Go, which is the opposite of the reason
`registry.json` exists.

**Where the rules went**

| Was                                     | Is                                                            |
| --------------------------------------- | ------------------------------------------------------------- |
| `switch entry.ID` for media counts      | `accepts.images/videos/audios {min,max}` + `accepts.minTotal` |
| `switch entry.ID` for the error hint    | `accepts.note`, appended to every refusal                     |
| frame-mode defaults and per-mode counts | `frameMode {default, images{first,last,both}}`                |
| fixed execution mode per H3 workflow    | `mode` (`text`, `frames`, `reference`)                        |
| Qwen's per-mode image counts            | `variants.<mode>.accepts`                                     |
| Qwen's reference reordering             | `variants.edit.promptReorder: "qwen-edit"`                    |
| MOSS layout prompt injection            | `promptInject: "moss-layout"`                                 |
| probing the source video length         | `sourceVideoFrames: true`                                     |

**Three behaviour changes, all deliberate**

1. Error codes are uniform: a too-many-media refusal is `unsupported_media` (was
   `unsupported_video_media` for video, and `invalid_inpaint_media` / `invalid_outpaint_media` /
   `too_many_reference_images` for Qwen). The canvas does not map these codes; the message, which
   now names the workflow's own hint, is what it shows.
2. Multi-reference rejects more than six images instead of silently truncating to six. The canvas
   already caps at six, so only a hand-written request can reach it — and a silent truncation is
   exactly the class of bug this refactor is meant to remove.
3. A first/last-frame request with `frameMode: both` and one image now fails on the image minimum
   before any upload, with a message that says two are needed.

**Tested** `internal/workflow/media_test.go` exercises the rule engine directly and then asserts the
same answers against the _shipped_ `registry.json` — the old hardcoded behaviour, expressed as data.
`internal/server/video_media_test.go` proves the route calls the engine (a frames workflow refuses
no image, refuses audio, and counts two images for `both`) without a workflow id in the handler.
Verified against the running gateway for all six H3 workflows and all four Qwen modes; a real
image generation still passes end to end.

**Where** `internal/workflow/media.go`, `internal/workflow/entry.go`, `internal/server/routes_video.go`,
`internal/server/routes_generation.go`, `workflows/registry.json`.

## 55. The run protocol exists on the wire

**Decision** `POST /api/runs` + `/api/uploads` are the gateway's own shape, not a translation of
someone else's: submit returns `202` with a run id, and status, progress, outputs and cancel all
hang off that id. Media is uploaded first (`POST /api/uploads`) and referenced by role. One row per
artifact, so a batch image request returns several outputs from one run. `docs/protocol.md` is the
contract; this entry records what landed and what deliberately did not.

**Why now, and not "just fix audio"** The audio endpoint needed the queue, and the queue needed one
submission path. Building that path twice — once for `/v1/audio/speech`, once for the real protocol —
would have been the only way to get audio working sooner, and the second version would then have to
be kept in step with the first. The cheaper order was: one execution core, then audio as an adapter
on it, then the protocol itself.

**What landed**

| Piece                                                                                              | Where                                  |
| -------------------------------------------------------------------------------------------------- | -------------------------------------- |
| Queue, persistence, progress, cancel, one `submitRun`                                              | `internal/server/runs.go`              |
| `POST/GET /api/uploads`, `POST /api/runs`, `GET /api/runs[/{id}[/outputs/{i}]]`, preview, `DELETE` | `internal/server/routes_runs.go`       |
| Upload store with retention pruning and a cached ComfyUI reference                                 | `internal/upload`                      |
| Multi-artifact runs (`outputs[]`), per-output paths, `started_at`                                  | `internal/task`                        |
| Audio as an adapter over the same path                                                             | `internal/server/routes_generation.go` |

**Two deliberate limits**

1. **No `wait`.** The audio adapter blocks because the canvas asks it to; the protocol itself never
   does. A `wait` parameter would give every client two success shapes (`200` with an artifact,
   `202` with a run) and reintroduce "the connection died, the work is lost".
2. **Media roles are the conventional names** (`source`, `mask`, `first_frame`, `last_frame`,
   `reference`, `image`, `video`, `audio`) rather than a per-workflow declaration. The role list is
   one table in `routes_runs.go`; making it data belongs with typed `inputs`, and inventing it
   before the canvas uses it would be dead weight.

**Tested** `internal/server/runs_e2e_test.go`: a two-output image run (both artifacts fetched), an
upload used as a reference image (the injected `LoadImage` node carries the ComfyUI name), and the
refusals — unknown role, wrong kind, missing upload, unknown workflow. `internal/upload` covers kind
inference, reload, size limit and retention pruning. `internal/task` covers multi-output storage and
`OutputPath` bounds. Verified live against the real ComfyUI: an audio run through `/api/runs`, a
two-image batch, and a cancel that emptied the ComfyUI queue.

**Where** `internal/server/routes_runs.go`, `internal/server/runs.go`, `internal/upload`,
`internal/task`, `docs/protocol.md`.

## 56. A run's inputs are typed and checked at submission

**Decision** `inputs` is a closed, typed surface. `GET /api/workflows/{id}/schema` publishes it, and
`POST /api/runs` validates against it before anything is queued. Unknown keys are rejected with the
accepted list; types, ranges and `required` are enforced; `quality` is an enumerated value because
the gateway owns the preset names; other `select` options stay advisory because they are ComfyUI's
vocabulary, not ours. The list is assembled from the request-level keys plus the workflow's declared
`parameters` plus its non-media binding keys, so no workflow restates it.

**What was wrong without it** `inputs` was a bag. A typo (`setps`) was silently ignored and the run
quietly used the default; a wrong value (a string where a number belonged, a size that is not a size,
`count: 0`) travelled into the graph and failed inside ComfyUI — for a queued run that meant the
caller got a failed run minutes later instead of a `400` immediately. `count` was also clamped
silently by `BuildContext`, so a request for zero images was answered with one.

**Why these boundaries**

- _Unknown keys rejected_: silently ignoring input is never what the caller meant.
- _Media binding names rejected, with `use media.<role>`_: the two input channels must not blur;
  the error names the right one.
- _Ranges enforced, sampler lists advisory_: ranges are our own declarations (and were corrected
  while doing this — seeds now allow 0, audio steps allow 1). Sampler/scheduler lists are a curated
  UI subset of ComfyUI's; enforcing them would reject valid values the canvas never offered.
- _`/v1/_` stays lenient\* until the canvas migrates: an adapter that suddenly rejects values an
  existing client sends is a behaviour change disguised as a refactor.

**A race found on the way** Running the suite with `-race` exposed a pre-existing bug: `persistNow`
marshalled the live task records after releasing the lock, racing the worker's progress updates.
It now snapshots under the lock. `make test-race` is clean over repeated runs.

**Tested** `internal/workflow/inputs_test.go`: the declared surface is accepted, ten rejection cases
carry the right code and message, media keys point at `media.<role>`, the schema marks `prompt`
required and `count` bounded, and every shipped workflow accepts a minimal request and rejects a
typo. `internal/server/runs_e2e_test.go` drives the same through HTTP, plus the schema endpoint.
Verified live against the real registry: unknown key, `count: 0`, `size: "huge"`, `steps: 999`,
`refs` as an input and `quality: "ultra"` all answer `400` with a message that says what to change,
and a valid audio run still completes.

**Where** `internal/workflow/inputs.go`, `internal/workflow/render.go`,
`internal/server/routes_runs.go`, `internal/task/task.go`, `workflows/registry.json`.

## 57. The protocol exchanges workflow runs, not graphs

**Decision** The gateway's unit of exchange is a _run of a registered workflow_: `{workflow, inputs,
media}` in, a run id out. It does not accept a ComfyUI graph from a client, and it does not hand out
node ids. The graph is server-side data (`workflows/*.json` + `bindings`). The job skeleton — submit,
id, poll, fetch, cancel — is ComfyUI's, because that is what the executor actually offers; the
abstraction above it is ours.

**Why** A graph API makes the client responsible for wiring it can only learn by reading the
workflow: which node takes the prompt, which socket takes a mask, what `"37:13"` means today. Every
client then re-implements that knowledge, and the gateway cannot validate anything until ComfyUI
fails minutes later. Naming a workflow moves that knowledge to the one place that already has it —
`registry.json` — and makes submission-time validation possible (`inputs`, `accepts`). It is also
what the canvas needs: a node stores a workflow id, not a graph.

**What follows from it**

- Media is referenced by _role_, not by filename-in-a-socket, because the binding decides the socket.
- Artifacts and run state belong to the gateway (`data/outputs`, `data/tasks.json`), not to ComfyUI's
  in-memory history, so a result outlives a ComfyUI restart.
- Progress is exposed as polling even though ComfyUI pushes it over a WebSocket: the socket stays an
  implementation detail, and a browser that reloads loses nothing.
- Raw graph submission stays refused (`ui_workflow_not_supported`, `invalid_api_workflow`). The
  exposed surface is the capability set, not the executor.

**The comparison to keep** `docs/gateway.md` → "Relationship to ComfyUI's HTTP API": endpoint
mapping, the eight differences, and the same request written both ways. Read it before changing the
protocol; it is the shortest description of what we are and are not.

**Where** `docs/gateway.md`, `docs/protocol.md`, `internal/comfy/client.go` (the endpoints we borrow),
`internal/workflow/bindings.go` (the translation), `internal/server/routes_runs.go`.

## 58. The canvas's first node lives on the run protocol

**Decision** Audio nodes submit `POST /api/runs` and keep the returned id on the node
(`metadata.audioRunId`). A reload reattaches to that run instead of re-submitting, the step ring is
driven by the run's own `progress`, and stopping a generation deletes the run (which interrupts
ComfyUI, so the GPU is actually freed). Channels that are not the local gateway keep the
`/v1/audio/speech` contract.

**Why audio first** It is the newest node in the canvas and the least entangled: no batch, no
reference images, one artifact. It is also the node that most needed what the protocol provides — a
song takes minutes, so "the browser stayed open" must not be a requirement. Doing it first proves
the protocol against a real node before the image and video paths (uploads, multiple outputs) are
touched.

**How the node decides which contract to use** Two conditions, both cheap and both already known:
the channel's base URL is the local gateway, and the model id starts with `audio-`. The gateway's
naming rule is `<type>-<slug>`, so an audio workflow is recognisable without another request; every
other channel keeps the speech call. When image and video migrate, the same rule does the work.

**What it bought, concretely**

- A closed tab or a refresh no longer loses a finished or still-running song: `hasResumableAudioRun`
  keeps the node `loading` through the restore pass, and one effect resumes the poll.
- Stop is real: aborting the wait calls `DELETE /api/runs/{id}` (`cancelOnAbort`), where the old
  blocking call only dropped the connection and left ComfyUI rendering.
- Errors are the gateway's: a refused container or a ComfyUI validation failure arrives as a run
  error with its code and message, not as a generic "audio generation failed".
- The step ring works without a preview stream, which audio never had.

**A bug it exposed** `format: "wav"` used to reach ComfyUI and fail there; the run path now validates
the declared container at submission — fixed one commit earlier, because the canvas now sends the
container the user chose rather than one the adapter guessed.

**Tested** `tests/api-runs.test.ts` (root resolution, submit body, polling to terminal, failed runs,
abort-cancels-only-when-asked, relative output URLs) and `tests/canvas-resumable-runs.test.ts`
(which nodes survive a reload). End to end against a real ComfyUI with the exact payload the canvas
sends: a run reached `succeeded` 30/30 and the downloaded mp3 played; `opus` came back as Ogg/Opus.

**Where** `infinite-canvas/web/src/services/api/runs.ts`, `services/api/audio.ts`,
`pages/canvas/hooks/use-generation-runs.ts`, `pages/canvas/project.tsx`,
`lib/canvas/canvas-generation-helpers.ts`.

## 59. A video run carries roles, frames and its own post-step

**Decision** Video nodes submit `POST /api/runs` like every other node, and the request is the
workflow's own contract: `mode` picks the media mix, the frames arrive as `first_frame` /
`last_frame`, multi-reference arrives as `reference` + `video` + `audio`, and `frameMode` says which
frames the request supplies. `interpolation` is a run input the gateway executes: the frame-rate pass
runs on the produced video inside the same run. The task id the node stores is a run id, and the
in-flight preview frame is part of the run payload.

**Why the old shape had to go** `/v1/videos` was the most branch-heavy endpoint in the gateway: one
multipart form whose field names (`image[]`, `images`, `first_frame`, `video[]`, `audio[]`) had to be
resolved against the workflow, a `params` JSON string carrying `frameMode` and `interpolation`, and
task polling split across `/{id}`, `/{id}/content`, `/{id}/cancel` and `/api/previews/{promptId}`. A
video took minutes, so the interesting case — the page reloaded mid-render — was the one the shape
handled worst: the id lived on a task nobody could reattach to, and the preview came from an endpoint
keyed by a ComfyUI prompt id the client never saw.

**Why `interpolation` is an input and not a second run** The alternatives were a client-side chain
(submit, download, re-upload, submit again) or a hidden second run inside the gateway. Both leak the
same thing: two ids, two failure modes and an intermediate artifact nobody asked for. The canvas
wants "a 60fps version of this clip", which is one request and one artifact, so the post-step lives
inside the run — the same code `/v1/videos` uses, which is why the two routes cannot drift. It is also
declared: `interpolation` accepts `off|2x|3x` and nothing else, because unlike a sampler name this
set belongs to the gateway.

**Why `frameMode` is an input when the roles could imply it** A single uploaded `last_frame` cannot
be told apart from a single `first_frame` by counting: the default rule (`byCount`) would call it a
first frame and quietly wire the wrong socket. The workflow already declares which frame modes it
accepts and how many images each needs (`frameMode.images`), so the caller states the mode and the
gateway judges it — one owner for the rule, one message when it is violated.

**What the canvas sends now** `prompt`, `mode`, `seconds`, the pixel `size` the settings imply
(`resolution_name` and `ratio` are derived into it, not sent), `frameMode`, `interpolation`, and the
node's own workflow parameters (`seed`, `steps`, `cfg`, `transition_step`, …). Nothing is renamed on
the way in, so a rejected request names the offending key.

**Tested** `internal/server/video_runs_e2e_test.go`: a last-frame request wires `last_frame` and not
`first_frame`; `both` with one image is `missing_reference_image`; an unknown frame mode is
`invalid_frame_mode`; `interpolation: "4x"` is rejected with the accepted set; a `2x` run submits the
generation _and_ the RIFE graph and still stores one artifact; `frameMode` on an image workflow is
`unknown input`. `internal/workflow/inputs_test.go` covers the video-only input surface and
`InterpolationFactor`. `tests/api-video-runs.test.ts` pins the canvas side: one run per generation,
media by role (frames, references, no media for text-to-video), `interpolation` on the same request,
step and preview reporting, failure named as a video task failure, and cancel only on an explicit
user stop.

**Where** `internal/server/postprocess.go`, `internal/server/routes_runs.go`,
`internal/server/routes_video.go`, `internal/workflow/inputs.go`,
`infinite-canvas/web/src/services/api/video.ts`, `services/api/runs.ts`, `pages/canvas/project.tsx`,
`docs/protocol.md`.

## 60. An interrupted image batch reattaches to its run

**Decision** An image node keeps the id of the batch run that is producing it (`metadata.imageRunId`,
the same shape audio uses). A reload finds the node `loading` with its placeholder slots still in
place, and one poll fills them from the run instead of asking the gateway for the images a second
time.

**Why it needed more than the audio pattern** Audio has one artifact and one field to write; an image
batch has N placeholders, N uploads to canvas storage and a node that resizes to its first image. So
the fill step moved out of the live request into `fillImageSlot`, and both paths call it: the live
batch keeps its behaviour, and the resume path is the same code reading the same artifacts in the
same order. That is the property worth having — a reloaded batch must be indistinguishable from one
that never stopped, or the two paths will drift.

**What stays unwritten** A single-image retry (the "regenerate this slot" action) does not record a
run id, because the node it repairs usually already has content and would not qualify as resumable
anyway. Retrying is a deliberate, short action; the batch is the one that can quietly outlive a page.

**Tested** `tests/canvas-resumable-runs.test.ts`: a `loading` image node with `imageRunId` survives
the restore pass (and one with content, or of another type, does not). The fill-and-resume path was
exercised against the real gateway during the image migration.

**Where** `infinite-canvas/web/src/pages/canvas/hooks/use-generation-runs.ts`,
`lib/canvas/canvas-generation-helpers.ts`, `pages/canvas/project.tsx`, `types/canvas.ts`.

## 61. The last generation adapters, and why one of them is not a run

**Decision** Video upscale and the multi-angle re-render submit runs. Video upscale is the registered
workflow `video-seedvr2-upscale` with the clip in the `video` role; the angle tool is the hidden
workflow `image-qwen21-angle` with its three camera inputs and the source in the `reference` role. One
adapter is left: `/v1/videos/interpolate`.

**Why upscale was free** It was never an endpoint-shaped operation — it already had a graph, bindings
and declared parameters. The adapter was a form that renamed `batchSize` to `batch_size` and posted
the file as `video`. Deleting it removed that translation and nothing else. Two dialog values
(`scale`, `fps`) are not sent any more because the graph derives them itself (the target short edge
and the source frame rate); sending them as inputs would be a `400`, which is the validator doing its
job on a field that never reached the graph.

**Why the angle tool fits the same shape** `image-qwen21-angle` is a hidden workflow like the others:
a prompt, three angle inputs and one reference image. `wideAngle` is a UI toggle with no counterpart
in the graph, so it is not sent — inventing a binding that reaches nothing would be worse than
leaving the knob out, and the gateway rejects inputs a workflow does not declare, which is how that
stayed visible.

**Why interpolate is different** RIFE is not a workflow the registry knows: it is `_internal/video-rife.json`
applied by the engine as a post-step. "Interpolate this clip" therefore has no workflow to name, and
the honest options are (a) register RIFE as a workflow and delete the engine's interpolator, or
(b) drop the tool. Option (a) means the post-step of a generation run and the standalone tool share
one graph but not one mechanism, so the choice is deferred to the deletion phase instead of being made
in a hurry — the adapter stays until then, and `docs/protocol.md` says so.

**Tested** `tests/api-video-runs.test.ts`: an upscale request is one run whose media is the uploaded
clip and whose inputs are only the declared ones (`scale`/`fps` are absent). Against the real gateway
the request was accepted, the graph was built with the clip wired into `LoadVideo`, and ComfyUI's own
`missing_node_type` error came back on the run — the same failure the adapter produced on that
machine, since the SeedVR2 custom node is not installed there.

**Where** `infinite-canvas/web/src/services/api/video.ts`, `services/api/image.ts`,
`pages/canvas/hooks/use-video-editors.ts`, `docs/protocol.md`, `docs/gateway.md`.

## 62. Frame interpolation is a workflow, not a run input

**Decision** `video-rife` is a registered workflow: one clip in the `video` role, `multiplier` (2 or 3) as an input, its output frame rate derived by a binding (`multiply`, 24 × multiplier). The
`interpolation` run input, its post-step (`internal/server/postprocess.go`), the hand-written
interpolator (`internal/generate/interpolate.go`, 163 lines of its own upload/submit/poll/cancel),
the private `workflows/_internal/` directory and the `/v1/videos/interpolate` adapter are all gone.
Choosing "2×" is now two runs: generate, then interpolate over the result. The canvas chains them.

**This reverses part of #59** That entry kept interpolation inside the run so that "one request is one
artifact" stayed true. The reasoning was half right: the produced bytes are already in the gateway's
hand, so interpolating before storage avoids a download-and-re-upload. Everything else it bought was
worse than it looked:

- It needed **three exceptions** to the rule "a run is one workflow": a private graph directory the
  scanner must ignore, a second executor, and an input that changes what a run means.
- It made **failure lossy**. The artifact is only stored when the run ends, so a missing RIFE node or
  a cancel during interpolation threw away the clip the user had already paid GPU time for. That is a
  correctness bug, not a simplification.
- It made the **missing dependency invisible**: no scan, no resource entry, no way for the canvas to
  warn. (Same root cause as the `hidden` bug reported in the same session.)

The intermediate 24fps clip is not "an artifact nobody asked for" — it is the thing the GPU time
bought. Keeping it is worth two ids.

**What stays** A run is still **one workflow, one job**, so `status`, `progress`, `error` and
`outputs` keep describing exactly one thing. What is dropped is the _user action = one run_
equation: the canvas may chain runs to achieve one effect, and it already had that pattern (its
"interpolate" button was always download → upload → second task → child node).

**Two small engine additions it needed**

- `multiply` in `applyTransform`, with a `factor` on the binding: a derived value written into
  another node, the same shape as `fps` on `secondsToFrames`. Generic numeric coercion, not a special
  case for one workflow.
- `RIFE VFI.ckpt_name` in `ModelLoaderSpecs`. That list is the only thing that makes a model file's
  absence visible; omitting the entry would have kept `rife49.pth` invisible, which is the trap its
  own comment warns about.

**Tested** `internal/workflow/bindings_test.go` (`multiply`: 2 → 48, 3 → 72),
`internal/server/video_runs_e2e_test.go` (a `video-rife` run wires the clip into `LoadVideo`, writes
`multiplier` 3 and `frame_rate` 72 into the RIFE/VHS nodes, and stores one output), plus the shipped
registry still validating. `tests/api-video-runs.test.ts` pins the canvas side: an interpolate request
is one run with `{multiplier}` and `media.video`, and a generation run no longer carries an
`interpolation` input.

**Still open** RIFE's node pack and `rife49.pth` are not declared in `resources/index.json`, so a
clean machine can now _report_ the missing node (it is a registered workflow) but cannot install it
from the panel. Declaring them is the follow-up, and it needs the real folder name the node pack
reports.

**Where** `workflows/video-rife.json`, `workflows/registry.json`, `internal/workflow/bindings.go`,
`internal/workflow/loaders.go`, `internal/server/routes_video.go`, `internal/server/routes_runs.go`,
`infinite-canvas/web/src/services/api/video.ts`.

## 63. An input is a key that reaches something

**Decision** A binding key becomes an accepted input only when a caller can actually set it: either
the binding is addressed by its own name (`from: "params.<key>"`) or it has no `from` at all. A
binding that derives its value elsewhere (`size.width`, `seconds`, `quality.steps`, `params.steps`
under a different name) is no longer advertised.

**Why** Those keys were accepted and then ignored, because the binding reads its own source. That is
the same failure as a typo that does not error — the shape #56 set out to remove — except it looked
like a feature: the schema listed `width` on a text-to-video workflow, `steps` on z-image (which only
takes it through a quality preset), and `frame_rate` on RIFE (derived from the multiplier). Sending
any of them did nothing, silently.

**What it caught** The schema now shows what a workflow really takes: H3 loses `width`/`height`/
`length`/`task_type`, z-image loses `steps`/`cfg`/`width`/`height`/`batch`, audio loses
`formatQuality`/`stepsOverride` (both were the _binding_ name of a value written under another key),
RIFE keeps only `multiplier`.

**What still wins** A workflow's declared `parameters` are authoritative and are registered before
bindings, so a declared key stays accepted even when a same-named binding reads elsewhere. That
combination is a registry mistake, not a supported case; it is why `steps` (declared for audio) still
works while feeding the `stepsOverride` binding.

**Tested** `internal/workflow/inputs_test.go`: `width` (from `size.width`) and `batch` (from `count`)
are rejected as unknown inputs, while the self-named `seed` is still accepted. Every shipped
workflow's schema was re-read through the live gateway.

**Where** `internal/workflow/inputs.go`, `docs/protocol.md`.

## 64. The model list belongs to the client

**Decision** `registry.json` has no `hidden` flag any more, and neither do `/api/workflows` rows or
the availability scan. The gateway registers workflows and reports two facts about each: what it
accepts, and whether it can run here. Which of them appear in a model dropdown is decided by the
canvas (`lib/canvas/tool-workflows.ts`), because the canvas is the only client.

**What `hidden` was doing** Four unrelated jobs at once: keeping a workflow out of the model list,
excluding it from the availability totals and the "what is missing" list, refusing the user's own
visibility toggle, and overlapping with `category: "upscale"`. Only the first is a real need, and it
is not a fact about the workflow — it is a fact about our UI. The result was the bug this session
started from: the panel read "14/14 available" while two upscalers were missing three node classes.

**What replaced it** The canvas already knew which workflows it invokes from a button; those ids are
now one exported list, used by the buttons, the request builders and the model-list filter. An id
cannot drift between "the button submits this" and "this appears in the dropdown" because it is the
same constant.

**Also deleted with it: three dead workflows.** `video-esrgan_2x` and `image-seedvr2-upscale` had no
caller left (no button, no model, nothing in the canvas referenced them), and
`image-krea2-aesthetics` was retired — its only trace was a migration shim that remapped a saved
selection to `image-krea2-turbo`. The shim is gone too: the generic "pick the first available model"
fallback covers it. The `upscale` pack lost those two workflows and the 7B SeedVR2 DiT resource only
they used, so the panel no longer offers an 8.5GB download for a workflow nobody can invoke.

**Tested** `internal/server/setup_report_test.go`: a workflow whose node class is missing appears in
the setup status totals and in `missing.nodes` (models and tools alike), and a workflow row carries no
`hidden` field. `tests/tool-workflows.test.ts` pins the client-side list. The live gateway's
`/api/setup/status` and `/api/workflows` were re-read after the change.

**Where** `internal/workflow/entry.go`, `internal/workflow/render.go`, `internal/setup/scan.go`,
`internal/server/routes_admin.go`, `internal/resource/status.go`, `workflows/registry.json`,
`resources/index.json`, `infinite-canvas/web/src/lib/canvas/tool-workflows.ts`.

## 65. The image upscaler is a run, and a tool workflow takes exactly one item

**Decision** `POST /v1/images/upscale` is deleted. The canvas's 放大 button uploads the source image
and submits a run of `image-esrgan-upscale` (`inputs: {upscale_model, resolution}`, `media: {image:
[id]}`) like every other image action. `GET /api/upscale/workflows` is deleted too: `/api/workflows`
now carries each workflow's loader-enriched `parameters`, so a client reads the whole catalogue in one
request and decides for itself which entries are upscalers. All four tool workflows declare
`accepts` with `min: 1, max: 1` for the media kind they consume.

**Why the route had to go** It was the last image route that existed only for one button. It resolved
a workflow by id, refused anything outside `category: "upscale"`, and parsed a multipart body — three
things `POST /api/runs` already does, with an id that a reload can resume. The category check was the
only thing it added, and that is a UI concern: "is this a button or a model" is a question about the
canvas, not about the workflow.

**Why `/api/upscale/workflows` went with it** It was a Go-side filter (`category == "upscale" &&
type == "image"`) that re-rendered four fields of a workflow row plus its parameters. The parameters
were the only thing `/api/workflows` lacked, and enriching them there costs one map copy per row, so
the second endpoint became a worse copy of the first. The filter moved into the dialog, next to the
button that needs it.

**Why `accepts` matters here** Without it, an upscale with no image is accepted (202) and fails a
minute later inside ComfyUI when the required binding finds nothing — the failure mode #56 exists to
prevent. `min: 1` makes the same mistake a 400 at submission with the workflow's own note. `max: 1`
says what the graph can honour: ESRGAN upscales one image, RIFE one clip, SeedVR2 one clip, the angle
re-render one source.

**Also cleaned** `registry.json` still carried `hidden: true` on those four entries two commits after
the flag was deleted from the struct. Ignored fields that read like behaviour are worse than no
fields.

**Tested** `internal/server/runs_e2e_test.go:TestRunImageUpscaleTakesOneImage` (image reaches the
graph, `upscale_model` reaches its node, no image is a 400),
`tests/api-image-upscale.test.ts` (one upload + one run, nothing on `/v1`, a failed run surfaces its
message). In the browser: the 放大 dialog listed the three ESRGAN models from `/api/workflows`,
submitted a run, and produced a 4096×4096 PNG node.

**Where** `internal/server/routes_generation.go`, `internal/server/routes_admin.go`,
`workflows/registry.json`, `infinite-canvas/web/src/services/api/workflows.ts`,
`infinite-canvas/web/src/services/api/image.ts`,
`infinite-canvas/web/src/components/canvas/canvas-node-super-resolve-dialog.tsx`.

## 66. Everything under `workflows/` is English

**Decision** The graphs, `registry.json` and their defaults contain no Chinese. Chinese for a shipped
workflow lives in `i18n/locales/zh-CN.ts` under `gateway.models.<id>.label`, keyed by the id the
gateway sends (#8 already said this; the registry just had not caught up).

**What was mixed** Three unrelated kinds of Chinese in the directory:

- _Copy we author_: RIFE's `label`, its `multiplier` label and description, and the angle workflow's
  default prompt. This is the actual violation — data we ship, in one language, shown to both UIs.
  `video-rife` gained a `gateway.models` entry so the Chinese UI still says "RIFE 帧插值", and the
  angle default is now English because the canvas overrides it with `canvas.generation.anglePrompt`
  anyway: the default only reaches a caller that sends no prompt at all.
- _ComfyUI node titles_ (`_meta.title`): cosmetic labels the author typed, never read by the gateway
  and never shown by the canvas — the only titles we write are our own (`Gateway Ref 1`, `Gateway
Load 2`). Translated anyway, because a reader opening a shipped workflow should not have to guess
  what "① 单采 SelfLift（日常版参数）" means, and a half-translated file is worse than either.
- _A real path_: `image-qwen21-outpaint.json` names its LoRA `Qwen-功能2.1/Qwen21-图像扩展…safetensors`,
  and `resources/index.json` installs it at exactly that path. **Left alone** — that string is a
  filename on disk, not copy.

**Cost** A workflow re-imported from an author's file comes back with its Chinese titles; the same
pass has to be applied again. The registry is the part that matters, and it is machine-written.

**Also found** `resources/index.json` was Chinese-only: pack labels and descriptions render verbatim
in the resource panel, with no key indirection. That is #67; it could not be fixed by editing JSON
alone.

**Where** `workflows/*.json`, `workflows/registry.json`, `infinite-canvas/web/src/i18n/locales/zh-CN.ts`.

## 67. The resource catalogue is English; its Chinese lives beside the app

**Decision** `resources/index.json` carries no Chinese copy — pack `label`/`description`, resource
`label`/`note`/`license` are English. The Chinese sits in
`infinite-canvas/web/src/lib/resource-copy-zh.ts`, keyed by pack/resource id, and
`ResourceCenterPanel` looks it up before falling back to the index text. Vendored third-party content
under `resources/payload/**` is untouched: it is the upstream author's text, not ours.

**Why the index had to change** The panel renders those fields verbatim, so the English UI was showing
Chinese — the one place the "English data, Chinese in the dictionary" rule (#8) was still broken in a
way a reader could see. Translating it also makes the index readable to anyone auditing what gets
downloaded.

**Why a map and not the i18n dictionary** Resource ids contain dots: `qwen3.5_9b_int8_convrot`. With
`keySeparator: "."` such a key is read as a nested path and never resolves, so the natural home for
"Chinese keyed by id" (`gateway.models.<id>`, which works because model ids have no dots) does not fit.
A typed `Record<string, …>` next to it is the same idea with the id treated as an opaque string. The
`resolvedLanguage` check is copied from `gateway-labels.ts`, so both tables switch language the same
way.

**What is still Chinese, on purpose** Two `target` paths name files the upstream authors published in
Chinese (`loras/Qwen-功能2.1/…`, `loras/Moss_image/KOOK-MOSS-SFT-观澜.safetensors`). They are disk
paths, not copy; translating them would break the install. A test fails the build if any _other_ field
grows Chinese again.

**Tested** `tests/resource-copy-zh.test.ts`: Chinese for pack/resource/license in `zh-CN`, nothing at
all in `en-US` (so the index text stands), nothing for an unknown id, a dotted id resolving, and the
index itself containing Chinese only in `target`. In the browser: the Add-workflows panel reads in
English with the language set to English and in Chinese with it set to 中文, over the same rows,
including the LoRA whose path stays Chinese.

**Where** `resources/index.json`, `infinite-canvas/web/src/lib/resource-copy-zh.ts`,
`infinite-canvas/web/src/components/settings/resource-center-panel.tsx`.

## 68. There is no `/v1/models`; a workflow reports the ids it answers to

**Decision** `GET /v1/models` is deleted. `GET /api/workflows` gained a `models` array per row —
`{id, label, baseModel, baseModelLabel, imageInputs}` — built by the same expansion `ResolveModel`
accepts. The canvas syncs from it (`hooks/use-local-gateway-sync.ts` → `lib/gateway-models.ts`) and
decides what the picker offers.

**Why the endpoint had to go** It was the last route whose only job was to present, and it presented
the wrong shape: an OpenAI envelope around a _workflow_ list, with `object: "model"`, `owned_by` and a
`metadata` bag holding what the workflow really is. Everything in it was a fact about a workflow, so it
belongs on the workflow row.

**Why the ids could not just be dropped** The tempting simplification — "a model id is just the
workflow id" — is wrong for two graphs. Z-Image and Krea2 have selectable base models, each combination
has its own spelling (`img-zit-int8`, `img-k2t-int8`), and only `ModelIDFor` knows how to build it and
only `ResolveModel` knows how to read it back. A client that invented the spelling would be guessing at
a gateway rule. So the list moved, it did not disappear: `models` is the gateway reporting the names it
will answer to.

**What stayed in the client** Three decisions, none of which is a fact about a workflow: a workflow
whose `type` the canvas cannot render is not a model; a workflow the canvas drives from a button
(`lib/canvas/tool-workflows.ts`) is not a model; a disabled (`enabled: false`) or unavailable workflow
is not offered. `lib/gateway-capability.ts` went with the endpoint — it existed to guess a capability
from a name or a metadata field, and the workflow's own `type` is the answer.

**Cost** One more request than a list of names would need, and the client has to do the flattening. In
exchange there is one endpoint describing workflows, and the picker is testable without a browser: the
mapping is a pure function (`tests/gateway-models.test.ts`, 11 cases).

**Tested** Go: `TestWorkflowsReportTheirModelIds` (a row carries `models`, `enabled`, `availability`),
`TestWorkflowsReportOneIdPerBaseModel` (two installed base models → three ids, each naming its file),
`TestWorkflowVisibilityToggles` (a hidden workflow stays in the list as `enabled: false`; filtering is
the client's). Canvas: `tests/gateway-models.test.ts`. In the browser: `/v1/models` answers 404, the
channel holds the same 17 models as before with their `workflowId` / `baseModel` / `imageInputs`, the
base-model sub-dropdown still lists `默认底模 / z_image_bf16 / int8`, and a generation run through it
returned three images.

**Where** `internal/server/routes_admin.go`, `internal/server/e2e_test.go`,
`infinite-canvas/web/src/services/api/workflows.ts`,
`infinite-canvas/web/src/hooks/use-local-gateway-sync.ts`,
`infinite-canvas/web/src/lib/gateway-models.ts`, `infinite-canvas/web/src/services/api/image.ts`
(`fetchChannelModels`).

## 69. `/v1` is gone

**Decision** The gateway has no `/v1/...` route. `POST /api/runs` is the only way to produce media;
`GET /api/workflows` describes what can run; the prompt services and the two in-flight helpers
(`/api/previews/{id}`, `/api/progress/{id}`) are everything else.

**What was deleted** Nine routes and the code that existed to translate into them:
`POST /v1/images/generations`, `POST /v1/images/edits`, `POST /v1/videos`, `POST /v1/videos/upscale`,
`GET /v1/videos/{id}`, `GET /v1/videos/{id}/content`, `POST /v1/videos/{id}/cancel`,
`POST /v1/audio/speech`, `GET /v1/tasks/{id}`. With them went the request readers nothing else used:
`requireModel`, `parseParams`, `imageFiles`, `matchesImageField`'s multipart role, `uploadFiles`,
`orderedVideoFiles`, `orderedVideoRefs`, `orderedAudioRefs`, `toUploadResults`, `mediaNames`,
`firstMediaName`, `queuedPayload`, `imageResponse`, `runImageWorkflow`, `waitRun`, `runFailure`,
`runResultBuilder` (no caller ever passed one), and three files of adapter tests.

**What replaced each of them** Nothing: every caller was already moved. The canvas's image, video,
audio, upscale, interpolate and angle actions all submit runs, and the audio "blocking call" contract
was the last thing that needed an adapter — the run id answers it.

**The bug the deletion flushed out** All three media modules decided "is this the gateway?" by the
model's _name_: `<type>_WORKFLOW` regexes (`image-`, `video-`, `audio-`). A base-model variant is
`img-zit-int8`, which matches none of them, so selecting the int8 base model would have sent the
request to `/v1/images/generations` — a route that no longer exists. The channel is the only thing that
decides which contract a request speaks, so the three predicates are now `isLocalGatewayUrl(baseUrl)`.

**What the canvas still has** The `/v1`-shaped request builders (`/images/generations`,
`/v1/videos/{id}`, `/audio/speech`) stay: they are the OpenAI contract for _other_ channels, and the
channel editor allows a base URL that is not this gateway. They are unreachable for the local gateway,
which is the point.

**Cost** The gateway can no longer serve a client that speaks OpenAI. That was the goal: it never spoke
it honestly — one route was `/v1/images/upscale`, another took a `params` JSON string, and every media
rule was a branch keyed to a workflow id.

**Tested** `make check` and `make test-race`; the two audio adapter tests became
`audio_runs_e2e_test.go` (caption + lyrics as ordinary inputs, ComfyUI's own error code on failure);
`video_fixture_test.go` kept only the shared fixture and multipart helper. In the browser: a fresh
page load syncs its models, generates an image, and lists the workflows with no request to `/v1`.

**Where** `internal/server/routes_generation.go`, `internal/server/routes_video.go` (deleted),
`internal/server/runs.go`, `internal/server/server.go`, `internal/server/helpers.go`,
`infinite-canvas/web/src/services/api/{image,video,audio}.ts`, `docs/protocol.md`, `docs/gateway.md`,
`README.md`.

## 70. Saved names are keys, not translations

**Decision** A name the app generates is stored as a translation key, and rendered in the reader's
language: `CanvasNodeData.titleKey` (+ `titleParams`) and `ModelChannel.nameKey`. `title` / `name` stay
on the record as the snapshot — what a filename, an export, or a user-typed name looks like. Editing a
title (or a channel name) clears the key: from then on the node says exactly what the user wrote.

**What it fixed** Everything the app wrote down was written in the language that happened to be active.
A canvas created in Chinese showed "图片", "音频" and "AI 超分" as node titles in the English UI, and the
channel was called "本地 ComfyUI" there too. #66 and #67 fixed the _shipped_ text; this was the other
half — generated text that outlives its session.

**Why not store only the key** Two of the three uses of a title are not UI: `${node.title}.png` names a
reference image, and the canvas export writes it into a file. Those need a string now, in whatever
language. So the key drives display and the snapshot drives data, and a node without a key keeps working
as before — a user-typed title, or one from a canvas written by an older build.

**The migration** `CANVAS_PERSIST_VERSION` 2 adds `titleKey` to a stored node whose `title` matches one
of the app's own defaults **in either language**; the same idea runs over the channel list on load (no
version bump: the config store already rebuilds channels every time it reads). The matching list lives in
`lib/canvas/node-title.ts` and has to be extended when a new default title is added; the test fails when
a stored default goes unrecognised.

**What is deliberately still a snapshot** Titles that _compose_ several translations — the multi-angle
label (`前方 · 俯视 10° · 正面视角`), the split title's source name — store the composed string. They are
descriptions of a particular moment, and rebuilding them would need the parameters, not just a key. The
`Copy` suffix and asset titles (which are prompts) are the same kind of thing.

**Tested** `tests/node-title.test.ts` (key wins, params pass through, a typed title is untouched, a dotted
id, whole-title matching, `channelName` both ways) and a case in `tests/persist-migration.test.ts` (a
stored "音频" becomes `canvas.nodeTypes.audio`, a typed title and an explicit key are untouched). In the
browser: the existing canvas, created in Chinese, shows "Audio" and "Local ComfyUI" in English, and
"音频" / "本地 ComfyUI" again after switching back — with nothing edited in between.

**Where** `infinite-canvas/web/src/lib/canvas/node-title.ts`,
`infinite-canvas/web/src/constant/canvas.ts`,
`infinite-canvas/web/src/stores/canvas/canvas-persist.ts`,
`infinite-canvas/web/src/stores/use-config-store.ts`,
`infinite-canvas/web/src/components/canvas/canvas-node.tsx`,
`infinite-canvas/web/src/pages/canvas/hooks/use-image-editors.ts`,
`infinite-canvas/web/src/pages/canvas/project.tsx`.

## 72. The gateway address has no `/v1`

**Decision** `LOCAL_GATEWAY_BASE_URL` is the gateway's **root** (`http://host:8190`), and
`start.sh`/`config.js` inject it that way. A trailing `/v1` is still accepted on input and stripped
in exactly one place (`gatewayRoot`), so an older `config.js` or a stored channel keeps working.

**Why it was still there** The base URL was written when the gateway spoke OpenAI's paths, and
deleting the routes did not touch it: the value was carried around with `/v1` on the end and each
caller stripped it again before using it — `gatewayProtocolRoot`, `local-session`, `resources`,
`canvas-system-status`, two plugin-host fetches, plus the config store's own normalization. Six strips
for a suffix nothing needed.

**What replaced them** One function, `gatewayRoot(url)` in `constant/app-mode.ts`, applied where the
address enters the app (the injected value and a persisted channel). The call sites now use the value
directly, which is what makes "there is no `/v1`" true rather than maintained by convention.

**Kept deliberately, then removed** `buildApiUrl` appended `/v1` for the prompt-API endpoint. It was
the last caller of the canvas's chat path, which had no gateway endpoint behind it; decision 73 gave
that path a real endpoint (`POST /api/prompts/chat`) and `buildApiUrl` went with the client it served.
The model server's own `/v1/chat/completions` is now reached only by the gateway, from the base URL
the settings hold.

**Cost** A hand-written channel entry that points at a path like `https://host/openai/v1` loses the
suffix. Nothing in this build talks to such an endpoint: the gateway and the prompt API are configured
separately.

**Tested** `make check`, `npm run check`, `npx vitest run` (117). In the browser, with a cleared config:
17 models sync from `http://127.0.0.1:8190`, the stored channel address has no `/v1`, and a recorded
network capture shows every gateway request on `/api/...` with no `/v1` path anywhere.

**Where** `infinite-canvas/web/src/constant/app-mode.ts`, `start.sh`,
`infinite-canvas/web/src/services/api/{runs,resources}.ts`,
`infinite-canvas/web/src/lib/local-session.ts`,
`infinite-canvas/web/src/components/canvas/canvas-system-status.tsx`,
`infinite-canvas/web/src/pages/canvas/hooks/use-plugin-host.tsx`,
`infinite-canvas/web/src/stores/use-config-store.ts`.

## 73. Text is a prompt route, not a model

**Decision** `POST /api/prompts/chat` answers one prompt with one text: `prompt` (required), `system`
and images (optional), plus the endpoint's `promptBaseUrl` / `promptApiKey` / `promptModel`. The gateway
adds no wording of its own and answers once — no streaming.

**Why the canvas needed it** The text node and the plugin API's `generateText` called OpenAI's
_Responses_ API (`POST /v1/responses`) straight at the channel address. The gateway has never served
that path — it went with the rest of `/v1` — so in this build every text generation was a 404. The
canvas was carrying a whole Responses client (SSE parsing, tool calls, `input_text` / `input_image`
conversion) with nothing on the other end.

**Why not OpenAI compatibility** Serving `/v1/responses` or `/v1/chat/completions` would have meant the
gateway re-implementing another protocol for one feature. The host that actually answers is the
configured model server; the gateway's only job there is to be the party that sends the request.
`/api/prompts/chat` is that job and nothing else: it reads the prompt, hands it to the same `chat()`
the other three prompt routes use, and returns `{text, model, elapsedMs}`.

**The one thing it decides** An empty `system` is omitted rather than sent as an empty turn: a model
that receives a blank system role may answer it instead of the prompt. `Optimize`, `OptimizeImage` and
`Caption` all pass a system message, so nothing else changed.

**What the canvas lost** The streaming consumer, the tool-call types and `buildApiUrl`. A text node
shows "generating" and then the answer, which is what a non-streaming route can report, and a plugin's
`onDelta` fires once with the whole answer. Text nodes still ask for several variants (`textCount`):
the canvas sends N requests, so the gateway's contract stays one in, one out.

**What replaced the model guard** Text used to be gated on a channel model (`isAiConfigReady`), and no
workflow has a text capability, so a text node could never start — the click opened the settings and
nothing else. The gate is now `promptEndpointReady(config)` (the endpoint's base URL, key and model),
which sends the user to the prompt-API tab, and the config node's empty "pick a model" shows the
endpoint's model instead: the only model that answers.

**Tested** `make check`, `make test-race`, `npm run check` (lint, types, i18n, exports), `npx vitest
run` (124). New: `internal/server/prompt_chat_e2e_test.go` (the configured endpoint and the one user
turn, system plus image parts, unconfigured settings, the endpoint's own error, an empty prompt),
three `Optimizer.Complete` cases, `tests/api-prompts.test.ts` (7) for the form the canvas builds. In
the browser, against a real model (`qwen3.8-27b` on `172.16.9.7:9999`, configured through the settings
UI): a text node holding "写一句关于海边灯塔的诗，不超过二十个字" produced "灯塔守夜，潮声吻着星光"; the
gateway log shows `POST /api/prompts/chat 200` and one `POST /v1/chat/completions` to the model server,
and the browser's own capture shows no `/v1` path on the gateway.

**Where** `internal/prompt/optimizer.go` (`ChatRequest`, `Complete`, `chatMessages`),
`internal/server/routes_generation.go` (`handlePromptChat`, `promptCloud`, `promptImageURLs`),
`infinite-canvas/web/src/services/api/prompts.ts` (the four prompt routes, moved out of `image.ts`),
`infinite-canvas/web/src/services/api/image.ts`,
`infinite-canvas/web/src/pages/canvas/hooks/{use-generation-runs,use-plugin-host}.tsx`,
`infinite-canvas/web/src/pages/canvas/project.tsx`,
`infinite-canvas/web/src/stores/use-config-store.ts`,
`infinite-canvas/web/src/components/canvas/{canvas-config-node-panel,canvas-node-prompt-panel}.tsx`.

## 74. The prompt endpoint is called what it is, and text has no model

**Decision** Three leftovers from the cloud era, removed together:

1. `cloud` → `prompt` everywhere it meant "the configured chat endpoint": the Go type is
   `prompt.Endpoint`, the request fields are `promptBaseUrl` / `promptApiKey` / `promptModel` /
   `promptEndpointId`, the config keys are `promptBaseUrl` / `promptApiKey` / `promptModel` /
   `promptEndpoints` / `promptEndpointId`, the settings tab is `prompt-api`, and the error codes are
   `prompt_not_configured` / `prompt_invalid_url` / `prompt_api_failed` / `prompt_api_timeout`.
2. `ImageCaptionEngine` (`"qwen35" | "florence" | "cloud"`) is gone. Only `"cloud"` was ever
   produced — the canvas hardcoded it and the gateway always answered it — so the union documented
   two engines that do not exist. `captionEngine` / `captionEngineUsed` went with it, and so did the
   `provider: "cloud"` / `engine: "cloud"` fields the gateway echoed back: one producer needs no label.
3. Text mode has no model picker. It offered "text models" (a capability no workflow has, so the list
   was always empty) plus a `caption:cloud` entry that was really a mode switch. Both are replaced by
   the prompt endpoint's model in a read-only chip, and by a separate **反推** button beside the send
   button.

**Why now** The rename was deferred while `/v1` was being deleted, and the caption union was blocked
by the same thing: with `docs/known-issues.md`'s text node still 404ing there was no working path to
name. Decision 73 created it, so the names had to follow — "cloud" described a provider that this
build does not have, and a picker with an empty list is not a choice.

**What the split means for the four routes** The endpoint travels per request, but there is exactly
one of it in the settings, so the canvas services read it instead of taking it as an argument: the
three call sites that each rebuilt `{ baseUrl, apiKey, model }` from config now pass nothing. Images
are attached only where they are read — `optimize-image` and `optimize` never sent them anywhere, and
`optimize-image` is text-only by design, so its client stopped uploading files it knew would be
ignored.

**Migration** A saved config that still says `promptCloud*` is read once by
`withLegacyPromptFields()` and written back in the current spelling only. It is four keys and a
rename; silently dropping a working endpoint would be a worse bug than the name ever was.

**Cost** A hand-written client posting `cloudBaseUrl` gets a `prompt_not_configured` 400 instead of
silently working. Nothing outside this repository calls these routes.

**Tested** `make check`, `make test-race`, `npm run check` (lint, types, i18n, exports), `npx vitest
run` (128; new `tests/config-migration.test.ts` covers the rename, "current spelling wins" and "no
prompt fields at all"). In the browser with the pre-rename config still in localStorage: the config
node and the text panel show `qwen3.8-27b` (migrated, not re-entered), the settings tab lists the
endpoint as "使用中", a text generation answered `孤灯向海，夜浪无声` via `POST /api/prompts/chat`, and the
caption route returned a prompt with no `engine` field.

**Where** `internal/prompt/optimizer.go`, `internal/server/routes_generation.go`,
`infinite-canvas/web/src/lib/config-migration.ts`,
`infinite-canvas/web/src/stores/use-config-store.ts`,
`infinite-canvas/web/src/services/api/prompts.ts`,
`infinite-canvas/web/src/components/canvas/canvas-prompt-endpoint-chip.tsx`,
`infinite-canvas/web/src/components/canvas/canvas-node-prompt-panel.tsx`,
`infinite-canvas/web/src/components/canvas/canvas-config-node-panel.tsx`,
`infinite-canvas/web/src/components/settings/app-config-modal.tsx`,
`infinite-canvas/web/src/types/canvas.ts`.

## 75. Prompts are served by AlayaCore, and one instance belongs to one person

**Status** decided, not implemented. Today the four `/api/prompts/*` recipes and their in-process
chat client are still what runs; this entry records the target so the code that replaces them does
not have to re-litigate it.

**Decision** The gateway stops calling a chat-completion endpoint and starts **spawning the agent**:
one request → one `alayacore --rawio` process → one event stream → process released. Nothing else
lives longer than that; the canvas's panels do not keep an agent alive. The prompt
surface collapses to a single route (`POST /api/prompts/chat`), and `internal/prompt` (the chat
client, the four recipes, the H3 skill assembly, the sanitisers, the speaker tags) goes away. No dual
backend and no fallback: a second path would be two configuration stories and two code paths for one
feature.

**Why this is not over-engineering here** This repository is part of the AlayaCore ecosystem, so the
binary is a prerequisite the way ComfyUI already is: nothing to install, nothing to version by hand.
What it buys: the canvas no longer has to be told a base URL, key and model (they are in the agent's
`model.conf`, which the user already has); the gateway's guessing at "which thinking parameter does
this endpoint want" is replaced by the agent's per-model `reasoning_0/1/2`; and four routes, four
response shapes and four error vocabularies become one. What it costs: process-level failures (spawn,
crash, hang, stderr-only errors), a protocol dependency with a `message_version`, and more tokens per
request (the agent's own system prompt, and one extra step if a skill has to be read). Spawning is not
itself expensive — a rawio session reaches `SM session ready` in ~30 ms on this machine, so releasing
the process after each request is cheaper than keeping one warm.

**Tenancy is the security model** The gateway is **single-tenant**: one instance serves one person and
uses **one** AlayaCore config directory — `os.UserConfigDir()/comfyui-gateway/alayacore` by default
(`~/.config/...` on Linux, the right place on macOS and Windows), overridable with
`AGENT_CONFIG_PATH`. The gateway does not create or validate it: the agent creates what it needs on
first spawn, and a fresh directory comes with AlayaCore's own Ollama default, which exists to be
_seen_ — see "The model is visible and selectable" below. There is no per-request identity, so there
is nothing to map a second user onto — and therefore no per-user credential to leak: a caller of
`:8190` spends that instance's own keys, exactly as it already spends that instance's GPU on
`POST /api/runs`.

**If the gateway ever serves more than one user**, each user must have their own AlayaCore config
directory, and that requires real identity (a login/session the gateway can resolve a request to) —
not a shared secret. `GATEWAY_TOKEN` would not be that answer: it is injected into the page, so
anyone who can open the canvas can read it. Until then, one directory, and the rule that makes it
safe: **the config path, the skill container and the tool list are gateway settings and never fields
of a request.** They are the user's to change — there is no separate operator in a one-person install
— but they are changed in the gateway's own settings, not per call: a request that could name its own
`--config-path` would turn "every user has their own directory" into "the caller picks whose directory
to read", and a request that could name its own tool list would let the caller undo the one switch
that decides whether the agent can run commands.

**Security posture, stated plainly** No token is required to enable the agent. `GATEWAY_HOST`
defaults to loopback and binding anything else already emits a startup warning; `GATEWAY_TOKEN` stays
the optional, existing knob for an exposed deployment. Built-in tools are off (`--builtin-tools=`)
unless the operator turns them on — that switch, not the agent itself, is what changes the risk class
from "uses your quota" to "reads your files and runs your commands".

**Cancel belongs to the agent** Its `CI cancel` command winds a task down on its own terms — it is
the process that knows what a half-finished step means — so the gateway does not layer its own
timeout on top, and it does not invent a reaper either. A request that ends early sends `cancel` and
stops reading; the process exits when it says it has.

**Nothing binds to a window** The process lives for one operation and no longer: spawn when the
request arrives, release on `done`/`error`/cancel. It is not tied to a panel's lifetime, so there is
no idle agent to reap, no TTL, no "the panel is still open but the process was reclaimed" state to
hide, and no per-node processes for the config nodes that are always on screen. A spawn costs ~30 ms,
which is why re-opening one for every ✨ press is the cheap end of the trade.

**One route, one stream** A prompt and a command are the same kind of thing to AlayaCore — frames on
its stdin — so they are one route (`POST /api/agent/chat`) and one stream: what the request carries is
written to the agent (a `command` + `args` for a setting change, a `prompt` and images for a question,
either or both), and what the agent says comes back. Two routes would have been two ways to say one
thing.

**The model and the level are the user's, and the gateway only shows them** Everywhere the canvas
uses the agent there is a model `<select>` and a reasoning-level control. The gateway does not track,
remember, re-send or validate either of them. It displays what the agent says — `SM model` and `SM
reasoning_level`, forwarded as they arrive — and when the user changes one, the canvas sends the
agent's own command (`model_set`, `reasoning_level_set`) and the agent's answer comes back on the same
stream. Whether a
setting survives a session is AlayaCore's business: it persists the model in `runtime.conf` and does
what it likes with the level, and the canvas shows whatever it reports next.

**The list arrives with the answer, not before it** `SM model_list` and `SM model` are part of the
startup broadcast of every spawn, ahead of the model call. The gateway forwards them as one `models`
event with **every `api_key` stripped**, and the canvas renders the select from the last such event.
There is no listing route, no probe and no gateway-side cache: an empty picker before the first
request in a page is the honest state, and one request fills it.

**The event surface is a whitelist** `delta` (Ax text), `reasoning`, `step` (SM `task`), `done`,
`error` (+ an optional one-line `tool` state). Everything else the agent emits stays on the gateway:
`SM model_list` carries every `api_key` from `model.conf` verbatim, and `mcp`/`theme`/`video_config`/
`CO` have no meaning for a canvas node. The gateway is an adapter, not a pipe.

**Borrowed, not invented** `internal/wire` (frame codec, tests) and `internal/core` (spawn, stderr
tail, kill order, binary discovery) come from the Go backend of the AlayaFace project, which is the
same adapter for a different client. `message_version` is validated at startup, because the codec now
exists in three repos and drift between them is a real failure mode.

**Still open** Where the H3 wording lives: (a) an Agent Skill loaded with `read_file` (one extra model
step, and the agent gains read access), or (b) `--system` assembled by the gateway and passed at spawn
(single step, no file access). The decision waits on a measured comparison of the two outputs.

**Also noted for later** The canvas will eventually grow a settings page for the agent's models. It
must write through the agent's command plane (`CI model_sync` with the model array, `CI model_set` to
switch) rather than parsing `model.conf` from Go: the file format (`protocol_type`, `reasoning_0/1/2`,
…) belongs to AlayaCore, and going through the agent is what keeps it there.

## 76. The prompt recipes are deleted; the agent route is the only one

**Decision** The four `/api/prompts/*` routes, `internal/prompt`'s chat client and its recipes (the H3
skill assembly, the two sanitisers, speaker tags, frame alignment), the prompt-endpoint settings in the
canvas and the model/key/base-URL fields they carried are gone. What replaced them is `#75`'s one route:
`POST /api/agent/chat`, a prompt and/or one of AlayaCore's commands in, its frames out.

**Why** They had had no caller since the canvas moved to the agent, and their whole job — writing the
wording, choosing the model, cleaning the answer — is the agent's now. Keeping them was keeping a second
backend, a second configuration story (a base URL and a key in the browser) and 900 lines that could
drift from the skill they duplicated.

**What went with them, and where it went instead** The H3 wording was already a shipped Agent Skill
(`prompts/skills/h3-prompt-writing/`), so it stays exactly where it was and the agent loads it with
`read_file` — the one tool the spawn enables. The canvas now says "rewrite this into a MiniMax H3
prompt, task type …, duration …" and nothing else. `promptOptimizeLanguage` survived: the instruction
still has to say which language to answer in.

**Logging, because the transcript is the API** Every agent operation writes a line a reader who knows
AlayaCore can follow: the request (`command`, `args`, the prompt's head, how many images), the spawn
(pid, binary, config dir, skills container, tools), **every frame in both directions** (`agent >> UX`,
`agent << SM`, … with the value truncated at 400 runes), the release (exit, stderr's last line, how long
it lived), and a closing count per event type. `api_key` values are redacted: `SM model_list` carries
every key from `model.conf`, and a transcript is worth having without them.

**Tested** `make check` (14 packages), `npx vitest run` (121), `npm run check`. Against the real agent:
a prompt streamed `models`/`reasoning`/`delta`/`done` with the frame log, a command-only request changed
the reasoning level and logged its `CI`/`CO` pair, and the browser generated text, switched models and
optimised an image prompt through the one route.

## 77. Regenerating an image answers in that node; a variant is a node you make

**Decision** `nodeRegenerateBehavior` is gone. Pressing generate on a node that already holds a result
replaces that result, always. Nothing is created beside it, and the old image is not quietly fed back in
as a reference. The "原图" leading reference (`canvas.references.source`, `leadingNode`) went with it,
and so did `Radio` in `components/kit/choice.tsx`, which had no caller left.

**Why** The setting offered two answers to one button: _overwrite_ meant "re-roll this one", _new node_
meant "keep this, and make another one conditioned on it" — it created the sibling **and** inserted the
old image into the request as a reference. So the same press meant two different things depending on a
preferences toggle, and the meaning leaked into the result: with the default, pressing generate twice
gave you two images, the second one not a re-roll but an edit of the first.

**Both real intents already have explicit routes.** A variant conditioned on an image is a new node with
that image picked in 参考内容 (`requestEdit`); a variant of a _prompt_ is a config node pressed twice,
since the config node keeps the prompt and never reuses a result node. Neither needed the toggle — the
automatic path was the manual path, performed silently on the user's behalf.

**Where a reference actually comes from** An image node's references are **connections**
(`buildNodeMentionReferences(node, nodes, connections)`); picking one in 参考内容 is that connection
being made (`project.tsx`, the picker appends `{fromNodeId, toNodeId}`). The `@` mention is a different
thing and lives elsewhere: the config node's 组装提示词 composer writes `@[node:…]` into the text, and
double-clicking a text node gives a textarea with an `@` menu. An image node's prompt box is a plain
textarea — there is no `@` there, and saying otherwise would send people looking for a menu that is not
in that panel.

**Cost, stated plainly** Pressing generate on a finished image destroys it. To keep it, 复制 the node
first, or make the new node before generating. This is the trade the decision buys: the button does one
thing, and the thing it does is visible in the node you pressed.

**Tested** `npm run check` (i18n 1049/1031, no dead keys, exports clean), `npx vitest run` (124). In the
browser against `:8191`: an image node with content and no references showed only the add button in
参考内容 (the "原图" chip is gone), pressing 生成 kept the node count at 26 and replaced that node's image
through `image-z-image-turbo` — a plain generation, no edit workflow, so no source image was sent — and
the preferences page no longer lists the option.

**The sediment, and where it went** A persisted blob keeps whatever it was written with, and the store's
merge spread it over the defaults — so every field deleted from `AiConfig` (`promptEndpoints`,
`reasoningEffort`, `nodeRegenerateBehavior`, and the `promptCloud*` / `channelMode` / `apiFormat` /
`videoInterpolation` names before them) stayed in state and was written out again on the next change.
`use-config-store.ts` now drops those names at the merge, from one list, with the reason attached.
Nothing reads them and the values need no migration: the setting they describe is gone.

## 78. A mask belongs to the node that shows it

**Decision** The mask annotation node carries the mask the workflow consumes: `metadata.mask` holds
the storage key of the file that goes out as `media.mask` — binary for the inpaint workflows, the
drawn-over picture for the ones that accept that. Its own picture stays the mask painted over the
source, because that is what a person needs to see. Inpaint runs take the mask from whichever connected
node carries one; `submit()` opens the mask editor only when no connected node does, so wiring a source
and a mask node by hand works exactly like a run the editor made.

**Why not the result node's saved references** `buildImageGenerationMetadata` already records the
references a run used, so an inpaint could re-run from `metadata.references`. That was the first attempt
and it is wrong in two ways. It is a string, not a thing the canvas shows: `collectImageStorageKeys`
only counts objects with a `storageKey`, so the binary mask was treated as garbage and deleted, and the
run then fell back to the connected picture — a blue-tinted photograph in the `media.mask` slot, which
inpaints nothing and reports nothing. And it makes the connections a second, silently overridden
answer, so re-wiring a different mask would be ignored.

**What the binding buys** The file is part of a node, so a storage cleanup keeps it; the request is
whatever is connected, so #77 still holds and a mask can be swapped by re-wiring; and pressing generate
on an input of a run repeats the run because that is what the graph says, not because a hidden string
was found.

**The cost** Mask nodes made before this have no binding, and their binary mask is gone — deleted by
the cleanup that caused the bug. Pressing generate with one connected now opens the editor instead of
sending a picture where a mask belongs: one honest re-mark, and that node is fixed for good.

**Tested** `image-qwen21` on `:8191`, three runs over the same source. The editor's run uploaded mask
`7b985050…` (31,835 B). Re-running on the build that resolved saved references sent the tint instead
(`81e7d561…`, 1.48 MB) — the bug, reproduced. With the mask bound to the node, re-running pressed
generate, opened no editor, kept the node count at 30, and uploaded `7b985050…` again, byte-identical.

## 79. A reference makes it one answer per press; only text-to-image draws

**Decision** An image run with references generates exactly one image, whatever 画布生图张数 says. Only
a run with nothing to condition on uses that setting. The settings chip stops showing a count it will
not use when the node has references.

**Why** A reference is the user being specific — this picture, this mask, this region. Asking for three
answers to a specific question turns the node into a batch that looks exactly like a text-to-image batch
while meaning something else, and the three are stacked in a node nobody asked to be a batch. Without a
reference there is no提问 to be specific about: that is the draw, and repeating it inside one run is
what the count is for. Dissatisfied with an edit? Press again — the node it overwrites is the one being
looked at (docs/decisions.md #77).

**What this fixes** The mask editor has always forced `count: "1"`, so the same inpaint made 1 image
through the editor and 3 through a hand-wired node. Now both are 1, and the rule is one sentence instead
of two paths.

**Tested** `image-qwen21` inpaint with two references on `:8191`: the run fetched only `outputs/0`, one
artifact, with the bound mask (`7b985050…`). The count could not be observed on the other side of the
rule in this environment — a Z-Image text-to-image run with 画布生图张数 = 3 also fetched only
`outputs/0`, so the workflows here return a single artifact per run regardless of `count`. The
no-reference path is the same expression it was before this change.

## 80. An agent's life belongs to the client that opened it

**Decision** A connected agent is one process per client-chosen name, held until that client disconnects.
Nothing expires. Opening it again under the same name is idempotent: the same name returns the same
process, which is how a reloaded page finds the conversation it left. The conversation route requires a
name and writes into that process without ever spending it; `POST /api/agent/once` has no name at all and
releases its process when the answer ends (see #81 for why that split is structural, and #82 for the
transport that carries it).

**Why** A conversation is the point of this process: an agent that forgot what it had done while the
user was thinking would be worse than no agent. An idle timer cannot know the difference between "the
user gave up" and "the user is reading a video that takes six minutes", and it took the history away in
both cases. So the only thing that ends an agent is the user closing it.

**The cost, stated plainly** A tab that is closed by the browser rather than the user leaves its process
behind, and a gateway that is killed takes the map with it. Both are accepted: the first is the price of
a history that survives a reload, and the second is why the canvas has to say "已断开" rather than
pretend the agent is still there. The disconnect route is the one way out, and it is the client's job to
call it.

**The key is the client's, not the gateway's** The name is whatever the client files the process under;
the canvas passes its own id. The gateway does not know what a canvas is — it files processes under
strings, which is the same dumbness it keeps everywhere else. A string that changes means a new agent,
and that is the whole of the semantics.

**One turn at a time** Two streams reading one stdout would each take half the frames and split an
answer between them, so a session serialises its turns. This replaced a `Take`/`Touch`/`idleSession`
state machine — a session was previously consumed by its one question, and "does this request spend the
connection" was a branch in the route. Both are gone; a question now spends nothing because there is
nothing to spend.

**What a turn ends on** `SM task in_progress: false`, measured against a real tool round: AlayaCore runs
built-in tools itself, so `AF` (the call, in two frames — name then arguments) and `UF` (the result) both
arrive inside one task, and the task ends once. The gateway relays all three as `tool_call` and
`tool_result` events and answers none of them. That measurement is also why there is no reader goroutine
fanning out to subscribers: the agent only speaks when spoken to, so the read loop stays in the request.

**A cancelled turn is not a dead agent** A client that goes away mid-answer sends `CI cancel` and the
frames that arrive while it winds down are read and dropped, so the next turn does not inherit them. Only
a process that ends before its turn does returns `ErrGone`, and that releases the session.

**Tested** On `:8191` with a real AlayaCore: `connect` twice with one name returned one pid; two
questions on that session both answered ("收到", then "还在") from the same process; the tool round
(prompt → `AF`/`AF`/`UF` → answer) streamed as `tool_call`/`tool_result`/`delta` with one `done`.
The e2e tests state the idempotency, the second turn and the tool relay against a fake agent.

## 81. A one-shot question has no name, so it cannot reach a canvas's agent

**Decision** The words the canvas asks for without continuing — a text node's body, a rewritten prompt, a
caption — go to `POST /api/agent/once`, which names no session and reads none. It spawns
`alayacore --rawio`, asks, and releases. The canvas's own agent is reached only through
the conversation's own route, which requires a name (#82).

**Why** One function used to do both, and it read the session out of the store, so every one-shot
question was sent to whichever agent the canvas happened to hold — and the gateway spent it when the
answer ended. That was harmless while a connection was one question by design (#80 replaced that), and
it would have become "pressing the sparkle next to a prompt empties the history of the agent you are
working with" the moment the process started living. The rewrite buttons and the canvas agent are
different kinds of thing; the route is where that difference lives rather than a convention the caller
has to remember.

**The shape of the guarantee** `/api/agent/once` never reads a `session` field, so a buggy caller cannot
hand it one either — the field is not on the route. The client mirrors it: `requestAgentOnce` passes a
literal `null` where `requestAgentChat` passes the store's session, and both go through one helper that
takes the session as an argument, so the only place that can look one up is the caller that should.

**Not a weakened agent** The one-shot keeps the tools and the skills container: it is the H3 prompt
rewrite, which works by reading a `SKILL.md`, so `--builtin-tools=read_file` stays on both routes. The
difference between the two kinds of agent is lifetime, not capability.

**Tested** `:8191`: a prompt on `/chat` spends nothing and the session answers again; a prompt on `/chat`
for a name that was never connected is `409 session_not_connected`; a prompt on `/once` handed a live
session id answers, leaves that session connected, and the session still answers afterwards. From the
browser, clicking 优化提示词 logged `POST /api/agent/once 200` with 20 deltas, and the process exited by
itself (`lived=14886ms`) — a one-shot agent does not outlive its answer.

## 82. The conversation is a socket; a shortcut stays a POST

**Decision** The agent is one WebSocket per session, at `GET /api/agent/socket/{session}`, both directions
as messages: prompts, settings commands, `cancel`, `close` up; the agent's own frames down.
`POST /api/agent/connect`, `/api/agent/chat` and `DELETE /api/agent/session/{id}` are gone. So is
`GET /api/agent/once`: a second route for a shorter lifetime was a second mechanism for the same thing,
and the lifetime is the client's to decide (see #83).

**Why this and not SSE** Three shapes of SSE were proposed and each one added a concept instead of
removing one: a stream per turn needed "when does a turn end" and "which request owns the stream"; a
stream per turn with injection needed a lock; one downstream plus N upstream POSTs needed status codes
for "queued" and a separate lifetime for the downstream. The third would have worked (it is the
WebSocket topology expressed in HTTP), but the sequence itself was the argument: a transport that needs
a new shape for every requirement is the wrong transport. The agent is a bidirectional stream; so is a
socket. See #80 for the lifetime this carries, and #81 for why the one-shot must not share it.

**No dependency** `go.mod` still has no `require`. `internal/ws` is ~250 lines: the handshake accepted
instead of asked for, masked frames read, unmasked written, fragmentation reassembled, ping answered.
`internal/comfy` already spoke the same framing as a client, and the tests drive the new server with
that client — an independent implementation rather than the server's own reflection.

**One transport** The one-shot was a POST returning `text/event-stream`, which is the natural shape for
one request and one stream — but it meant two streaming mechanisms for one feature: two client readers,
two framing formats, and a question ("why are these different?") whose only answer was "one of them is a
single answer". Moving it cost one route and the SSE machinery it had needed (`startEventStream`, the
multipart body, the file-to-data-URL helpers that existed only to fill that body — images now travel as
the data URLs the canvas already holds). #83 finished the job by deleting the route itself, because a
session's lifetime is the client's to decide, not the route's to declare.

**What it cost** `curl -N` no longer watches anything; `websocat` does. Two things had to be fixed
to make the upgrade possible at all, and both were silent bugs: `statusRecorder` in `internal/httpx`
wrapped the response writer without forwarding `Hijack`, so every upgrade failed with a 400, and without
forwarding `Flush` — which meant the SSE that `/api/agent/once` still uses had _never_ actually flushed:
every event sat in the response buffer until the handler returned. The first event of a one-shot answer
now arrives at 0.03s instead of at the end.

**Origin** A handshake is a `GET` that becomes a write channel the moment it is accepted, so `Guard`
now treats an upgrade as a write and applies the cross-site check it applies to POSTs. Without that, the
one route that speaks to a canvas's agent would have been open to any page the user had loaded.

**One watcher, one reader** The pump that reads the agent's stdout belongs to the session, not to a
socket: it starts with the process and runs until the process ends. A socket attaches as the watcher and
detaches when it goes away; the pump keeps reading and dropping. This is not decoration — a read blocked
in a syscall cannot be cancelled, so a reader that lived and died with a socket would be stuck holding
half a frame when the user reloaded the page, and the next socket would corrupt the stream.

**Tested** Against a real AlayaCore on `:8191`, in a browser: the panel opened the socket
(`socket open … spawned=true`); a prompt answered "收到" and a follow-up "刚才让你回复什么" answered from
history; `@` on a node sent `【画布引用】` with that node's id and prompt, and the agent answered "茶壶";
the model select sent `model_set` and the display became `qwen3.8-flash`; `结束对话` logged
`released … reason=closed by the client`; a page reload reattached with `spawned=false` and the agent
still remembered "新开". Dragging the panel moved it and left the canvas viewport untouched; a wheel over
it did not zoom. The Go tests cover the same ground without a model: idempotent open, one watcher per
name, second turn on one socket, tool relay, `close` releasing, a dropped socket keeping the agent, a
killed process reporting itself, and the one-shot never reaching a conversation.

## 83. One session per surface, and the model is the session's

**Decision** Every agent use is a session: one process, one socket, one name chosen by the client — the
canvas panel's is `canvas`, a node panel's is `node:<id>`. A session is created by pressing connect in
that panel, announces its own model list, is told which model it is on, and answers on it. Nothing about
a session is stored in the application: `agentSession` and the `agent` blob are gone from the config
store, and there is no per-node model field either. The canvas panel's session lives until it is closed;
a node panel's is released **the moment its answer ends** — not when the panel closes — and asking again
means pressing connect again, which is what makes one panel one turn. `GET /api/agent/once` is deleted
with the last of the nameless one-question processes.

**Why** The application had been keeping a mirror of state the agent already owns — which models exist,
which one is in effect, how hard it is thinking — in a single global slot. One slot for many processes
can only be wrong: the first node to connect made every other node think it was connected, and choosing
a model for one node changed the model everywhere. Every bug in this area was the same bug: two owners
for one fact. A session's own process is the owner, and the client only ever reads back what it says.

**Why this is the simple shape, not the big one** The processes are small and there can be as many as
there are panels open. Refusing to start one per panel is what produced the shared slot in the first
place; the alternative — one agent shared by every node — is not cheaper, it is a different and wrong
product. A node holds no process between answers — the release is part of the answer, so nothing at all is
running for a node nobody is asking. The canvas's own agent is the one thing that outlives its socket,
because the user asked for that.

**What it fixes** Pressing connect on a second node now shows connect, because that node has asked
nothing of anyone yet; the model select in each panel shows _that session's_ model; and text generation
asks the session of the node whose button was pressed, not a new node's name.

**Tested** In a browser against a real AlayaCore: two config nodes in text mode, the first connected
(`socket open session=node:config-1793445940295-nk65u spawned=true`, model list shown, button became
send) while the second still read 未连接 / 连接; pressing send on the first logged
`socket in session=node:config-…`, the answer landed in a new text node, and the gateway logged
`released session=node:config-… reason=close` with the button back to 连接 and no `alayacore` process
left running. The client tests hold the
other half: a session's model does not leak into another's, and asking for words opens no second socket.

## 84. A session's tools are its own, and the workspace is how a call says so

**Decision** A session's agent is spawned with `--config-path` pointing at a directory of its own,
`<DATA_DIR>/agent/<sanitised-session>-<hash8>`, holding a copy of the operator's `model.conf` and
`runtime.conf` plus an `mcp.conf` that points its tool calls at `http://<GATEWAY_HOST>:<GATEWAY_PORT>/mcp/<workspace>`
(see #87 for why it is that address and not loopback).
The workspace name is derived from the session name, so `POST /mcp/{workspace}` finds the session that is
asking by scanning the sessions that exist rather than through a registry beside them. The directory is
removed with the process, and every leftover is removed at startup. The endpoint speaks `initialize`,
`tools/list` and `tools/call` (notifications are `202`) and, for now, offers one tool, `ping`. A call is
then **not answered by the gateway**: it is published down the session's socket as a `tool` event, and the
canvas — the only thing that knows what a node is — replies with `{"toolResult":…}` on the same socket,
which becomes the tool's answer to the agent. Tool names are unprefixed: AlayaCore namespaces them under
the server, so `ping` is the model's `canvas_ping` and arrives back here as `ping`.

**Why MCP at all** Anything the agent does that AlayaCore cannot do itself — make a node, wire an edge,
start a generation — has to reach the gateway, which is the process that holds the socket to the canvas.
AlayaCore's only way to learn a tool exists is `mcp.conf` in its config directory, so the gateway serves
that tool itself instead of inventing a second channel.

**Why the identity is a directory** An MCP call carries nothing but its arguments. The alternative ways
to know who is asking are both wrong: guessing the session with a turn in flight breaks the moment two
panels ask at once, and adding a session field to every tool's arguments makes the model carry something
that is not its business. A path segment written by the gateway, into a file the gateway chose, cannot be
got wrong by the client — and it is a name that says what it is.

**Why a config directory per session, rather than one shared** `model_set` is persisted by AlayaCore into
`runtime.conf`, so a shared directory would turn "the model this session runs on" into the default for
every later session — the same kind of second owner that #83 removed from the client. The copy also means
the agent writes nowhere near the operator's configuration.

**Tested** Against a real AlayaCore on `:8191`, with a WebSocket client standing in for the page: on
connect it read its own `mcp.conf` and logged `MCP servers initialized: 1 servers, 1 tools loaded`; the
agent called `canvas_ping`, the gateway published
`{"type":"tool","callId":"tool-1","name":"ping","arguments":{}}` on the session's socket, the client
answered `{"toolResult":{"callId":"tool-1","text":"pong from the page"}}`, the POST that had been holding
the agent's turn returned it, and the agent's answer was "pong from the page". Closing the conversation
removed `data/agent/probe-mcp-7ed10748` with it. The name the model sees was measured, not assumed — the
first version offered `canvas_ping` and the model was shown `canvas_canvas_ping`. `mcp_test.go` holds the
rest: the handshake and tool list, a call run by the canvas and answered with what it said, a tool the
canvas called a failure, a session nobody is watching, a name the gateway never advertised, a notification
accepted with no body, and a session's directory built for it and removed with it.

## 85. A tool runs in the page, once, for whichever agent asked

**Decision** The canvas answers the gateway's `tool` events itself: one hook mounted where the nodes are
(`useCanvasAgentTools`), subscribed to **every** session's events (`onAnyAgentEvent`) rather than to the
canvas panel's name, and replying `{"toolResult":{…}}` on the session the call arrived on. The vocabulary
starts read-only — `ping` (how much is on the canvas) and `list_nodes` (every node with the id a later tool
can act on, and every connection), the second written as one line per node.

**Why every session and not just the panel's** The canvas is one, and a node's agent asking to see it is
the same question as the panel's asking. Subscribing per name would have to be done for sessions that do
not exist yet — a node's session exists only after its connect is pressed — and would then need a rule
saying which agents have hands. One subscription for the page, answering on the name it was given, has no
such rule: whatever agent asks, the page is what runs it.

**Why read-only to begin with** Reading is the only thing that cannot be wrong in a way the user has to
undo, and it proves the whole pipe — agent decides, gateway routes, page answers, agent reads — with real
canvas state. Anything that changes the canvas comes after, through the same functions the buttons call,
so a tool can never be a second way to do something.

**Tested** In a browser against a real AlayaCore on `:8191`, on a canvas with 38 nodes and 29 connections:
opening 画布助手 connected the session, a prompt asking for `canvas_list_nodes` made the agent call it,
the gateway logged `tool answered session=canvas callId=tool-1 isError=false` in 3ms, and AlayaCore logged
`<< UF {"output":[{"type":"text","text":"38 nodes, 29 connections\n- text-1793432616807-5rz7o (text)
\"文本\" at (689, 247)\n…"}]}`; the panel's answer named both counts and the shapes it had read. A second
turn and a page reload repeated it (`callId=tool-2`). The node titles that disagree with their prompts are
the canvas's own data — a title is a snapshot taken when the node was made.

## 86. A tool makes a node exactly the way the button does

**Decision** `create_node` calls the same function the canvas's own 文本 / 图片 / 视频 buttons call, with the
same consequences: the node lands in the middle of the view (a tool that states a position gets its
top-left corner there, which is the coordinate `list_nodes` reports), it is selected, and for the types
whose button opens a panel the panel opens. A `prompt` becomes a text node's `content` and every other
node's `prompt` — the field that node's own panel edits. The answer names the new node's id and position.

**Why** The agent must look like a hand the user cannot see, not like a second, quieter way to change the
canvas: whatever appears has to appear where he is looking, selected, with the panel he would have got by
pressing the button. Anything else — building the node in a corner, skipping the selection, writing
metadata the panel would not have written — makes the canvas a different surface depending on who asked,
and the user would have to learn which one he is looking at.

**What it refuses** Groups (structure, not content) and plugin nodes (the user's own extensions): a tool
that could make anything the registry knows would be a tool that makes things nobody asked for. The
refusal names what it can make.

**Tested** In a browser against a real AlayaCore on `:8191`: "用 canvas_create_node 在画布中央新建一个文本节点，
内容是「第二只手写的字」" → `<< AF {"name":"canvas_create_node"}`, then
`{"input":{"prompt":"第二只手写的字","type":"text"}}`, `tool answered … isError=false`, and
`<< UF "created text-1793522100580-liw89 (text) at (1541, 438)"`. The node appeared in the middle of the
viewport with the text already in it, selected, with the selection toolbar over it — visible in a
screenshot taken right after — and the panel showed one step, `调用 canvas_create_node`, and the id.

## 87. The tool URL is the address the gateway actually listens on

**Decision** `mcp.conf` names the workspace at the gateway's **bind** address — `GATEWAY_HOST:GATEWAY_PORT`,
not `127.0.0.1:<port>` — with a wildcard bind (`0.0.0.0`, `::`) replaced by loopback, since a wildcard has
no address of its own to dial and loopback is what it reaches.

**Why** The agent runs on the same machine as the gateway, but "same machine" is not "loopback": a gateway
bound to `192.168.3.232:8190` does not answer on `127.0.0.1:8190` at all. Handed that URL, AlayaCore's MCP
client simply cannot connect — it reports `0 servers` in its own output, the agent has no tools, and the
only place the failure is visible is the agent's own log. This was found the way it should have been found
earlier: on a real setup whose gateway binds a LAN address, while every test of ours ran on `127.0.0.1`,
where the wrong URL happens to be right.

**Tested** `TestMCPURLIsAnAddressTheAgentCanDial` pins the four shapes (loopback, LAN, wildcard, IPv6) and
the generated file. A gateway bound to `192.168.3.232:8192` wrote
`url: http://192.168.3.232:8192/mcp/probe-lan-8a8315a4` and its agent logged
`MCP servers initialized: 1 servers, 3 tools loaded`; with the old `127.0.0.1` URL the same setup cannot
connect at all (`curl http://127.0.0.1:8192/` → connection refused).

## 88. A tool answers with content, and the gateway only carries it

**Decision** A tool's result is a **content array in the protocol's own shape** — `text`, `image` and `audio`
(each with `data` and `mimeType`), `resource` (`mimeType` + `blob`, which is how a film travels because the
protocol has no video block of its own), `resource_link`. The page produces the blocks, the session carries
them as raw JSON, and the gateway puts them in `tools/call` unchanged: it does not resize, transcode,
truncate, cap, or turn one kind of block into another. `read_node(nodeId)` is the tool that uses this: it
answers with the node's own words and media, not with a description of them.

**Why raw, and why nothing is inspected** The blocks are self-describing — a block says what it is, and
`mimeType` says what the bytes are — so every layer between the canvas and the model would be guessing if it
looked inside. AlayaCore already knows what to do with each kind (`internal/mcp/tool_adapter.go` maps them to
image/audio/video parts, `internal/llm/providers/openai.go` promotes a tool result's media into a follow-up
user message because an OpenAI `tool` message holds only a string, and `media_fit.go` shrinks what is too
large to send). A gateway that "helpfully" downscaled a picture, sent only a video's first frame or rewrote a
sound as prose would be deciding how much of the canvas the agent is allowed to see — and would break as soon
as a later protocol revision added a block type, which a raw carrier simply forwards.

**What it costs, and why that is the user's call** A 1024² picture is ~1.3 MB of base64 on the socket and in
the conversation; a film is more. That is what looking at the real thing costs, and it is charged to whoever
asked for it (the agent, once per read), not paid preventively by every call.

**Tested** In a browser against a real AlayaCore and a vision model, on a canvas holding two nodes whose
prompt was "a red ceramic teapot on a wooden table, soft window light": `canvas_list_nodes`, then two
`canvas_read_node` calls in one turn; each result was `[{"type":"text","text":"a red ceramic teapot…"},
{"type":"image","uri":"data:image/png;base64,…"}]` (1.27 and 1.30 M runes) and the agent answered that the
first pot has water droplets on the lid and body, the second does not, spout left and ring handle right, wood
grain with wear marks, diffuse window light from the left — details that are in the pixels and nowhere in the
canvas's text. `TestMCPCarriesTheBlocksThePageProduced` holds the carrier property: text, image and resource
blocks come out of `tools/call` byte-for-byte as the page sent them.

## 89. Generating is the button, not a second way to run

**Decision** `generate(nodeId)` calls the same `handleGenerateNode` the node's own panel calls, with the mode
that panel would use — a content node generates what it is, a config node generates the kind its composer is
set to — and the words the node already holds. It is not awaited: it answers at once that the button was
pressed, and the node shows its own progress and holds the result. A group has nothing to generate and is
refused.

**Why not awaited** A picture takes minutes and a film longer. The tool's job is to start the work, not to be
the progress bar for it: an agent holding a tool call open for five minutes blocks its own turn, and the place
the user is watching is the node.

**Why it is allowed at all** The agent may ask before pressing — that is a decision about the conversation —
but if the user says do it, doing it cannot be something only a human hand can reach. A canvas the agent can
build but not run is a canvas where every resulting step is still the user's.

**Tested** In a browser against a real AlayaCore: "用 canvas_create_node 建一个图片节点，prompt 是「a brass
compass on a marble table」，然后立刻用 canvas_generate 对它按下生成" →
`<< AF {"name":"canvas_create_node"}` → `created image-1793524968642-jq17f (image) at (1541, 438)` →
`<< AF {"name":"canvas_generate"}` with `{"nodeId":"image-1793524968642-jq17f"}` → `tool answered … isError=false`
with `pressed generate on image-1793524968642-jq17f (image)`, and the gateway straight after logged
`POST /api/runs 202` and the run in the queue (`image-z-image-turbo running`). The node count went 39 → 40 and
the new node sits on the canvas with its prompt.

## 90. The transcript follows the newest message only while the reader is at the bottom; `@` points at a node

**Decision** The agent panel keeps itself at the newest message only while the reader is at the bottom. A
wheel up stops the follow at once; being at the bottom again resumes it, and the position is re-read on every
message rather than kept as a flag that can drift from where the reader actually is. Typing `@` no longer
opens a list of node names: it puts the canvas into the same "click a node" state a reference's `+` uses, and
the node the user clicks becomes the citation, its title replacing the `@` in the draft.

**Why** An answer that streams while the user scrolls up is an answer they cannot read: the follow turns a
gesture into a tug of war. The list of names was a second, worse copy of the canvas — the nodes are already on
screen, with their pictures and their layout, and picking one is pointing at it. Both are the same rule as
everything else here: the surface shows what is there, and the user aims at the thing itself.

**Tested** In a browser, on a real answer long enough to overflow (1279px of transcript in a 290px box):
while it streamed the panel stayed at the bottom (`gap 0`); after a wheel up and a scroll to the top, two more
messages grew it to 1360px with `scrollTop` still 0 and `gap 1070` — the reader was not dragged down; after
scrolling back to the end, the next message put it at 1145 of 1435 with `gap 0` again. `@` in the composer
showed "点击一个节点来引用 · ESC 取消", clicking the teapot node put `@a brass compass on a marble table` in the
draft, and the prompt the gateway logged carried `【画布引用】- image-1793524968642-jq17f (image) …`.

## 91. Changing a prompt and running it again are the manual path, so the agent gets both

**Decision** `set_prompt(nodeId, prompt)` writes the prompt where the node's own prompt panel writes it: a node
that already holds a result keeps the words in `composerContent` (typing must not throw the result away), a
node that holds none keeps them in `prompt`. The question "does this node hold a result" is one function,
`holdsResult`, asked by both the panel and the tool. Generating afterwards is `generate` on that same node,
which replaces the result in place (#77) — nothing is created beside it. A variant remains what it is manually:
a new node fed through the reference bar, i.e. `create_node` plus a connection.

**Why** Before this the agent's only way to answer "draw this differently" was `create_node` + `generate`, so
every request produced a sibling — the shape #77 deliberately removed from the manual button because it made
one press mean two things. An agent that offers only the removed route is a second, worse product: the user's
hand and the agent's hand would do different things to the same canvas. Both intents are legitimate and both
have explicit routes; which one is meant is the user's sentence, not the tool's choice.

**Tested** In a browser against a real AlayaCore: "把 image-1793524968642-jq17f 这个节点的提示词改成「a rusty
copper kettle on a dark stone shelf, candle light」，然后在同一个节点上重新生成，不要新建节点" →
`canvas_set_prompt` → `canvas_generate` → `POST /api/runs 202` → the run succeeded in that node (3 images, the
count the canvas is set to), the node's prompt box showed the new words, its picture became the copper kettle
by candlelight, and the canvas still had **40 nodes** — the same 40 as before. The agent then called
`read_node` and reported "已直接在原节点上触发生成（没有新建节点）".

## 92. A connection is a drag, so the agent gets the same call

**Decision** `connect(fromNodeId, toNodeId)` adds one connection through the same function a drag adds it with
(`addConnection`, which `connectNodes` now also calls): the canvas's own `normalizeConnection` decides the
direction and whether the pair may be connected at all, and an existing connection is never added twice. The
tool starts from the source handle, because a tool states its two nodes in order; a drag states a gesture, so
it says which handle it came from. What a connection means is unchanged: it is how one node's content becomes
another's input — the picture connected into a config node is what makes the next generation a variant of it.

**Why** The variant route (#77) is a connection, and without it the agent could only ever make things from
nothing: it could not build the "keep this, make another one conditioned on it" gesture the canvas offers. Two
implementations of "may these connect" would eventually disagree, so the drag and the tool share one.

**Tested** In a browser against a real AlayaCore: "新建一个图片配置节点，把铜壶那张图
image-1793524968642-jq17f 连到它上面，然后在配置节点上生成一张以它为参考的变体" → `create_node` →
`created config-1793527710406-y2raf (config) at (1927, 438)`, `connect` →
`connected image-1793524968642-jq17f -> config-1793527710406-y2raf`, `generate` on the config node → the
config node's panel showed 参考图 1 张 with the kettle in it, the run went out and succeeded with one artifact,
and the canvas went from 40 nodes / 29 connections to 42 / 31 — a new image node holding the variant.

## 93. A pick is a way of typing, so it gives the caret back

**Decision** Choosing a node with `@` puts the focus back in the composer with the caret just after the label it
inserted; cancelling (escape, or the banner) leaves the `@` as typed and puts the caret back after it. The node's
own mousedown no longer moves the focus while a pick is in progress: picking is typing somewhere else, and the
caret belongs to the box that asked.

**Why** Without it the click ends the sentence: the caret stayed wherever the click left it, the next thing
typed went nowhere, and the user had to click into the box and find the end of the line again. The pick exists
to save typing, not to interrupt it.

**Tested** In a browser: typing "看看 @" then clicking the kettle node left `看看 @a rusty copper kettle on a
dark  ` in the box, `document.activeElement` a TEXTAREA, and `selectionStart` 37 — the end of what was
inserted. Escaping a pick left `看看 @ 再看 @` untouched with the caret at 9, still in the box.

## 94. The agent panel is the user's window: it opens from the corner it sits in, and it resizes

**Decision** The button that opens the agent panel is a round button in the canvas's bottom-right corner — where
the panel itself opens — not one of the small icons in the top bar. The panel can be resized by dragging either
of its corners: the bottom-right one, and the top-left one, which also moves the panel so that the opposite
corner stays where it was (a panel in its default corner is pinned by its right and bottom edges, so pulling the
top-left there needs no move at all). Both are clamped to 320×280 at the least and to the canvas (minus a 16px
margin) at the most. The
size is not persisted, for the same reason the position is not (#83's list of things that are not the canvas's):
a window's shape belongs to the window. The open button is shown only while the panel is closed, because when it
is open it covers that corner and carries its own close button.

**Why** The panel is the one surface that is not the canvas: it is a card you move, read in and put away, so it
should behave like one — and a card that can only be the size it was born at cannot hold a long answer or a
transcript you want to scan. The opening button belongs to the panel, so it lives where the panel appears; in the
top bar it was a small icon among other small icons, next to the sidebar's own collapse control, and it read as
part of the canvas's chrome rather than as "the assistant is over there".

**Tested** In a browser: the button rendered at (1653, 670) in a 1717×734 canvas — bottom-right — and clicking it
opened the panel; closing the panel showed the button again in the same corner. Dragging the new corner grip
took the panel from 380×480 to 536×446, and a drag towards the top-left clamped it at 320×360 (the minimum
width), with the size in the panel's own inline style rather than the class list.

## 95. A popup over the canvas has to say so twice: the marker, and the height

**Decision** A dropdown opened from inside the canvas carries `data-canvas-no-zoom` (so the canvas does not read
the click on an option as a click on its background), stops pointer events, **and** sets a z-index above the
canvas's own chrome — `z-[1500]` for the agent panel's two selects, whose card sits at `z-95`. That is the same
recipe `model-picker` uses (`z-[1200]`), and the marker-only version was not enough.

**Why** The first fix was the marker alone, and it was incomplete in a way only a person could see: radix renders
the popup in a portal at the document root with the component's own `z-50`, so the dropdown opened _underneath_
the panel that owns the trigger. Nothing threw, the accessibility tree listed the options, and a synthetic click
aimed at the element still changed the model — so it looked fixed. A real mouse click lands on whatever is on
top, which was the panel. A floating layer over the canvas is therefore two things, not one: told apart from the
canvas, and above it.

**Tested** In a browser, with `elementFromPoint` at the centre of an option: before, the option measured
`z-50` against a card at `z-95`; after, `z-1500` and the option is the topmost element at its own centre. Clicking
它 changed the panel to `Test / qwen3.8-27b` and the gateway logged `command="model_set"`; the level select
showed 不思考/普通思考/深度思考, and picking 不思考 logged `command="reasoning_level_set"`.

## 96. `@` needs no space in front of it

**Decision** Any `@` that lands at the caret in the composer asks for a node pick. The earlier rule — only after
whitespace, so that `foo@bar` would not trigger — is gone.

**Why** The rule was Latin typography imposed on a Chinese box: Chinese does not put a space between words, so
the natural way to cite a node is `看看@那个茶壶`, which did nothing at all, and the "fix" the rule asked of the
user was to type a space that then travelled into the prompt. The case it was protecting against — an address
being typed in an agent's message — is rare here and costs one escape: a cancelled pick leaves the `@` exactly as
typed. A gesture that refuses to start on the input it was made for is worse than one that starts too eagerly.

**Tested** In a browser: the draft `看看@` (no space before the `@`) showed "点击一个节点来引用 · ESC 取消";
clicking the kettle node replaced it with `看看@a rusty copper kettle on a dark  `, focus back in the textarea at
the end of what was inserted.

## 97. The system menu was four questions in one trench coat

**Decision** The hamburger at the top-left is gone. 我的资产 and 我的画布 became round buttons beside the
side-panel toggle, where the two "where is my stuff" questions are one click away instead of two. 导出当前画布
became a button in the bottom dock, right after 上传资产. 删除当前画布 joined the canvas menu behind the title,
next to 新建画布 and 我的画布. Everything the menu held now has a second home: 新建/重命名/切换 were already in
the title menu, 撤销/重做 were already in the dock.

**Why** One menu was answering four unrelated questions — where am I, where are my things, what is this canvas
doing, can I undo that — with the same three dividers. A menu is a place to put what has nowhere else to go;
once 导入 sat in the dock and 导出 sat only in a menu, the menu was a hiding place, not a home. Deleting it
costs nothing that is not already within one click of where it belongs, and removes a concept (a menu that
owns no subject) rather than adding one.

**Tested** In a browser (1717×734): the top-left renders 收起面板 / 我的资产 / 我的画布 as three buttons in one
row; the dock lists … 上传资产 / 导出当前画布 …, and `导出当前画布` was present in the accessibility tree next to
`上传资产`. The title menu reads 无限画布 1 (42 个节点) / 重命名 / 新建画布 / 我的画布 / **删除当前画布** (red).
A grep for `打开画布菜单` in the accessibility tree returns nothing: no hamburger is left.

## 98. The side panel is a board over the canvas, not a column beside it

**Decision** `CanvasSidePanel` is no longer a flex sibling that the canvas is laid out next to. It lives
inside the canvas `<section>` as `absolute left-0 top-16 bottom-0`, slides out with `translateX(-width)` and
`opacity: 0`, and turns `pointer-events` off while it is away. It is shut by default (`canvas-side-panel-open`
must say `1`), and the same top-left button opens and closes it. With the reflow gone, the store lost
`panelMounted`/`panelClosing` and the component lost the two-frame "render at 0, then flip" hack that existed
only because a CSS transition cannot run on first paint. Chrome that used to be pushed aside by the column now
steps over the board: `useSidePanelInset(gap)` — 20 for the zoom dock, 24 for the minimap, 300 for the bottom
dock.

**Why** The panel was reflowing the canvas: opening it moved every node on screen, so a person looking for a
node in the list lost the view they were looking at. A list of what is on the canvas is a way of _looking_, and
looking should not move the thing you are looking at. The floating board keeps the function and drops the
consequence.

The bottom dock's `left: 300` was **not** room for the side panel, which is what this change first assumed: it
is the width the zoom dock and minimap occupy in the bottom-left corner. Changing it to 16 made the dock
overlap the zoom dock as soon as the board was wide, which the browser showed immediately. It is
`useSidePanelInset(300)` now: still 300 from the canvas's left edge when the board is shut, 300 further right
when it is open — the same pixels as before, without the canvas ever moving.

**Tested** In a browser (1717×734, 42 nodes). The board sits at (0, 64), 280×670, bottom flush with the canvas.
Its first node's screen rect read `left=-53` with the board open, after closing it, after a resize to 360, and
after a reload — the canvas did not move once. Closing it put the aside at `left=-361` with
`pointer-events: none`; the zoom dock moved 380 → 20, and the bottom dock 660 → 300, both without touching the
canvas. `elementFromPoint(120, 300)` returned a button inside the aside, and `(900, 600)` returned the canvas,
so the board takes the pointer it covers. A clean storage (all three keys removed, reload) opened with the board
shut and width 280 and wrote nothing until it was toggled; after opening it, a reload kept it open.

## 99. The memory readout is ComfyUI's, or it is nothing

**Decision** `/api/system/stats` no longer samples the gateway's own host. It asks ComfyUI's
`/system_stats` what its machine has — `ram_total`/`ram_free` and, per device, `vram_total`/`vram_free` —
and reports that as `gpu` and `ram`, with `available: true`. When ComfyUI cannot be asked, or answers
without a device, `available` is false and the panel renders nothing at all. `internal/sysinfo` lost its
`nvidia-smi` shell-out and its `/proc/meminfo` reader and is now only the shape plus the translation.
`/api/system/free-memory` measures the freed memory from ComfyUI's own figures for the same reason.

**Why** The gateway and ComfyUI are usually _not_ the same machine — here the gateway runs on a 14 GB box
with no NVIDIA GPU at all, while ComfyUI is a 5080 with 16 GB of VRAM and 94 GB of RAM on another host.
The panel was printing the gateway host's numbers under the heading "VRAM" and "memory", so it read
`显存 --` and `内存 6/14G` while the machine doing the work had 15.6 GB of VRAM. A number about the wrong
machine is worse than a blank: it invites you to wait for memory that is not the memory that ran out.
ComfyUI already knows the answer about itself, so the fix needs no new protocol and no IP comparison
between gateway and ComfyUI — the question is simply asked in the right place.

The `utilization` and `temperature` fields are gone: ComfyUI does not report them, and they were the only
numbers the local sampler had that ComfyUI lacks. The panel shows a device's name and its memory, which
is what it can be told.

**Tested** Against the real pair (`gateway :8191` → `comfy 192.168.3.233:8188`), the endpoint returned
`available: true`, `gpu[0] = cuda:0 NVIDIA GeForce RTX 5080, 821 / 15933 MB`, `ram = 22.1 / 93.6 GB` —
figures that cannot have come from the gateway host (14 GB, no GPU). In a browser, the bar read
`显存 0.5/15.6G · 内存 20.0/93.6G`, and its dialog listed the 5080 by name with no load or temperature
line. Pointed at a gateway whose ComfyUI is unreachable (`:8192` → `:8198`), the same bar showed only the
account buttons: no readout, no zeros. `POST /api/system/free-memory` there returned
`503 comfy_unreachable`. Go: `TestSystemStatsReportComfysMachine`,
`TestSystemStatsAreAbsentWhenComfyReportsNoDevices`, `TestSystemStatsAreAbsentWhenComfyIsGone`, and the
`sysinfo.FromComfy` unit tests (units, `nil` for no device, index fallback).

## 100. The conversation is a file, and the process is only what works on it

**Decision** Every agent session is spawned with `alayacore --session <file>`, one file per session name,
in `DATA_DIR/agent/sessions/`. The process now lives exactly as long as its socket instead of outliving it,
and the conversation comes back the way AlayaCore offers it: a new process replays the file before it says
`session ready`, and the gateway relays that as one `history` event. The panel draws `history` by
**replacing** its messages, not appending. The canvas panel's session name became `canvas:<canvasId>` —
it was the bare `"canvas"` for every canvas, one conversation shared by all of them.

**Why** The panel kept its transcript in `useState`, so a reload showed an empty panel while the agent, in
a process that had survived the reload, still remembered everything: a blank screen answering from
invisible context. That was the worst of both worlds — worse than either side forgetting.

The fix is not in the page. AlayaCore already keeps a conversation when told where to put it, and already
replays it on start (a 733-byte file for one turn, `context_tokens: 222` restored before `ready`). What was
missing was the two things that let it happen: a file to write to, and a process short-lived enough to
have to read it. Keeping the process alive across reloads was the very thing that prevented the replay.

Two frames made this safe to wire, and both were checked against the real binary: replayed content arrives
as **complete** `UX`/`AX` frames carrying text, while a live turn ends its messages with `AX` frames that
carry **no** text at all — so "text present" is the whole difference between history and a full stop, and
nothing is drawn twice. `UX` echoes exist in both cases, which is why history is defined by _when_ it
arrives (before `ready`), not by its tag.

The name had to change with the file. One name for every canvas meant one file for every canvas: what was
said on one canvas would be replayed on the next.

**Tested** In a browser against a real AlayaCore (`gateway :8191` → `Test / qwen3.8-27b`): asked the panel
to remember a code word, got `好的`; the session file `canvas-UyrJZS8t68LWjeruICYOR-bb5c104f.session`
appeared (744 bytes) and the live process's command line carried
`--session /tmp/gw8191/data/agent/sessions/canvas-UyrJZS8t68LWjeruICYOR-bb5c104f.session`. Reloaded the
page, reopened the panel: both messages were there. Asked "刚才让你记的暗号是什么?" and the answer was
`紫罗兰` — the context, not just the text, had come back in a new process. Closing and reopening the panel
inside the same page showed the same four messages and no duplicates, so a replay replaces. The gateway log
for the whole sequence: `released session=… reason=socket closed`, then `spawn … spawned=true`.

Go: `TestSocketGoingAwayReplaysTheConversation` (fake agent that keeps its own session file: prompt, drop
the socket, reconnect, and the new process replays the question and the answer); `TestMCPToolSaysWhenNoCanvasIsWatching`
now reports the session's disappearance; the workspace tests read the new `agent/workspaces/` path.
`make check`, `make test-race`, `npm run check`, `vitest 125` green.

## 101. A replayed citation shows the words, not the block

**Decision** The block the canvas puts in front of a cited prompt — `【画布引用】` and one line per node —
now has a home of its own, `lib/canvas/agent-references.ts`, and that module can take it off again
(`withoutReferences`). The panel uses the first half when sending and the second half when it draws a
user message replayed from the agent's session file.

**Why** What is sent and what was typed are different strings, and until now only the sent one could
survive a reload. So a message that read `@补全背景 描述一下这张图片的背景。` came back as:

```
[Canvas references]
- lPiENyy6iXVTwtTVDzd7M (image) 补全背景: 补全背景

@补全背景 描述一下这张图片的背景。
```

The block is plumbing for the agent — `@补全背景` means nothing to it without the node's id — and it is
not something anybody typed. The stripper recognises the marker in **either language**, because a
conversation written in Chinese can be read back by an English UI, and it only cuts where the block
actually ends (marker, then a blank line): a prompt that merely starts with those characters keeps every
word.

**Tested** Unit tests pair the two directions against each other: what `referenceBlock` builds,
`withoutReferences` removes, in both languages and across a language switch; a marker without its blank
line is left alone; a prompt without a block is returned unchanged. In a browser: the session file was
edited to carry exactly the block from the report, the page was reloaded, and the panel drew
`@补全背景 描述一下这张图片的背景。`.

## 102. `editCanvasRatio` was UI state wearing a workflow input's clothes

**Decision** The image settings popover no longer writes `editCanvasRatio`. Which ratio pill is lit is read
back from `editCanvasSize` — the workflow's own input — through the same table that wrote it
(`canvasRatioOf`). A format 2 → 3 migration deletes the key from nodes that already carry it.

**Why** Every edit run from a node whose params held that key failed with

```
unknown input "editCanvasRatio"; workflow image-qwen21 accepts: background, cfg, count, …
editCanvasSize, layout, …
```

The key was never a workflow input: it was "which pill did I click" parked next to the real inputs and
shipped with them. The gateway is right to refuse it — an input it does not know is either a typo or a
version skew, and guessing which would be worse than saying so. The fix belongs on the side that invented
the key.

Deriving it beats keeping it somewhere else: the pill _is_ the size, so a second field can only drift from
the first. Old canvases are repaired on load rather than left to fail, which is what the migration is for.

**Tested** `tests/persist-migration.test.ts`: a version-2 blob whose node params hold
`{editCanvasRatio, editCanvasSize, resolution, seed}` reads back as
`{editCanvasSize, resolution, seed}`, and a node with nothing stored is untouched.

## 103. Deleting a canvas forgets its conversation

**Decision** `DELETE /api/agent/session/{session}` releases the process if one is running, removes the
session file and the session's workspace, and answers `{ok, removed}`. The canvas calls it for each canvas
it deletes — from the canvas menu and from the multi-select dialog — with the name it already owns
(`canvas:<canvasId>`), fire and forget.

**Why** A conversation now outlives the page, which is the point, but a canvas does not outlive the canvas.
Without this, every deleted canvas left a file in `DATA_DIR/agent/sessions/` that nothing would ever read
again — the user's words, kept forever, for a canvas that no longer exists. Only the canvas knows when a
conversation is over: the gateway sees names, not canvases.

Closing the panel is deliberately _not_ this. A closed panel is a conversation waiting to be reopened; a
deleted canvas is a conversation with nobody left to have it.

**Tested** `TestDeletingASessionForgetsTheConversation`: prompt, wait for the turn, confirm the file exists,
`DELETE`, confirm it is gone and that the next connection replays nothing. In a browser: a scratch canvas
was opened with its panel (workspace created), given a session file, then deleted through the canvas menu
and its confirmation — the gateway logged
`DELETE /api/agent/session/canvas%3APrivX2hRZQHWmyUJmmsP6 200`, and both the file and the workspace were
gone while the other canvas's conversation was untouched.

## 104. The transcript is the agent's, and only a conversation keeps a file

**Decision** Two things that were the same mistake:

1. The panel no longer writes a message when the user presses send. A user message is drawn from the agent's
   own echo (`UX`, which it sends live as well as on replay), and the assistant's message starts at the
   first delta instead of being created empty beforehand. The panel holds no message the agent did not
   report.
2. A session file is created only when the client asks for one: `GET /api/agent/socket/{name}?history=1`.
   The canvas panel asks; a node's panel does not, so a one-shot question leaves nothing on disk.

**Why** (1) The submit handler drew the typed text itself, and the replay drew the agent's copy — two copies
of the same message, which is why they could disagree (the typed `@补全背景 …` versus the stored
`[Canvas references] …` block). Stripping the block on replay patched the symptom and left both copies in
place. One owner removes the class: what is on screen is what the agent says was said, live and replayed,
and the citation block is stripped in one place because there is only one place.

(2) A node's agent is asked one question at a time — reverse-prompt, text generation — and writes the
answer into the node. Giving it a session file left one file per node in the data directory with nothing to
ever read or delete it, and it would carry an earlier question into the next answer. Whether a name is a
conversation is a property of the surface, and the surface is the client, so the client says so in the
socket URL. The gateway keeps filing processes under opaque names.

**Tested** Gateway: `TestSocketRelaysTheUsersWordsOnce` (a prompt produces exactly one `user` event, with the
words as sent) and `TestOneShotSessionKeepsNoFile` (a socket without the flag prompts and answers, and
`sessionFile("node:n1")` does not exist). In a browser against a real AlayaCore: after a reload the replayed
conversation still drew, and a new turn showed the typed sentence once — with the answer appearing under it
as it streamed — while nothing was added by the submit handler.

## 105. Sending is never blocked, and the queue is visible

**Decision** The panel no longer refuses to send while a turn is in flight, and it shows how many messages are
waiting in the agent's queue above the input: `2 条消息已排队,会在当前步骤之后一起插入`. The gateway relays
AlayaCore's `SM steering` frames as `{type:"steering", phase:"queued"|"delivered"}`; the panel adds one per
`queued` and **zeroes** on `delivered`. The "End conversation" button in the header is gone, and with it
`canvas.agent.end`.

**Why** AlayaCore has its own queue. A message sent into a running task is inserted between the agent's steps
— that is in the gateway's own reading loop, which explicitly may not assume one prompt equals one answer —
so a send button that goes dead for the length of a three-hundred-word answer is the panel inventing a
limitation the agent does not have.

But queueing invisibly is its own problem: the message is accepted and then nothing happens for a while, which
looks exactly like a message that was lost. AlayaCore already announces both ends of the queue
(`phase: queued` when it takes the message, `phase: delivered` when it works it in), so the panel says so,
where the message was sent from.

The number is counted from the `queued` frames, and the frame that clears it is the one that arrives once.
AlayaCore inserts a whole batch at the same time and says `delivered` **once** for it — measured with three
messages sent into one running task: `queued`, `queued`, `queued`, `delivered`. Decrementing on delivery (the
first version) therefore left a queue on screen that no longer existed, which is the one thing worse than not
showing it at all: the message looks stuck. The panel zeroes instead.

The End conversation button promised something that stopped being true when conversations moved into session
files: it closed the socket and released the process, and the next open replayed the conversation anyway. A
button that claims to end a conversation and does not is worse than no button — closing the panel is the only
"leave" the panel has, and deleting the canvas is the only end.

**Tested** Gateway: `TestSocketRelaysTheSteeringQueue` (the two phases arrive in order). In a browser against a
real AlayaCore: a 300-word request was sent, the send button stayed enabled while it streamed, a second
message was accepted mid-answer — the line above the input read `1 条消息已排队,会在当前步骤之后插入` and then
disappeared when delivered — and both messages and both answers ended up in the transcript in order. The
gateway log shows the pair:
`<< SM {"type":"steering","data":{"phase":"queued"…` then `…"phase":"delivered"}`. The header now lists only
`关闭` and `发送`.
