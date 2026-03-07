# Backend Go Style Unification

## TL;DR
> **Summary**: Use the handwritten `worker` package as the canonical backend template and refactor `server` to align with its process-oriented structure, lifecycle management, state ownership, logging boundaries, and init/get/run conventions.
> **Deliverables**:
> - repo-wide backend style contract and static enforcement
> - `server` structure aligned to the `worker` package's runtime-oriented patterns
> - explicit worker-derived style contract covering lifecycle, state ownership, logging, and package boundaries
> - removal or isolation of server-side dead/commented/incomplete code that blocks alignment
> - final static verification evidence proving style convergence guardrails
> **Effort**: Large
> **Parallel**: YES - 3 waves
> **Critical Path**: 1 -> 2 -> 3/4/5 -> 6/7 -> 8

## Context
### Original Request
Explore how many backend code styles exist in the repository and create a path to unify them into one concise, clear, non-redundant style. Updated direction: treat the handwritten `worker` package as the desired form and align `server` to it.

### Interview Summary
- Scope is the full backend: `cmd/*`, `internal/server/*`, and `internal/worker/*`.
- The user wants architecture-level convergence included, not just formatting and comments.
- Verification stays at static/style/build-check level; adding test infrastructure is explicitly out of scope.
- The desired target is now explicitly the handwritten `worker` package, not the previous server-oriented baseline.
- The main implementation burden shifts to `server`; `worker` acts as the reference unless a small shared-contract change is needed.

### Metis Review (gaps addressed)
- Locked generated code out of manual style cleanup to avoid editing machine-generated files.
- Added an explicit baseline-verification task so style work does not hide pre-existing compile or structural defects.
- Narrowed architecture convergence to worker-style ownership/boundary cleanup only; no feature work, protocol redesign, or speculative abstractions.
- Added explicit dead-code and non-English comment cleanup so style drift is not preserved under a new formatter.

## Work Objectives
### Core Objective
Standardize backend architecture around the handwritten `worker` package's model by refactoring `server` to match its lifecycle-driven structure, initialization conventions, state ownership, logging boundaries, and package responsibilities.

### Deliverables
- One worker-derived backend style contract and enforcement baseline for handwritten Go code.
- `cmd/server` aligned to the lifecycle/orchestration shape already used by `cmd/worker`.
- `internal/server/*` aligned to worker-style ownership, initialization, logging, and runtime loop boundaries.
- `internal/worker/*` treated as reference code, with only minimal supporting changes allowed when needed to clarify the shared contract.
- Generated code explicitly excluded from manual style edits.

### Definition of Done (verifiable conditions with commands)
- `go build ./...` succeeds after the refactor.
- `gofmt -w` or equivalent formatting pass leaves no diff in backend Go files.
- `goimports` or equivalent import normalization leaves no diff in backend Go files.
- repo-wide backend search finds no Chinese comments in handwritten Go files.
- repo-wide backend search finds no large commented-out code blocks preserved as dead implementation.
- server entrypoints and packages follow the worker-derived ownership and lifecycle rules in this plan.

### Must Have
- Use `cmd/worker/main.go`, `internal/worker/worker.go`, `internal/worker/reporter.go`, and related worker files as the structural reference.
- Treat `cmd/worker` as the desired entrypoint shape: lifecycle setup, signal handling, dependency init, goroutine startup, and shutdown orchestration.
- Treat worker-style package ownership as acceptable: `Init*`, `Get*`, `Run`, package-level orchestrators, and runtime state are allowed when they are explicit and singular rather than ad hoc.
- Standardize error handling by boundary the way worker does it: process/bootstrap failures may terminate early, long-running loops log and continue/retry, protocol/state helpers return actionable values to their immediate caller.
- Remove commented-out implementation blocks and incomplete fragments from `server`; keep `worker` as the reference rather than forcing it toward the old server pattern.
- Exclude generated code from manual style changes unless regeneration is part of the implementation path.

### Must NOT Have (guardrails, AI slop patterns, scope boundaries)
- Must NOT add new features, protocol changes, schema changes, or business logic redesign.
- Must NOT invent framework-heavy dependency injection or broad interface layers "for future flexibility".
- Must NOT manually style-edit generated sources such as `pkg/myrienttree/MyrientTree/Node.go`.
- Must NOT preserve incomplete code by merely reformatting it.
- Must NOT force `server` into repository/handler layering that conflicts with the established worker lifecycle model.
- Must NOT treat all package-level state as automatically invalid; only ad hoc or duplicated state is forbidden.

