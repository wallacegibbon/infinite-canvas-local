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

**Why** The repository *is* a Go service with one vendored frontend. An extra `gateway/`
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
`legacy-peer-deps=true` for the whole subtree — a flag that also silences the *next* real peer
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

**Why** A workbench whose gateway is on `192.168.3.232:8190` so a phone can use it *is* the
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
gatewayModelLabel(modelId, gatewayLabel)   // gateway.models.<id>.label
gatewayVariantLabel(mode, gatewayLabel)    // gateway.variants.<mode>
gatewayModelDescription(modelId, gatewayLabel)
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
graph *is* how a model is defined here: `workflows/<type>-<slug>.json` plus an entry in
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
is to make *it* speak ComfyUI's protocol — that costs a compatibility layer in the new thing
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

- *antd's internal DOM.* Every `.ant-*` class name used by application logic now lives in
  `src/lib/canvas-overlay.ts`. The overlay guards match our own `data-canvas-overlay` marker
  and the existing `data-canvas-no-zoom` first; the antd class list is a documented fallback
  array beneath them. When antd goes, that array goes and the canvas is not touched.
- *The palette.* Colour values live in `src/lib/design-tokens.ts`; `src/lib/app-theme.ts`
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
It also fixed a real inconsistency: `canvas-node-prompt-panel.tsx` used antd's *static*
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
the *cloud-mode* contract (an explicit but unavailable model selection is preserved so readiness
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
could reach the cloud interface. It was inherited from the upstream fork, where it *was* the
main interface, and kept as an escape hatch that only a source-code comment mentioned. A branch
nobody builds is a branch nobody validates: it had already produced four permanently failing
tests (#26), and two of its pages were the largest single consumers of antd. The rule applied
here is the one from #23 — an unsimplified fork is not a smaller dependency, it is a second
product that silently rots.

**How the deletion was bounded** Not by reading: by reachability. A script walked imports from
`main.tsx`, simulating the removed routes as absent, and reported modules nothing could reach.
That found 8 modules (956 lines) already dead *before* any decision, and after each removal it
was re-run until only `vite-env.d.ts` remained — an ambient-types file, not dead code.
`tsc --noUnusedLocals --noUnusedParameters` then caught the second class of leftovers the
graph cannot see: unused imports and unused locals (45 of them). Three Chinese UI strings that
the local branches already carried moved into the locale files, because inline copy violates
#17 whether or not a flag guards it.

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
