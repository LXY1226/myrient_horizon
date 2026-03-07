# Worker Verifier-Style Bootstrap Alignment

## TL;DR
> **Summary**: Realign `cmd/worker` and `cmd/verifier` to the current `internal/worker` runtime model by using fail-fast `InitWorker` / `InitVerifier` entrypoints, package globals, and direct startup sequencing from `config.Global`, instead of trying to satisfy the stale `Get*`/`sync.Once`/returned-instance API that no longer exists.
> **Deliverables**:
> - fail-fast worker/verifier bootstrap contract in `internal/worker`
> - removal of stale `GetReporter` / `GetDownloader` / `sync.Once` startup patterns from the worker path
> - `cmd/worker` and `cmd/verifier` rewritten to match verifier-style direct initialization and current runtime owners
> - verification evidence proving both commands build and fail fast at controlled startup boundaries rather than through nil-pointer/runtime drift
> **Effort**: Medium
> **Parallel**: YES - 3 waves
> **Critical Path**: 1 -> 2/3/4 -> 5/6 -> 7/8/9

## Context
### Original Request
接下来请帮我修复worker，以verifier为范本，能用全局变量和直接的初始化解决的就不使用Get/Set/sync.Once来解决。后续补充：`worker` 和 `verifier` 都依赖配置文件启动，所以允许保留 `InitWorker` / `InitVerifier`，但它们必须通过 `config` 包全局配置取值、不返回实例和错误、在出错点直接 panic/fatal。

### Interview Summary
- Scope includes both the immediate `cmd/worker` repair and the related worker-side singleton cleanup needed to align startup with verifier-style direct initialization.
- The user explicitly prefers package globals and direct init over `Get*`/`Set*` wrappers and `sync.Once`, but accepts thin boot functions such as `InitWorker` and `InitVerifier`.
- Failures in this startup path should be fail-fast at the exact init site; do not bubble errors outward for outer `if err != nil` branches.
- No new automated tests should be introduced; verification stays build/static/runtime-path based.
- `cmd/verifier` is also in scope for startup alignment because it currently depends on the same worker package bootstrap and reporter globals.

### Metis Review (gaps addressed)
- Locked the default implementation direction to shrink both command roots to the currently implemented worker runtime model instead of recreating missing `InitReporter/GetReporter/SendHeartbeat/Flush/UpdateRequired` APIs.
- Added explicit guardrails for fail-fast init functions: no return values, direct `config.Global` usage, and no caller-side error branches after init.
- Added command-root fixes for both `cmd/worker` and `cmd/verifier`, because `cmd/verifier` currently compiles but still has broken startup flow (`Reporter` is nil and `Reporter.Run(...)` is called synchronously).
- Defaulted `Verifier.Run` and `Downloader.Run` to accept `context.Context`, while `Reporter.Run` remains `Run(wsURL, workerKey string)` and `Reporter.Close` remains no-arg.

## Work Objectives
### Core Objective
Make the worker startup path consistent with the current verifier-style runtime model by centralizing bootstrap in fail-fast init functions, using package globals directly, and removing stale API expectations that no longer exist in `internal/worker`.

### Deliverables
- `internal/worker` exposes a decision-complete bootstrap contract centered on `InitWorker` and `InitVerifier`.
- Reporter, verifier, downloader, tree, and config state are initialized and consumed through explicit package globals rather than accessor wrappers or `sync.Once` singletons.
- `cmd/worker/main.go` no longer references missing/stale APIs such as `GetReporter`, `UpdateRequired`, `SendHeartbeat`, `Flush`, or server-pushed concurrency fields that are absent from local config.
- `cmd/verifier/main.go` starts reporter/verifier state in a non-blocking, nil-safe way and retains its directory-walk verification behavior.
- Verification evidence proves both commands compile and fail fast at controlled config/tree/bootstrap boundaries.

### Definition of Done (verifiable conditions with commands)
- `go build ./cmd/worker ./cmd/verifier ./internal/worker/...` succeeds.
- `go vet ./cmd/worker ./cmd/verifier ./internal/worker/...` succeeds.
- Static searches under `cmd/worker` and `internal/worker` find no remaining startup-path `GetReporter`, `GetDownloader`, or `sync.Once` usage.
- Static searches confirm `cmd/worker/main.go` no longer references `UpdateRequired`, `OnConfigUpdate`, `SendHeartbeat`, `Flush`, `cfg.VerifyConcurrency`, `cfg.DownloadConcurrency`, or `cfg.Simultaneous`.
- Running the commands in controlled temp workdirs produces explicit config/tree/bootstrap failures, not nil-pointer or undefined-symbol failures.