## Verification Strategy
> ZERO HUMAN INTERVENTION - all verification is agent-executed.
- Test decision: none for new test infra; use static verification only.
- QA policy: Every task includes agent-executed happy-path and failure/edge scenarios.
- Evidence: `.sisyphus/evidence/task-{N}-{slug}.{ext}`

## Execution Strategy
### Parallel Execution Waves
> Target: 5-8 tasks per wave. <3 per wave (except final) = under-splitting.
> Extract shared dependencies as Wave-1 tasks for max parallelism.

Wave 1: baseline/style contract/foundation (`1`, `2`, `3`, `4`, `5`)
Wave 2: package convergence (`6`, `7`, `8`)
Wave 3: static verification and cleanup (`9`, `10`)

### Dependency Matrix (full, all tasks)
- `1` blocks `2-10`
- `2` blocks `6-10`
- `3` blocks `6-10`
- `4` blocks `6-10`
- `5` blocks `8-10`
- `6` blocked by `1-4`
- `7` blocked by `1-4`
- `8` blocked by `1-5`
- `9` blocked by `6-8`
- `10` blocked by `9`

### Agent Dispatch Summary (wave -> task count -> categories)
- Wave 1 -> 5 tasks -> `quick`, `unspecified-high`
- Wave 2 -> 3 tasks -> `unspecified-high`, `deep`
- Wave 3 -> 2 tasks -> `quick`, `deep`

## TODOs
> Implementation + Test = ONE task. Never separate.
> EVERY task MUST have: Agent Profile + Parallelization + QA Scenarios.

- [ ] 1. Capture baseline, worker template rules, and server divergence map

  **What to do**: Record the current backend state before edits; enumerate handwritten vs generated Go files; lock the target style to the worker package's repeated patterns (`cmd/worker/main.go`, `internal/worker/worker.go`, `internal/worker/reporter.go`, `internal/worker/downloader.go`); create a short implementer-facing contract explaining exactly how `server` diverges today.
  **Must NOT do**: Do not edit application code in this task beyond creating the style-contract artifact and evidence; do not start refactoring before the baseline is written down.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: mostly inventory, guardrails, and static checks
  - Skills: none
  - Omitted: `frontend-ui-ux` - why not needed: backend-only planning execution

  **Parallelization**: Can Parallel: NO | Wave 1 | Blocks: `2,3,4,5,6,7,8,9,10` | Blocked By: none

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `cmd/worker/main.go` - target process lifecycle and orchestration template
  - Pattern: `internal/worker/worker.go` - target bootstrap/init pattern
  - Pattern: `internal/worker/reporter.go` - target long-running loop, reconnect, and runtime logging boundaries
  - Pattern: `internal/worker/downloader.go` - target stateful runtime owner shape, even where implementation is incomplete
  - Divergence: `internal/server/handler.go` - request/response-centric style diverging from worker package shape
  - Divergence: `internal/server/wsrpc.go` - stateful server runtime that should be aligned to worker-style ownership
  - External: `https://go.dev/doc/effective_go` - idiomatic Go baseline
  - External: `https://go.dev/s/comments` - Go code review comments
  - External: `https://google.github.io/styleguide/go/decisions` - naming, errors, comments, repetition

  **Acceptance Criteria** (agent-executable only):
  - [ ] A backend style contract exists in the implementation workspace and explicitly names handwritten-scope vs generated-scope files.
  - [ ] Baseline evidence captures current `go build ./...` status before refactoring.
  - [ ] Baseline evidence captures searches for globals, Chinese comments, and commented-out code hotspots.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Baseline inventory captured
    Tool: Bash
    Steps: Run `go build ./...`; run repo searches for `var .*\*`, Chinese characters, and large comment blocks under `cmd/`, `internal/`, `pkg/`; save outputs.
    Expected: Evidence files exist with current baseline status and explicit generated-file exceptions.
    Evidence: .sisyphus/evidence/task-1-baseline.txt

  Scenario: Generated-file exclusion validated
    Tool: Bash
    Steps: Search for generated candidates such as `pkg/myrienttree/MyrientTree/Node.go` and verify they are marked excluded from manual style edits.
    Expected: Exclusion list includes generated files and no handwritten backend file is omitted.
    Evidence: .sisyphus/evidence/task-1-baseline-error.txt
  ```

  **Commit**: YES | Message: `refactor(style): define backend Go style contract` | Files: `.sisyphus/*`, style contract artifact, optional lint config scaffold

- [ ] 2. Add static enforcement for the worker-derived style

  **What to do**: Introduce repo-level static tooling for handwritten Go style enforcement: `gofmt`, `goimports`, and a minimal Go linter configuration focused on naming, comments, dead code, and ignored errors. Keep the config narrow and aligned to the chosen style contract.
  **Must NOT do**: Do not add test framework setup; do not enable noisy rules that would force unrelated rewrites outside this plan.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: config-only, low-complexity foundation work
  - Skills: none
  - Omitted: `playwright` - why not needed: no UI/browser verification

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: `6,7,8,9,10` | Blocked By: `1`

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `go.mod` - confirms Go module root and backend language
  - Pattern: `cmd/worker/main.go` - source of target lifecycle shape
  - Pattern: `internal/worker/worker.go` - source of target bootstrap style
  - Pattern: `internal/worker/reporter.go` - source of target runtime loop/logging style
  - Research: `https://google.github.io/styleguide/go/decisions` - rule source for naming/comments/errors
  - Research: `https://go.dev/s/comments` - rule source for `gofmt`, comments, error strings, ignored errors

  **Acceptance Criteria** (agent-executable only):
  - [ ] Lint/format config exists and is scoped to handwritten backend Go code.
  - [ ] The config documents or encodes generated-file exclusions.
  - [ ] Running the chosen lint/format commands produces deterministic output.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Enforcement commands run on backend scope
    Tool: Bash
    Steps: Run the configured formatting and lint commands against backend Go paths only; capture outputs.
    Expected: Commands run successfully or produce a bounded actionable violation list aligned with the contract.
    Evidence: .sisyphus/evidence/task-2-static-enforcement.txt

  Scenario: Generated-file exclusion holds
    Tool: Bash
    Steps: Run the linter/formatter in a way that would include generated files if exclusions fail; inspect output paths.
    Expected: Generated files such as `pkg/myrienttree/MyrientTree/Node.go` are not targeted for manual style remediation.
    Evidence: .sisyphus/evidence/task-2-static-enforcement-error.txt
  ```

  **Commit**: YES | Message: `chore(style): add backend Go static enforcement` | Files: lint/format config files, docs if needed

- [ ] 3. Align `cmd/server` to the `cmd/worker` composition-root model

  **What to do**: Refactor `cmd/server/main.go` so it follows the same lifecycle orchestration shape as `cmd/worker/main.go`: context creation, signal handling, dependency init, process startup, goroutine supervision where needed, and controlled shutdown. Keep `cmd/worker` as the template and touch it only if the shared contract needs clarifying comments or naming alignment.
  **Must NOT do**: Do not redesign protocols or runtime behavior; do not force `cmd/worker` to match `cmd/server`.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - Reason: cross-file boundary cleanup with behavior sensitivity
  - Skills: none
  - Omitted: `git-master` - why not needed: no git operation required during implementation

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: `6,7,8,9,10` | Blocked By: `1`

  **References** (executor has NO interview context - be exhaustive):
  - Template: `cmd/worker/main.go`
  - Target: `cmd/server/main.go`
  - Supporting: `internal/worker/worker.go`
  - Supporting: `internal/worker/reporter.go`
  - Supporting: `cmd/verifier/main.go` - secondary check for worker-family entrypoint conventions

  **Acceptance Criteria** (agent-executable only):
  - [ ] `cmd/server/main.go` follows the same lifecycle/orchestration contract as `cmd/worker/main.go`.
  - [ ] Signal handling, lifecycle control, and dependency setup are still present and compile.
  - [ ] Formatting/linting passes on `cmd/server/main.go` and any minimally touched worker entrypoints.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Entrypoints still build after thinning
    Tool: Bash
    Steps: Run `go build ./cmd/server ./cmd/worker ./cmd/verifier` after refactor.
    Expected: Server command builds and worker-family commands still build unchanged or with only bounded contract-support edits.
    Evidence: .sisyphus/evidence/task-3-cmd-roots.txt

  Scenario: Entrypoints do not accumulate banned patterns
    Tool: Bash
    Steps: Search `cmd/server/main.go` for request-specific business branches, and compare its structure against `cmd/worker/main.go`.
    Expected: `cmd/server` now reads like a worker-style process entrypoint rather than a bespoke server bootstrap.
    Evidence: .sisyphus/evidence/task-3-cmd-roots-error.txt
  ```

  **Commit**: YES | Message: `refactor(server): align server entrypoint to worker lifecycle` | Files: `cmd/server/main.go`, minimally touched worker entrypoints if any

- [ ] 4. Declare generated-code and shared-package boundaries

  **What to do**: Separate handwritten shared packages from generated code responsibilities. Explicitly exclude generated flatbuffer output from style work, while normalizing handwritten packages like `pkg/protocol/messages.go` and `pkg/myrienttree/tree.go` to the same comment, naming, and error-handling standards where applicable.
  **Must NOT do**: Do not hand-edit generated code unless regeneration is the only safe route and is included in the implementation path.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: mostly scope locking and limited shared-package cleanup
  - Skills: none
  - Omitted: `frontend-ui-ux` - why not needed: no frontend relevance

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: `8,9,10` | Blocked By: `1`

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `pkg/protocol/messages.go` - handwritten shared protocol package
  - Pattern: `pkg/myrienttree/tree.go` - handwritten shared generic package
  - Exclusion: `pkg/myrienttree/MyrientTree/Node.go` - generated file to quarantine from manual style edits
  - Guidance: `https://go.dev/doc/effective_go` - naming and package conventions

  **Acceptance Criteria** (agent-executable only):
  - [ ] Generated vs handwritten shared Go files are explicitly documented.
  - [ ] Handwritten shared packages match the same naming/comment/error expectations as the worker-derived contract.
  - [ ] Generated files remain untouched unless regeneration is explicitly performed and documented.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Shared-package boundary enforced
    Tool: Bash
    Steps: Diff or inspect modified files after this task and compare against the generated-file exclusion list.
    Expected: Only handwritten shared files are modified.
    Evidence: .sisyphus/evidence/task-4-shared-boundaries.txt

  Scenario: Generated file remains exempt
    Tool: Bash
    Steps: Check whether `pkg/myrienttree/MyrientTree/Node.go` changed.
    Expected: File is unchanged unless a documented regeneration step exists.
    Evidence: .sisyphus/evidence/task-4-shared-boundaries-error.txt
  ```

  **Commit**: YES | Message: `refactor(pkg): lock shared package style boundaries` | Files: shared handwritten packages, exclusion docs/config

- [ ] 5. Define the ownership migration path for backend globals and singletons

  **What to do**: Inventory backend globals and runtime state and decide, file by file, whether each server-side instance should be reshaped to match worker-style ownership (`Init*`/`Get*`/`Run`, single runtime owner, package-level orchestrator) or retained as a documented exception. Use worker state management as the reference, not the previous anti-global rule.
  **Must NOT do**: Do not force full-blown dependency injection across the whole repo; do not erase worker-style package ownership just because it is package-scoped.

  **Recommended Agent Profile**:
  - Category: `deep` - Reason: architecture convergence with risk around init order and concurrency
  - Skills: none
  - Omitted: `playwright` - why not needed: backend-only task

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: `6,7,8,9,10` | Blocked By: `1`

  **References** (executor has NO interview context - be exhaustive):
  - Global: `internal/server/db.go` - `DB`
  - Global: `internal/server/tree.go` - `Tree`
  - Global: `internal/worker/config/config.go` - `Global`
  - Global: `internal/worker/reporter.go` - `Reporter`
  - Global: `internal/worker/task.go` - `iTree`
  - Global: `internal/worker/downloader.go` - downloader globals/channels
  - Global: `internal/worker/verify.go` - `verifyTaskCh`
  - Template: `internal/worker/worker.go`
  - Template: `internal/worker/reporter.go`
  - Template: `internal/worker/downloader.go`
  - Oracle guardrail source: architecture review from this planning session

  **Acceptance Criteria** (agent-executable only):
  - [ ] Every current server global/runtime owner is assigned one explicit disposition: align to worker pattern, retain as documented exception, or remove.
  - [ ] No later task introduces ad hoc package state outside the worker-style ownership contract.
  - [ ] The chosen ownership path is reflected in server task implementations.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Global inventory completed
    Tool: Bash
    Steps: Search backend Go files for package-level mutable globals and compare results to the ownership map.
    Expected: Every mutable global found in search appears in the ownership map with a disposition.
    Evidence: .sisyphus/evidence/task-5-global-ownership.txt

  Scenario: No new globals introduced
    Tool: Bash
    Steps: After implementing later tasks, rerun the global-state search and diff against the baseline.
    Expected: No new mutable package globals appear.
    Evidence: .sisyphus/evidence/task-5-global-ownership-error.txt
  ```

  **Commit**: YES | Message: `refactor(style): define backend ownership migration path` | Files: backend packages touched for ownership convergence, docs/config if needed

- [ ] 6. Refactor `internal/server/handler.go` to match worker-style package boundaries

  **What to do**: Refactor `internal/server/handler.go` so it stops acting like an isolated HTTP/controller layer and instead fits the worker-style package model: one package-owned runtime surface, consistent helper naming, explicit state access, logging at runtime boundaries, and no commented-out or incomplete fragments.
  **Must NOT do**: Do not change API routes, response schemas, or business semantics unless required to complete an obviously broken/incomplete path already present.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - Reason: medium-risk handler cleanup with existing inconsistencies
  - Skills: none
  - Omitted: `git-master` - why not needed: no git work required

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: `9,10` | Blocked By: `1,2,3,5`

  **References** (executor has NO interview context - be exhaustive):
  - Template: `internal/worker/worker.go`
  - Template: `internal/worker/reporter.go`
  - Template: `cmd/worker/main.go`
  - File to refactor: `internal/server/handler.go`
  - Related dependency: `internal/server/wsrpc.go`
  - Related dependency: `pkg/protocol/messages.go`
  - Known issue area: `internal/server/handler.go:167` incomplete line fragment
  - Known helper: `internal/server/handler.go:339` `writeJSON`

  **Acceptance Criteria** (agent-executable only):
  - [ ] `internal/server/handler.go` contains no commented-out implementation blocks.
  - [ ] Handler flow matches the worker-derived package contract rather than a separate controller dialect.
  - [ ] The file builds and matches repo-wide formatting/lint expectations.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Server handler compiles cleanly
    Tool: Bash
    Steps: Run `go build ./cmd/server` and capture output after the handler refactor.
    Expected: Server command builds successfully.
    Evidence: .sisyphus/evidence/task-6-server-handler.txt

  Scenario: Handler dead-code cleanup validated
    Tool: Bash
    Steps: Search `internal/server/handler.go` for large commented blocks and TODO-style placeholders tied to implementation.
    Expected: No commented-out implementation remains; only necessary explanatory comments survive.
    Evidence: .sisyphus/evidence/task-6-server-handler-error.txt
  ```

  **Commit**: YES | Message: `refactor(server): align HTTP surface to worker package contract` | Files: `internal/server/handler.go`, tightly related helper files only

- [ ] 7. Align server runtime ownership and WebSocket/state packages to worker patterns

  **What to do**: Refactor `internal/server/wsrpc.go`, `internal/server/tree.go`, and any related `internal/server/db.go` touchpoints so server runtime ownership looks like worker runtime ownership: one clear state owner, explicit lifecycle transitions, loop-oriented logging, and package-level coordination where deliberate. Resolve broken or inconsistent method signatures uncovered during this cleanup.
  **Must NOT do**: Do not redesign the worker protocol, database schema, or tree semantics.

  **Recommended Agent Profile**:
  - Category: `deep` - Reason: concurrency/state-sensitive package convergence
  - Skills: none
  - Omitted: `frontend-ui-ux` - why not needed: backend-only task

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: `9,10` | Blocked By: `1,2,3,5`

  **References** (executor has NO interview context - be exhaustive):
  - Template: `internal/worker/reporter.go`
  - Template: `internal/worker/downloader.go`
  - Template: `internal/worker/worker.go`
  - File to refactor: `internal/server/wsrpc.go`
  - Related ownership file: `internal/server/tree.go`
  - Related caller: `internal/server/handler.go`
  - Shared protocol: `pkg/protocol/messages.go`
  - Known inconsistency: `internal/server/wsrpc.go` contains mixed method signatures and mutex usage that must be reconciled

  **Acceptance Criteria** (agent-executable only):
  - [ ] `internal/server/wsrpc.go` compiles with coherent method signatures and worker-style runtime ownership.
  - [ ] Server-side globals/state owners follow the ownership map from task `5`.
  - [ ] Concurrency primitives are retained only where they serve a documented worker-style runtime boundary.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Server packages build after ownership cleanup
    Tool: Bash
    Steps: Run `go build ./cmd/server` and `go build ./internal/server/...` if package layout allows.
    Expected: Server code builds successfully.
    Evidence: .sisyphus/evidence/task-7-server-state.txt

  Scenario: Ownership map respected
    Tool: Bash
    Steps: Search `internal/server/*.go` for package globals and compare with allowed exceptions from task `5`.
    Expected: Only documented exceptions remain.
    Evidence: .sisyphus/evidence/task-7-server-state-error.txt
  ```

  **Commit**: YES | Message: `refactor(server): align runtime ownership to worker pattern` | Files: `internal/server/wsrpc.go`, `internal/server/tree.go`, `internal/server/db.go`, related callers

- [ ] 8. Apply minimal shared-contract adjustments around the worker reference

  **What to do**: Touch `internal/worker/*` only where necessary to make the shared backend contract explicit for server alignment - for example, clarifying naming, exposing the intended `Init*`/`Get*`/`Run` conventions, or adding narrowly scoped comments/config support. Keep worker behavior and structure intact.
  **Must NOT do**: Do not refactor worker toward server; do not broaden worker cleanup into an independent rewrite.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: bounded support work around the reference implementation only
  - Skills: none
  - Omitted: `playwright` - why not needed: no UI/browser work

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: `9,10` | Blocked By: `1,2,4,5`

  **References** (executor has NO interview context - be exhaustive):
  - Template: `internal/worker/worker.go`
  - Template: `internal/worker/reporter.go`
  - Template: `internal/worker/downloader.go`
  - Supporting package: `internal/worker/config/config.go`
  - Related shared types: `pkg/protocol/messages.go`
  - Related server targets: `internal/server/handler.go`, `internal/server/wsrpc.go`, `cmd/server/main.go`

  **Acceptance Criteria** (agent-executable only):
  - [ ] Any worker file touched in this task changes only to clarify or stabilize the shared contract.
  - [ ] Worker command behavior and lifecycle shape remain intact.
  - [ ] Touched worker files compile and satisfy repo formatting/linting rules.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Worker reference remains stable
    Tool: Bash
    Steps: Run `go build ./cmd/worker ./cmd/verifier` after any supporting worker edits.
    Expected: Worker and verifier commands build successfully with no behavior-oriented rewrite implied by the diff.
    Evidence: .sisyphus/evidence/task-8-worker-core.txt

  Scenario: Worker edits stay bounded
    Tool: Bash
    Steps: Inspect the touched worker files and compare them against the task scope.
    Expected: Changes are contract-supporting only; no broad worker rewrite appears.
    Evidence: .sisyphus/evidence/task-8-worker-core-error.txt
  ```

  **Commit**: YES | Message: `chore(worker): clarify reference contract for server alignment` | Files: minimally touched `internal/worker/*` files only

- [ ] 9. Sweep remaining server divergences and align shared runtime behavior

  **What to do**: After the main server refactors, do one bounded sweep across `internal/server/*`, `cmd/server/*`, and shared handwritten packages for any leftover divergences from the worker contract: naming mismatches, logging prefix mismatches, lifecycle gaps, state-owner inconsistencies, or dead/incomplete code that still reads unlike worker.
  **Must NOT do**: Do not pull worker infrastructure details like aria2 transport concerns into server just for cosmetic consistency.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - Reason: final convergence sweep across server and shared runtime surfaces
  - Skills: none
  - Omitted: `frontend-ui-ux` - why not needed: backend-only task

  **Parallelization**: Can Parallel: NO | Wave 3 | Blocks: `10` | Blocked By: `5,6,7,8`

  **References** (executor has NO interview context - be exhaustive):
  - Template: `cmd/worker/main.go`
  - Template: `internal/worker/worker.go`
  - Template: `internal/worker/reporter.go`
  - Target: `internal/server/handler.go`
  - Target: `internal/server/wsrpc.go`
  - Target: `cmd/server/main.go`
  - Related shared types: `pkg/protocol/messages.go`, `pkg/myrienttree/tree.go`
  - Shared protocol: `pkg/protocol/messages.go`
  - Known style issues: request/controller-centric flow in `internal/server/handler.go`; inconsistent runtime ownership in `internal/server/wsrpc.go`; direct bootstrap shape mismatch in `cmd/server/main.go`

  **Acceptance Criteria** (agent-executable only):
  - [ ] Remaining server and shared-package code reads as part of the same worker-derived runtime model.
  - [ ] Logging, ownership, and lifecycle responsibilities are consistent across `cmd/server` and `internal/server`.
  - [ ] Server command builds successfully after the convergence sweep.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Server convergence sweep passes build
    Tool: Bash
    Steps: Run `go build ./cmd/server` and capture output after the final sweep.
    Expected: Server command builds successfully.
    Evidence: .sisyphus/evidence/task-9-worker-pipeline.txt

  Scenario: Divergence sweep catches leftovers
    Tool: Bash
    Steps: Search `internal/server/*.go` and `cmd/server/*.go` for leftover controller-specific/commented-out patterns that do not exist in worker.
    Expected: No significant server-only style dialect remains undocumented.
    Evidence: .sisyphus/evidence/task-9-worker-pipeline-error.txt
  ```

  **Commit**: YES | Message: `refactor(server): finish worker-style convergence sweep` | Files: `internal/server/*`, `cmd/server/*`, minimally touched shared handwritten packages

- [ ] 10. Run repo-wide backend static verification and exception sweep

  **What to do**: Execute the final backend-only formatting, import normalization, linting, build, and rule-based searches. Confirm all documented exceptions are intentional, generated-file exclusions still hold, and the backend now matches one coherent style contract.
  **Must NOT do**: Do not slip in new refactors unrelated to failing checks; only fix items necessary for the declared contract.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: final verification and bounded cleanup only
  - Skills: none
  - Omitted: `playwright` - why not needed: backend verification task

  **Parallelization**: Can Parallel: NO | Wave 3 | Blocks: none | Blocked By: `2,6,7,8,9`

  **References** (executor has NO interview context - be exhaustive):
  - Contract source: task `1`
  - Enforcement source: task `2`
  - Server targets: `internal/server/handler.go`, `internal/server/wsrpc.go`, `internal/server/db.go`, `internal/server/tree.go`
  - Worker reference targets: `cmd/worker/main.go`, `internal/worker/worker.go`, `internal/worker/reporter.go`, `internal/worker/downloader.go`, `internal/worker/config/config.go`
  - Shared handwritten targets: `pkg/protocol/messages.go`, `pkg/myrienttree/tree.go`
  - Exclusion target: `pkg/myrienttree/MyrientTree/Node.go`

  **Acceptance Criteria** (agent-executable only):
  - [ ] Formatting, imports, lints, and `go build ./...` all pass for backend scope.
  - [ ] Searches report no blocking commented-out implementation in handwritten backend Go files, especially under `server`.
  - [ ] State-owner exceptions match the ownership map and no new ad hoc globals exist outside the worker-style contract.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Final backend static verification passes
    Tool: Bash
    Steps: Run the agreed formatting/import/lint commands and `go build ./...`; store outputs.
    Expected: All commands pass and evidence is saved.
    Evidence: .sisyphus/evidence/task-10-final-verification.txt

  Scenario: Exception sweep remains bounded
    Tool: Bash
    Steps: Run searches for Chinese characters, commented-out implementation, mutable package globals, and generated-file modifications.
    Expected: Only documented exceptions remain; generated files are untouched unless explicitly regenerated.
    Evidence: .sisyphus/evidence/task-10-final-verification-error.txt
  ```

  **Commit**: YES | Message: `chore(style): finalize backend static verification` | Files: backend Go files touched by final cleanup, config/evidence artifacts

## Final Verification Wave (4 parallel agents, ALL must APPROVE)
- [ ] F1. Plan Compliance Audit - oracle
- [ ] F2. Code Quality Review - unspecified-high
- [ ] F3. Real Manual QA - unspecified-high
- [ ] F4. Scope Fidelity Check - deep

## Commit Strategy
- Create one commit per completed wave.
- Preferred messages:
  - `refactor(style): establish backend Go style contract`
  - `refactor(server): align server lifecycle to worker pattern`
  - `refactor(server): align handlers and runtime ownership to worker pattern`
  - `chore(worker): clarify reference contract for server alignment`
  - `chore(style): finalize backend static verification`

## Success Criteria
- Backend handwritten Go code reads as one system, not multiple local dialects.
- Server now reads like it belongs to the same family as worker: same naming, lifecycle, logging, and ownership expectations.
- No blocking dead/commented-out implementation remains in handwritten server Go files.
- No new ad hoc globals/state owners are introduced outside the worker-style contract.
- Static verification passes and evidence files exist for every task.