### Must Have
- Keep `cmd/verifier/main.go` as the structural template for direct config loading, `config.Global` assignment, and package-global runtime usage.
- Introduce or preserve thin `InitWorker` / `InitVerifier` boot functions that return nothing and fail fast at the init site.
- Read durable startup inputs from `internal/worker/config.Global`; do not add new local config fields just to resurrect stale `cmd/worker` branches.
- Keep reporter, verifier, and downloader as explicit package-owned runtime globals with a single initialization point.
- Make command roots consume globals directly after init; no `Get*` accessors in the worker startup path.

### Must NOT Have (guardrails, AI slop patterns, scope boundaries)
- Must NOT recreate the stale returned-instance API surface (`InitReporter` returning status, `GetReporter`, `GetDownloader`, `UpdateRequired`, `SendHeartbeat`, `Flush`, `OnConfigUpdate`) just to preserve the old `cmd/worker/main.go` shape.
- Must NOT add `sync.Once`, `Get*`, or `Set*` wrappers back into `internal/worker` startup paths.
- Must NOT add new automated test infrastructure, CI config, or protocol/config schema changes.
- Must NOT edit `cmd/server`, `internal/server`, `pkg/protocol/messages.go`, or `internal/worker/aria2/aria2.go` except for imports or compile-only touch-ups that are strictly required by the startup refactor.
- Must NOT invent new local config fields for download/verify concurrency; use the existing hardcoded bootstrap default from `cmd/worker/main.go` where a default is required.

## Verification Strategy
> ZERO HUMAN INTERVENTION - all verification is agent-executed.
- Test decision: none for new tests; use `go build`, `go vet`, targeted startup runs, and static searches.
- QA policy: Every task includes agent-executed happy-path and failure/edge scenarios.
- Evidence: `.sisyphus/evidence/task-{N}-{slug}.{ext}`

## Execution Strategy
### Parallel Execution Waves
> Target: 5-8 tasks per wave. <3 per wave (except final) = under-splitting.
> Extract shared dependencies as Wave-1 tasks for max parallelism.

Wave 1: startup contract and runtime-owner foundations (`1`, `2`, `3`)
Wave 2: downloader/bootstrap composition and command-root alignment (`4`, `5`, `6`)
Wave 3: stale-API cleanup and verification (`7`, `8`, `9`)

### Dependency Matrix (full, all tasks)
- `1` blocks `2-9`
- `2` blocked by `1`; blocks `5-9`
- `3` blocked by `1`; blocks `5-9`
- `4` blocked by `1`; blocks `5-9`
- `5` blocked by `2-4`; blocks `7-9`
- `6` blocked by `2-4`; blocks `7-9`
- `7` blocked by `5-6`; blocks `8-9`
- `8` blocked by `5-7`; blocks `9`
- `9` blocked by `5-8`

### Agent Dispatch Summary (wave -> task count -> categories)
- Wave 1 -> 3 tasks -> `unspecified-high`, `deep`
- Wave 2 -> 3 tasks -> `unspecified-high`, `quick`
- Wave 3 -> 3 tasks -> `quick`, `deep`

## TODOs
> Implementation + Test = ONE task. Never separate.
> EVERY task MUST have: Agent Profile + Parallelization + QA Scenarios.

- [x] 1. Freeze the supported worker/verifier bootstrap contract and stale API removal list

  **What to do**: Before editing code, capture the exact startup contract the implementation must converge to: `EnsureConfig`/`config.Global` -> `LoadTree` -> `InitVerifier` or `InitWorker` -> direct use of package globals. Record the stale `cmd/worker/main.go` references that must be deleted rather than reimplemented: `GetReporter`, `InitReporter` returning results, `UpdateRequired`, `OnConfigUpdate`, `SendHeartbeat`, `Flush`, and local-config concurrency fields that do not exist in `internal/worker/config.WorkerConfig`.
  **Must NOT do**: Do not add compatibility shims for the stale API surface; do not start implementing new runtime behavior in this task.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: contract locking, inventories, and static divergence mapping
  - Skills: none
  - Omitted: `playwright` - why not needed: CLI/backend-only work

  **Parallelization**: Can Parallel: NO | Wave 1 | Blocks: `2,3,4,5,6,7,8,9` | Blocked By: none

  **References** (executor has NO interview context - be exhaustive):
  - Template: `cmd/verifier/main.go` - direct config load, `config.Global` assignment, and package-global startup usage
  - Broken entrypoint: `cmd/worker/main.go` - source of stale API expectations that must be pruned
  - Bootstrap baseline: `internal/worker/worker.go` - current config bootstrap contract
  - Global config: `internal/worker/config/config.go` - defines `config.Global` and the actual persisted config fields
  - Reporter owner: `internal/worker/reporter.go` - current global reporter shape and no-arg `Close`
  - Verifier logic: `internal/worker/verify.go` - current `Task.Verify` flow and queue channel
  - Downloader owner: `internal/worker/downloader.go` - current `sync.Once` pattern to replace
  - Runtime intent note: `PLAN.md` - confirms worker should distribute tasks to downloader, verifier, reporter

  **Acceptance Criteria** (agent-executable only):
  - [ ] Evidence lists every stale startup-path symbol to delete or replace, with no ambiguity about whether it should be recreated.
  - [ ] Evidence names the exact supported bootstrap order and the files responsible for each step.
  - [ ] Evidence confirms `internal/worker/config.WorkerConfig` does not contain download/verify concurrency fields, so the old `cmd/worker` config-update branch cannot be preserved as-is.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Bootstrap divergence captured
    Tool: Bash
    Steps: Run static searches for `GetReporter|InitReporter|UpdateRequired|OnConfigUpdate|SendHeartbeat|Flush|cfg.VerifyConcurrency|cfg.DownloadConcurrency|cfg.Simultaneous` across `cmd/worker/main.go` and related worker files; save the output.
    Expected: Evidence clearly separates supported runtime owners from stale symbols to remove.
    Evidence: .sisyphus/evidence/task-1-bootstrap-contract.txt

  Scenario: Local config mismatch confirmed
    Tool: Bash
    Steps: Inspect `internal/worker/config/config.go` and `pkg/protocol/messages.go`, then record that only protocol-side config carries `VerifyConcurrency`/`DownloadConcurrency` while local worker config does not.
    Expected: Evidence proves the stale worker-main branch depends on fields that are unavailable at local bootstrap time.
    Evidence: .sisyphus/evidence/task-1-bootstrap-contract-error.txt
  ```

  **Commit**: NO | Message: `n/a` | Files: `.sisyphus/evidence/*`

- [x] 2. Add fail-fast reporter bootstrap owned by verifier/worker init

  **What to do**: Refactor `internal/worker/reporter.go` so reporter lifecycle is explicitly bootstrapable without getters or returned instances. Keep `Reporter` as the package global, keep `Run(wsURL, workerKey string)` and `Close()` semantics, and add an init helper that creates the reporter state in one place and fatals/panics immediately if bootstrap prerequisites are invalid. This helper may stay private if `InitVerifier` / `InitWorker` are the only intended exported entrypoints.
  **Must NOT do**: Do not add `GetReporter`; do not recreate the old `InitReporter(...)(result,error)` shape; do not add heartbeat/update channels just to satisfy stale `cmd/worker` code.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - Reason: lifecycle-sensitive runtime owner cleanup with behavior preservation
  - Skills: none
  - Omitted: `git-master` - why not needed: no git work required during execution

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: `5,6,7,8,9` | Blocked By: `1`

  **References** (executor has NO interview context - be exhaustive):
  - Owner: `internal/worker/reporter.go` - current reporter struct, `Reporter` global, `Run`, `Close`, `PushVerified`
  - Bootstrap precedent: `internal/worker/worker.go` - fail-fast config bootstrap style already used by `EnsureConfig`
  - Caller template: `cmd/verifier/main.go` - currently tries to use `worker.Reporter` directly and must stop assuming implicit init
  - Broken caller: `cmd/worker/main.go` - currently expects nonexistent returned reporter/init result
  - Message shapes: `pkg/protocol/messages.go` - reporter still sends/receives ping/pong/task-sync messages through existing protocol types

  **Acceptance Criteria** (agent-executable only):
  - [ ] Reporter initialization is performed in exactly one worker-package bootstrap path and does not return a reporter instance.
  - [ ] `Reporter.Run` and `Reporter.Close` signatures remain internally coherent with the command roots that will call them.
  - [ ] No `GetReporter` symbol exists after this task.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Reporter bootstrap is explicit and accessor-free
    Tool: Bash
    Steps: Search `internal/worker/reporter.go`, `internal/worker/worker.go`, and command roots for `GetReporter` and reporter init symbols; save results.
    Expected: Searches show a single explicit reporter bootstrap path and no getter usage.
    Evidence: .sisyphus/evidence/task-2-reporter-bootstrap.txt

  Scenario: Reporter call signatures stay aligned
    Tool: Bash
    Steps: Build `./cmd/verifier` and `./cmd/worker` after the reporter bootstrap refactor and capture any reporter-related compile failures.
    Expected: There are no reporter-related signature mismatches such as `Close(ctx)` vs `Close()`.
    Evidence: .sisyphus/evidence/task-2-reporter-bootstrap-error.txt
  ```

  **Commit**: YES | Message: `refactor(worker): make reporter bootstrap fail-fast and global` | Files: `internal/worker/reporter.go`, optional helper in `internal/worker/worker.go`

- [x] 3. Implement `InitVerifier` as the exported verifier bootstrap and global runtime owner

  **What to do**: Replace the current verifier-only function model in `internal/worker/verify.go` with a minimal verifier runtime owner that still preserves `Task.Verify()` as the core verification primitive. Export `InitVerifier` as a no-return, fail-fast bootstrap entrypoint that reads `config.Global`, ensures reporter state exists, seeds the bootstrap concurrency default from the old `cmd/worker/main.go` constant (`2`), and exposes the methods the commands actually need: `Run(ctx)`, `Submit`, `SetConcurrency`, and `Count` on the package-global `Verifier` owner.
  **Must NOT do**: Do not add new persisted config fields for verifier concurrency; do not return verifier instances; do not remove `Task.Verify()` or inline verification logic into the commands.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - Reason: new runtime owner must preserve existing verify semantics while aligning command APIs
  - Skills: none
  - Omitted: `frontend-ui-ux` - why not needed: no UI relevance

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: `5,6,7,8,9` | Blocked By: `1`

  **References** (executor has NO interview context - be exhaustive):
  - Existing verify primitive: `internal/worker/verify.go` - contains `Task.Verify`, `RunVerifier`, `verifyTaskCh`, and hash helpers
  - Task/report coupling: `internal/worker/task.go` - task paths and tree lookups used by verifier
  - Reporter sink: `internal/worker/reporter.go` - `PushVerified` consumes the output of `Task.Verify`
  - Bootstrap caller: `cmd/verifier/main.go` - standalone verifier command that should use `InitVerifier`
  - Old default: `cmd/worker/main.go` - hardcodes `InitVerifier(2)`, which should become the bootstrap default rather than a new local config field

  **Acceptance Criteria** (agent-executable only):
  - [ ] `internal/worker` exports a no-return `InitVerifier` and a package-global verifier owner.
  - [ ] The verifier owner exposes `Run(ctx)`, `Submit`, `SetConcurrency`, and `Count`, and these methods compile with both command roots.
  - [ ] `Task.Verify()` still routes verified hashes through `Reporter.PushVerified` instead of duplicating reporting logic in commands.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Verifier owner API matches command needs
    Tool: Bash
    Steps: Search `internal/worker/verify.go` for `InitVerifier`, `Run(`, `Submit`, `SetConcurrency`, and `Count`; then build `./cmd/verifier` and `./cmd/worker`.
    Expected: The verifier owner exposes the exact command-facing API and compiles without returned-instance shims.
    Evidence: .sisyphus/evidence/task-3-verifier-owner.txt

  Scenario: Verifier still reports through reporter
    Tool: Bash
    Steps: Search `internal/worker/verify.go` and `internal/worker/reporter.go` for `PushVerified` call sites after the refactor.
    Expected: `Task.Verify()` still forwards results through the reporter, and no duplicate command-level reporting path appears.
    Evidence: .sisyphus/evidence/task-3-verifier-owner-error.txt
  ```

  **Commit**: YES | Message: `refactor(worker): add fail-fast verifier bootstrap` | Files: `internal/worker/verify.go`, optional helper touch in `internal/worker/reporter.go`

- [x] 4. Remove downloader `sync.Once`/getter wiring and keep it as a direct runtime global

  **What to do**: Simplify `internal/worker/downloader.go` so the downloader startup path matches the user's preference: no `sync.Once`, no `GetDownloader`, no returned singleton instance. Keep the package-global downloader owner explicit, initialize it in one place, and make `Run` accept `context.Context` so the worker command can supervise downloader lifecycle consistently with verifier. Preserve existing task channels, aria2 client ownership, reconnect helpers, and `Reporter`/`verifyTaskCh` interactions.
  **Must NOT do**: Do not redesign aria2 behavior; do not add new concurrency primitives beyond what is required to remove `sync.Once`; do not hide downloader access behind a new accessor wrapper.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - Reason: stateful runtime owner cleanup with live-client coupling
  - Skills: none
  - Omitted: `playwright` - why not needed: backend-only runtime work

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: `5,6,7,8,9` | Blocked By: `1`

  **References** (executor has NO interview context - be exhaustive):
  - Owner to refactor: `internal/worker/downloader.go` - current `sync.Once`, `InitDownloader`, `GetDownloader`, channels, reconnect helpers
  - Client contract: `internal/worker/aria2/aria2.go` - downloader client methods (`TellWaiting`, `TellActive`, `ReconnectOnly`, `Close`, `IsAlive`)
  - Reporter dependency: `internal/worker/reporter.go` - consumes downloader state through package-level access
  - Verifier handoff: `internal/worker/verify.go` - downloader success path should still route into verifier/verify queue
  - Caller target: `cmd/worker/main.go` - currently calls `downloader.Run(ctx)` and expects a directly usable runtime owner

  **Acceptance Criteria** (agent-executable only):
  - [ ] `internal/worker/downloader.go` contains no `sync.Once` and no `GetDownloader` symbol.
  - [ ] Downloader initialization is explicit, returns nothing, and leaves a package-global runtime owner ready for command use.
  - [ ] `Downloader.Run(ctx)` compiles with the worker command and preserves existing helper usage (`adoptExisting`, `reconnect`, `drainEvents`).

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Downloader is direct-init only
    Tool: Bash
    Steps: Search `internal/worker/downloader.go` for `sync.Once`, `GetDownloader`, and returned `InitDownloader` signatures; save the results.
    Expected: The file shows direct global initialization only, with no stale singleton wrapper patterns.
    Evidence: .sisyphus/evidence/task-4-downloader-global.txt

  Scenario: Downloader lifecycle matches worker call site
    Tool: Bash
    Steps: Build `./cmd/worker` after the downloader refactor and capture any `Run`/context/signature errors.
    Expected: `cmd/worker` compiles without downloader signature mismatches.
    Evidence: .sisyphus/evidence/task-4-downloader-global-error.txt
  ```

  **Commit**: YES | Message: `refactor(worker): drop downloader singleton wrappers` | Files: `internal/worker/downloader.go`

- [x] 5. Add `InitWorker` as the no-return composition root for worker runtime owners

  **What to do**: Use `internal/worker/worker.go` to define the worker bootstrap sequence after config/tree load: validate `config.Global`, require a non-nil aria2 client input, initialize verifier/reporter state, initialize downloader state, and leave the package globals ready for direct consumption by `cmd/worker/main.go`. `InitWorker` must return nothing and panic/fatal immediately on invalid bootstrap inputs or impossible init states.
  **Must NOT do**: Do not create a second config-loading path inside `InitWorker`; do not return a worker struct; do not duplicate `EnsureConfig` or `LoadTree` logic inside command roots after introducing `InitWorker`.

  **Recommended Agent Profile**:
  - Category: `deep` - Reason: composition-root design must lock initialization order and failure boundaries across multiple runtime owners
  - Skills: none
  - Omitted: `frontend-ui-ux` - why not needed: no UI work

  **Parallelization**: Can Parallel: NO | Wave 2 | Blocks: `7,8,9` | Blocked By: `2,3,4`

  **References** (executor has NO interview context - be exhaustive):
  - Bootstrap home: `internal/worker/worker.go` - current `EnsureConfig` behavior and correct fail-fast style
  - Config source: `internal/worker/config/config.go` - `config.Global` and persisted worker settings
  - Runtime owners: `internal/worker/reporter.go`, `internal/worker/verify.go`, `internal/worker/downloader.go`
  - Command dependency: `cmd/worker/main.go` - composition root consumer that should become thin after `InitWorker`
  - Existing tree bootstrap: `internal/worker/task.go` - tree must already be loaded before runtime owners start using task metadata

  **Acceptance Criteria** (agent-executable only):
  - [ ] `internal/worker` exports `InitWorker` as a no-return bootstrap entrypoint.
  - [ ] `InitWorker` consumes `config.Global` plus a non-nil aria2 client and establishes the runtime globals in a documented order.
  - [ ] The implementation uses fail-fast fatal/panic behavior at the init site instead of returning bootstrap errors to the command root.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Worker composition root is explicit
    Tool: Bash
    Steps: Search `internal/worker/worker.go` for `InitWorker`, `config.Global`, and the calls that initialize verifier/reporter/downloader; save the output.
    Expected: Evidence shows a single no-return worker bootstrap path and a clear init order.
    Evidence: .sisyphus/evidence/task-5-init-worker.txt

  Scenario: Invalid bootstrap input fails fast
    Tool: Bash
    Steps: Add or run a targeted compile/runtime check that calls the worker bootstrap through `cmd/worker` with intentionally invalid prereqs (for example missing config/tree/client) and capture the first fatal/panic site.
    Expected: Failure occurs at the exact init boundary with a clear message, not through a later nil-pointer dereference.
    Evidence: .sisyphus/evidence/task-5-init-worker-error.txt
  ```

  **Commit**: YES | Message: `refactor(worker): add fail-fast worker bootstrap` | Files: `internal/worker/worker.go`, related worker runtime files

- [x] 6. Rewrite `cmd/verifier/main.go` to use `InitVerifier` and non-blocking reporter startup

  **What to do**: Update the standalone verifier command so it follows the new bootstrap contract: load config, assign `config.Global`, load the tree, call `worker.InitVerifier()`, then start reporter in a goroutine before scanning files. Keep the directory-walk verification behavior, bad-report logging, and graceful reporter close/wait path at the end. The command must stop assuming `worker.Reporter` is implicitly initialized and must stop calling `Reporter.Run(...)` synchronously on the main goroutine.
  **Must NOT do**: Do not move scanning logic into `internal/worker`; do not change the matching/verification walk semantics; do not introduce command-level duplicate reporting logic.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - Reason: entrypoint rewrite with behavior-sensitive command flow
  - Skills: none
  - Omitted: `git-master` - why not needed: no git step in implementation

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: `8,9` | Blocked By: `2,3`

  **References** (executor has NO interview context - be exhaustive):
  - Command to rewrite: `cmd/verifier/main.go`
  - Bootstrap contract: `internal/worker/worker.go` and `internal/worker/verify.go`
  - Reporter owner: `internal/worker/reporter.go`
  - Matching/verify primitives: `internal/worker/task.go`, `internal/worker/verify.go`
  - Template for config bootstrap: `internal/worker/config/config.go`, `internal/worker/worker.go`

  **Acceptance Criteria** (agent-executable only):
  - [ ] `cmd/verifier/main.go` calls `worker.InitVerifier()` before any use of `worker.Reporter` or verification work.
  - [ ] Reporter startup runs in a goroutine, so the file-walk verification path executes.
  - [ ] End-of-command reporter shutdown still calls `worker.Reporter.Close()` and waits for the returned waitgroup.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Verifier command reaches scan loop again
    Tool: Bash
    Steps: Build `./cmd/verifier`, then run it in a temp workdir with a controlled config/tree setup or a deliberately failing tree path; capture logs up to the first expected init/scan boundary.
    Expected: The command no longer blocks forever on synchronous `Reporter.Run(...)` and reaches the scan/bootstrap boundary before any controlled failure.
    Evidence: .sisyphus/evidence/task-6-cmd-verifier.txt

  Scenario: Reporter nil-start bug is gone
    Tool: Bash
    Steps: Search `cmd/verifier/main.go` and `internal/worker` for direct `worker.Reporter.Run(...)` usage that occurs before `worker.InitVerifier()`.
    Expected: No path remains where `Reporter` can be used before explicit verifier bootstrap.
    Evidence: .sisyphus/evidence/task-6-cmd-verifier-error.txt
  ```

  **Commit**: YES | Message: `refactor(verifier): align verifier command to bootstrap contract` | Files: `cmd/verifier/main.go`

- [x] 7. Rewrite `cmd/worker/main.go` around `InitWorker` and current runtime-owner capabilities

  **What to do**: Thin `cmd/worker/main.go` down to the startup shape the repo actually supports: signal/context setup, config bootstrap, tree load, aria2 client creation, `worker.InitWorker(aria2Client)`, goroutine startup for `worker.Reporter.Run(...)`, `worker.Verifier.Run(ctx)`, and `worker.Downloader.Run(ctx)`, plus orderly shutdown. Delete the stale branches that depended on nonexistent reporter/update/config-update APIs and unavailable local config fields. Keep only behavior that is already backed by current `internal/worker` capabilities.
  **Must NOT do**: Do not reintroduce updater/heartbeat/config-update scaffolding by expanding `internal/worker` to match the old main; do not add outer `if err != nil` checks after `InitWorker`; do not keep dead imports/functions such as `applyUpdateAndExit` if the refactor removes their only call sites.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - Reason: command-root rewrite with multiple dependency seams and lifecycle cleanup
  - Skills: none
  - Omitted: `playwright` - why not needed: no browser work

  **Parallelization**: Can Parallel: NO | Wave 3 | Blocks: `8,9` | Blocked By: `4,5`

  **References** (executor has NO interview context - be exhaustive):
  - Command to rewrite: `cmd/worker/main.go`
  - Worker bootstrap: `internal/worker/worker.go`
  - Verifier owner: `internal/worker/verify.go`
  - Downloader owner: `internal/worker/downloader.go`
  - Reporter owner: `internal/worker/reporter.go`
  - Config/tree bootstrap: `internal/worker/config/config.go`, `internal/worker/task.go`
  - Aria2 client creation: `internal/worker/aria2/aria2.go`
  - User intent note: `PLAN.md`

  **Acceptance Criteria** (agent-executable only):
  - [ ] `cmd/worker/main.go` no longer references `GetReporter`, `InitReporter`, `UpdateRequired`, `OnConfigUpdate`, `SendHeartbeat`, `Flush`, or protocol-only concurrency fields.
  - [ ] `cmd/worker/main.go` uses `worker.InitWorker(aria2Client)` and package globals directly after bootstrap.
  - [ ] Worker shutdown path still cancels context, stops reporter cleanly, and closes aria2 client without compile errors.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Worker command matches supported bootstrap model
    Tool: Bash
    Steps: Search `cmd/worker/main.go` for the stale symbol list, then build `./cmd/worker`.
    Expected: No stale startup symbols remain, and the worker command compiles against the new bootstrap contract.
    Evidence: .sisyphus/evidence/task-7-cmd-worker.txt

  Scenario: Worker command no longer depends on unsupported local config fields
    Tool: Bash
    Steps: Search `cmd/worker/main.go` for `cfg.VerifyConcurrency|cfg.DownloadConcurrency|cfg.Simultaneous` after the rewrite.
    Expected: The search returns no matches.
    Evidence: .sisyphus/evidence/task-7-cmd-worker-error.txt
  ```

  **Commit**: YES | Message: `refactor(worker): align worker command to bootstrap contract` | Files: `cmd/worker/main.go`

- [x] 8. Remove residual stale startup API expectations and banned patterns across worker files

  **What to do**: After both command roots compile, run a focused cleanup pass over `cmd/worker`, `cmd/verifier`, and `internal/worker` to remove any leftover stale wrappers, dead code, or bootstrap drift introduced by the refactor. This includes deleting unused imports, compatibility comments, old helper stubs, dead update/heartbeat code paths, and any remaining `Get*`/`sync.Once` startup residues.
  **Must NOT do**: Do not broaden this into general style cleanup outside the worker/verifier startup scope; do not touch unrelated packages.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: bounded cleanup driven by static search results
  - Skills: none
  - Omitted: `frontend-ui-ux` - why not needed: backend cleanup only

  **Parallelization**: Can Parallel: YES | Wave 3 | Blocks: `9` | Blocked By: `5,6,7`

  **References** (executor has NO interview context - be exhaustive):
  - Cleanup scope: `cmd/worker/main.go`, `cmd/verifier/main.go`, `internal/worker/worker.go`, `internal/worker/reporter.go`, `internal/worker/verify.go`, `internal/worker/downloader.go`
  - Broken symbols baseline: `cmd/worker/main.go` from Task 1 evidence
  - Startup intent: `PLAN.md`

  **Acceptance Criteria** (agent-executable only):
  - [ ] Static searches show no `GetReporter`, `GetDownloader`, or `sync.Once` usage anywhere in the worker startup scope.
  - [ ] Static searches show no residual stale worker-main startup symbols (`UpdateRequired`, `OnConfigUpdate`, `SendHeartbeat`, `Flush`) in the worker/verifier scope.
  - [ ] The edited files build cleanly without unused imports or dead helper leftovers.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Banned patterns are gone
    Tool: Bash
    Steps: Run repo searches across `cmd/worker`, `cmd/verifier`, and `internal/worker` for `GetReporter|GetDownloader|sync.Once|UpdateRequired|OnConfigUpdate|SendHeartbeat|Flush`; save outputs.
    Expected: No banned startup-path patterns remain in scope.
    Evidence: .sisyphus/evidence/task-8-cleanup.txt

  Scenario: Cleanup leaves no compile debris
    Tool: Bash
    Steps: Re-run `go build ./cmd/worker ./cmd/verifier ./internal/worker/...` immediately after cleanup and capture any unused-import or dead-code errors.
    Expected: Cleanup introduces no new compile failures.
    Evidence: .sisyphus/evidence/task-8-cleanup-error.txt
  ```

  **Commit**: YES | Message: `chore(worker): remove stale startup API remnants` | Files: worker/verifier startup files only

- [x] 9. Prove the new bootstrap contract with build, vet, and controlled fail-fast command runs

  **What to do**: Execute the final verification sweep for the refactor. Capture successful `go build` and `go vet` runs for the worker/verifier scope, then run both commands in controlled temp workdirs that intentionally miss required bootstrap inputs (for example absent `worker.json` or missing tree path) to prove failures now happen at explicit config/bootstrap boundaries instead of nil-pointer or stale-symbol paths.
  **Must NOT do**: Do not require a live server, real aria2 daemon, or human-driven interactive verification; do not add new test files as part of this proof.

  **Recommended Agent Profile**:
  - Category: `deep` - Reason: final proof requires careful negative-path verification without expanding scope
  - Skills: none
  - Omitted: `playwright` - why not needed: no UI/browser verification

  **Parallelization**: Can Parallel: YES | Wave 3 | Blocks: none | Blocked By: `5,6,7,8`

  **References** (executor has NO interview context - be exhaustive):
  - Verification targets: `cmd/worker/main.go`, `cmd/verifier/main.go`, `internal/worker/worker.go`, `internal/worker/reporter.go`, `internal/worker/verify.go`, `internal/worker/downloader.go`
  - Config bootstrap semantics: `internal/worker/config/config.go`, `internal/worker/worker.go`
  - Tree bootstrap semantics: `internal/worker/task.go`

  **Acceptance Criteria** (agent-executable only):
  - [ ] `go build ./cmd/worker ./cmd/verifier ./internal/worker/...` succeeds and evidence is saved.
  - [ ] `go vet ./cmd/worker ./cmd/verifier ./internal/worker/...` succeeds and evidence is saved.
  - [ ] Running each command in a controlled missing-config or missing-tree scenario produces explicit fatal/panic messages at the intended bootstrap boundary, with no nil-pointer/runtime-error evidence.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Final static verification passes
    Tool: Bash
    Steps: Run `go build ./cmd/worker ./cmd/verifier ./internal/worker/...` and `go vet ./cmd/worker ./cmd/verifier ./internal/worker/...`; save full outputs.
    Expected: Both commands succeed with zero compile/vet errors.
    Evidence: .sisyphus/evidence/task-9-final-verification.txt

  Scenario: Commands fail fast at bootstrap boundaries
    Tool: Bash
    Steps: In fresh temp workdirs, run `go run ./cmd/worker` and `go run ./cmd/verifier` with intentionally missing or invalid startup inputs, capture stderr/stdout, and search for `panic: runtime error` / `nil pointer`.
    Expected: Failures are explicit config/tree/bootstrap messages, and no nil-pointer/runtime-error signatures appear.
    Evidence: .sisyphus/evidence/task-9-final-verification-error.txt
  ```

  **Commit**: NO | Message: `n/a` | Files: `.sisyphus/evidence/*`

## Final Verification Wave (4 parallel agents, ALL must APPROVE)
- [x] F1. Plan Compliance Audit - oracle
- [x] F2. Code Quality Review - unspecified-high
- [x] F3. Real Manual QA - unspecified-high
- [x] F4. Scope Fidelity Check - deep

## Commit Strategy
- Prefer 2 implementation commits if the executor wants smaller checkpoints: first for `internal/worker` bootstrap/runtime-owner alignment (`2-5`), second for command-root rewrites plus verification cleanup (`6-9`).
- If the executor can keep the tree coherent through the full pass, a single commit after `9` is also acceptable.
- Do not commit intermediate states where `cmd/worker` or `cmd/verifier` fail to build.

## Success Criteria
- `cmd/worker` and `cmd/verifier` both compile against the same current `internal/worker` bootstrap contract.
- The worker startup path uses explicit globals and direct init, not accessor wrappers or `sync.Once`.
- Failures surface as clear init-site fatal/panic messages for missing config, bad tree path, or invalid bootstrap inputs.
- No stale worker-main API expectations remain in source after the refactor.
