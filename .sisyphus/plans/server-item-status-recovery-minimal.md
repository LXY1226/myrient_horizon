# Server Item Status Recovery Minimal Fix

## TL;DR
> **Summary**: Restore startup hydration so persisted `item_status` rows repopulate the in-memory `ServerTree`, and fix live conflict bubbling so `/api/stats/overview` and `/api/stats/tree` stay correct after startup and new reports.
> **Deliverables**:
> - Add all-row `item_status` scanner in DB layer
> - Add tree hydration/rebuild path at server startup
> - Fix conflict bubbling for live `ApplyReport` updates
> - Verify overview/tree stats against persisted state and live conflict changes
> **Effort**: Short
> **Parallel**: YES - 2 waves
> **Critical Path**: 1 -> 2 -> 3 -> 4

## Context
### Original Request
Correct the current server implementation so `item_status` data appears again in API stats, using the simplest implementation.

### Interview Summary
- `item_status` rows exist in Postgres.
- `/api/stats/overview` currently returns valid `total_*` counts but all mutable status counts are `0`.
- `/api/stats/tree?depth=1` likewise reflects empty in-memory status state.
- User suspects previous startup rehydration was removed; code still contains bulk-recovery hints.
- User also asked to validate the current bubbling behavior.

### Metis Review (gaps addressed)
- Hydration must rebuild `fileHashes`, not only `BestStatus`, or future conflict detection breaks.
- Recovery should stream rows and rebuild derived directory stats once, not mix DB reads into HTTP handlers.
- `bubbleDelta` must use `oldConflict` and `newConflict`; current `hasConflict` argument is insufficient.
- Scope stays minimal: no new APIs, no status-semantics redesign, no `claimed_*` revival.

## Work Objectives
### Core Objective
Make persisted `item_status` data visible again through existing tree-backed stats endpoints by restoring startup tree hydration and correcting conflict aggregation.

### Deliverables
- `internal/server/db.go`: unfiltered `item_status` row scanner
- `internal/server/tree.go`: tree hydration entrypoint and corrected conflict-aware bubbling
- `cmd/server/main.go`: startup call to hydrate tree after DB init and before serving traffic
- Minimal regression verification covering startup recovery and live conflict updates

### Definition of Done (verifiable conditions with commands)
- Starting the server against a DB containing historical `item_status` rows yields non-zero mutable stats in `GET /api/stats/overview` without waiting for a worker reconnect.
- `GET /api/stats/tree?depth=1` shows non-zero downloaded/verified/archived counts in at least one directory when the DB contains such rows.
- A new conflicting live report changes directory/root conflict counts without requiring full restart.
- `go test ./...` succeeds.

### Must Have
- Hydration happens after `server.InitDB()` and before HTTP serving begins.
- Hydration rebuilds file-level state from DB rows, then recomputes directory aggregates exactly once.
- Live `ApplyReport` keeps directory conflict counts accurate when conflict state changes but best status does not.
- Existing handlers remain tree-backed; no direct DB queries added to HTTP handlers.

### Must NOT Have (guardrails, AI slop patterns, scope boundaries)
- Must NOT add new REST endpoints.
- Must NOT redesign `protocol.TaskStatus` semantics or change precedence rules.
- Must NOT attempt to revive deprecated `claimed_*` tracking.
- Must NOT load all DB rows into an intermediate slice before applying them; stream rows through scanner callback.
- Must NOT touch unrelated worker, frontend, or WebSocket protocol contracts.

## Verification Strategy
> ZERO HUMAN INTERVENTION - all verification is agent-executed.
- Test decision: tests-after + Go stdlib `testing`, plus Bash-driven API verification
- QA policy: Every task includes agent-executed scenarios
- Evidence: `.sisyphus/evidence/task-{N}-{slug}.{ext}`

## Execution Strategy
### Parallel Execution Waves
> Target: 5-8 tasks per wave. <3 per wave (except final) = under-splitting.
> Extract shared dependencies as Wave-1 tasks for max parallelism.

Wave 1: DB scan foundation, tree hydration/rebuild implementation
Wave 2: startup integration, live bubbling correction, regression verification

### Dependency Matrix (full, all tasks)
- 1 blocks 2
- 2 blocks 3 and 4
- 3 and 4 block 5
- 5 blocks final verification wave

### Agent Dispatch Summary (wave -> task count -> categories)
- Wave 1 -> 2 tasks -> `quick`, `deep`
- Wave 2 -> 3 tasks -> `quick`, `quick`, `unspecified-low`
- Final verification -> 4 tasks -> `oracle`, `unspecified-high`, `unspecified-high`, `deep`

## TODOs
> Implementation + Test = ONE task. Never separate.
> EVERY task MUST have: Agent Profile + Parallelization + QA Scenarios.

- [x] 1. Add all-row `item_status` streaming scanner

  **What to do**: Add a new DB helper in `internal/server/db.go` that streams every `item_status` row through a callback, mirroring the existing callback style of `ScanAllItemStatusByWorker` but without worker filtering. Reuse the same scanned columns and row-to-`ItemStatus` mapping.
  **Must NOT do**: Must NOT introduce pagination complexity, ORM layers, or any API-facing changes.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: one-file, localized DB helper addition
  - Skills: `[]` - no special skill required
  - Omitted: [`git-master`] - no git operation involved

  **Parallelization**: Can Parallel: NO | Wave 1 | Blocks: 2 | Blocked By: none

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `internal/server/db.go:220` - existing `UpsertItemStatus` defines exact stored columns
  - Pattern: `internal/server/db.go:230` - existing iterator-style `ScanAllItemStatusByWorker` should be copied structurally
  - API/Type: `internal/server/db.go:212` - `ItemStatus` struct to populate

  **Acceptance Criteria** (agent-executable only):
  - [x] `go test ./...` passes after adding the helper
  - [x] `grep -n "func (s \*Store) ScanAllItemStatus" internal/server/db.go` finds the new helper
  - [x] The helper scans `worker_id, file_id, status, sha1, crc32` exactly once per row

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```text
  Scenario: Helper is present and compilable
    Tool: Bash
    Steps: Run `go test ./... > .sisyphus/evidence/task-1-db-scan.txt 2>&1`
    Expected: Exit code 0 and evidence file contains package test/build success output
    Evidence: .sisyphus/evidence/task-1-db-scan.txt

  Scenario: Scanner uses full-table query
    Tool: Bash
    Steps: Run `python - <<'PY' > .sisyphus/evidence/task-1-db-scan-query.txt
from pathlib import Path
text = Path('internal/server/db.go').read_text()
start = text.find('func (s *Store) ScanAllItemStatus(')
print(text[start:start+500])
PY`
    Expected: Output shows a query over `item_status` without `WHERE worker_id = $1`
    Evidence: .sisyphus/evidence/task-1-db-scan-query.txt
  ```

  **Commit**: NO | Message: `fix(server): add full item status scanner` | Files: `internal/server/db.go`

- [x] 2. Implement startup hydration and full directory stat recompute

  **What to do**: Add a tree-level recovery entrypoint in `internal/server/tree.go` that resets mutable file state, streams all persisted `item_status` rows, rebuilds per-file `BestStatus`, `HasConflict`, `ReportCount`, and `fileHashes`, then calls `recomputeAllStatsLocked()` once after the scan. Keep all work under the tree mutex or in an equivalent safe internal flow so HTTP handlers never observe partially rebuilt directory stats.
  **Must NOT do**: Must NOT query DB directly from handlers, must NOT rebuild via repeated public `ApplyReport` calls if that would double-count or depend on buggy bubbling, and must NOT touch `Total`/`TotalSize` semantics.

  **Recommended Agent Profile**:
  - Category: `deep` - Reason: derived-state rebuild with invariants and concurrency considerations
  - Skills: `[]` - repo-local logic only
  - Omitted: [`playwright`] - no browser work

  **Parallelization**: Can Parallel: NO | Wave 1 | Blocks: 3, 4 | Blocked By: 1

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `internal/server/tree.go:121` - current live update path and file-level mutable fields
  - Pattern: `internal/server/tree.go:222` - `recomputeAllStatsLocked()` is explicitly intended for bulk recovery
  - API/Type: `internal/server/tree.go:13` - `DirExt` fields that must be derived
  - API/Type: `internal/server/tree.go:29` - `FileExt` fields that must be rebuilt
  - Pattern: `internal/server/tree.go:159` - conflict semantics derive from comparing per-worker SHA1 values
  - Pattern: `internal/server/db.go:212` - hydrated row shape
  - Pattern: `internal/server/wsrpc.go:295` - live path currently updates DB then tree; hydration must restore equivalent tree state from persisted rows

  **Acceptance Criteria** (agent-executable only):
  - [x] Hydration path rebuilds `fileHashes`, `BestStatus`, `HasConflict`, and `ReportCount` from streamed DB rows
  - [x] Hydration path calls `recomputeAllStatsLocked()` exactly once after scan completion
  - [x] Hydration path tolerates out-of-range `file_id` values by skipping them safely
  - [x] `go test ./...` passes

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```text
  Scenario: Recovery path exists and uses bulk recompute
    Tool: Bash
    Steps: Run `python - <<'PY' > .sisyphus/evidence/task-2-tree-hydration.txt
from pathlib import Path
text = Path('internal/server/tree.go').read_text()
for key in ['recomputeAllStatsLocked()', 'fileHashes', 'ReportCount', 'HasConflict']:
    print(key, key in text)
PY`
    Expected: Output confirms all required recovery ingredients exist in tree hydration code
    Evidence: .sisyphus/evidence/task-2-tree-hydration.txt

  Scenario: Tree package still compiles after recovery logic
    Tool: Bash
    Steps: Run `go test ./internal/server/... > .sisyphus/evidence/task-2-tree-hydration-build.txt 2>&1`
    Expected: Exit code 0
    Evidence: .sisyphus/evidence/task-2-tree-hydration-build.txt
  ```

  **Commit**: NO | Message: `fix(server): restore tree hydration from persisted status` | Files: `internal/server/tree.go`, `internal/server/db.go`

- [x] 3. Reconnect hydration into server startup

  **What to do**: Update `cmd/server/main.go` so startup order becomes: load flatbuffer tree -> init DB -> hydrate tree from persisted `item_status` -> log initialized state -> start hub/server. If hydration fails, fail startup loudly instead of serving incorrect all-zero mutable stats.
  **Must NOT do**: Must NOT defer hydration until after HTTP serving begins; must NOT silently ignore hydration failure.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: single-file integration change
  - Skills: `[]` - straightforward wiring
  - Omitted: [`git-master`] - no git operation involved

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: 5 | Blocked By: 2

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `cmd/server/main.go:36` - current startup sequencing
  - Pattern: `cmd/server/main.go:51` - current post-init logging should reflect hydrated stats instead of empty mutable counts
  - Pattern: `internal/server/handler.go:205` - overview handler reads root stats from tree only
  - Pattern: `internal/server/handler.go:210` - tree handler reads tree nodes only

  **Acceptance Criteria** (agent-executable only):
  - [x] Server startup invokes hydration before logging final state and before serving HTTP
  - [x] Hydration failure aborts startup with a clear error path
  - [x] `go test ./...` passes

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```text
  Scenario: Startup path includes hydration call
    Tool: Bash
    Steps: Run `python - <<'PY' > .sisyphus/evidence/task-3-startup-wire.txt
from pathlib import Path
text = Path('cmd/server/main.go').read_text()
print('InitDB' in text)
print('Hydrate' in text or 'Recover' in text)
PY`
    Expected: Output indicates startup now includes a hydration/recovery call after DB init
    Evidence: .sisyphus/evidence/task-3-startup-wire.txt

  Scenario: Full workspace compiles after startup wiring
    Tool: Bash
    Steps: Run `go test ./... > .sisyphus/evidence/task-3-startup-build.txt 2>&1`
    Expected: Exit code 0
    Evidence: .sisyphus/evidence/task-3-startup-build.txt
  ```

  **Commit**: NO | Message: `fix(server): hydrate tree on startup` | Files: `cmd/server/main.go`

- [x] 4. Fix live conflict bubbling without changing status semantics

  **What to do**: Correct `ApplyReport`/`bubbleDelta` in `internal/server/tree.go` so live updates change directory/root `Conflict` counts when conflict state flips, even if `BestStatus` stays the same. Compute `oldConflict` before mutating hashes, compute `newConflict` after mutation, and only early-return when both status and conflict are unchanged.
  **Must NOT do**: Must NOT redesign `BestStatus` precedence, must NOT change handler payloads, and must NOT rely on periodic full recompute for live correctness.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: local bug fix once hydration contract is clear
  - Skills: `[]` - pure repo-local logic
  - Omitted: [`oracle`] - architecture decision already made

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: 5 | Blocked By: 2

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `internal/server/tree.go:122` - `ApplyReport` currently captures only `oldStatus`
  - Pattern: `internal/server/tree.go:178` - `bubbleDelta` currently ignores conflict changes and returns too early
  - Pattern: `internal/server/tree.go:224` - recompute logic shows intended meaning of `DirExt.Conflict`
  - Pattern: `internal/server/handler.go:333` - file-level conflict is already exposed in `/api/stats/dir`

  **Acceptance Criteria** (agent-executable only):
  - [x] Directory/root conflict counts change when a file transitions from non-conflicting to conflicting hashes
  - [x] Directory/root conflict counts change when a conflict is resolved
  - [x] Same-status conflict flips no longer get skipped by early return
  - [x] `go test ./...` passes

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```text
  Scenario: Code path compares old and new conflict states
    Tool: Bash
    Steps: Run `python - <<'PY' > .sisyphus/evidence/task-4-conflict-bubble.txt
from pathlib import Path
text = Path('internal/server/tree.go').read_text()
print('oldConflict' in text)
print('newConflict' in text)
print('Conflict++' in text or 's.Conflict++' in text)
print('Conflict--' in text or 's.Conflict--' in text)
PY`
    Expected: Output confirms explicit conflict delta handling exists
    Evidence: .sisyphus/evidence/task-4-conflict-bubble.txt

  Scenario: Tree package compiles with bubbling fix
    Tool: Bash
    Steps: Run `go test ./internal/server/... > .sisyphus/evidence/task-4-conflict-build.txt 2>&1`
    Expected: Exit code 0
    Evidence: .sisyphus/evidence/task-4-conflict-build.txt
  ```

  **Commit**: NO | Message: `fix(server): keep conflict counts in sync` | Files: `internal/server/tree.go`

- [x] 5. Verify recovered stats through existing APIs and restart scenario

  **What to do**: Add the smallest useful regression coverage. Prefer focused Go tests where practical for tree recovery/bubbling helpers, and supplement with Bash-driven verification that compares server API output against seeded persisted state. Verification must explicitly cover restart recovery and live conflict change behavior.
  **Must NOT do**: Must NOT build a large integration harness or introduce external test frameworks.

  **Recommended Agent Profile**:
  - Category: `unspecified-low` - Reason: mixed verification work, mostly glue and checks
  - Skills: `[]` - standard repo verification only
  - Omitted: [`playwright`] - API/server verification does not require browser automation

  **Parallelization**: Can Parallel: NO | Wave 2 | Blocks: Final verification wave | Blocked By: 3, 4

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `internal/server/handler.go:205` - overview API source
  - Pattern: `internal/server/handler.go:210` - tree API source
  - Pattern: `internal/server/tree.go:323` - expected overview payload fields
  - Pattern: `cmd/server/main.go:51` - startup log already prints aggregate stats and should reflect hydrated values
  - External: `https://pkg.go.dev/testing` - stdlib testing only

  **Acceptance Criteria** (agent-executable only):
  - [x] There is at least one regression test or deterministic scripted verification for startup hydration
  - [x] There is at least one regression test or deterministic scripted verification for conflict bubbling
  - [x] `GET /api/stats/overview` shows non-zero mutable counts when DB contains matching persisted rows
  - [x] `GET /api/stats/tree?depth=1` shows at least one non-zero mutable directory count when DB contains matching persisted rows
  - [x] `go test ./...` passes

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```text
  Scenario: Startup recovery populates overview stats
    Tool: Bash
    Steps: Start a local test server against a DB seeded with known `item_status` rows, then run `curl -s http://localhost:8099/api/stats/overview > .sisyphus/evidence/task-5-overview.json`
    Expected: `verified_files`, `downloaded_files`, or `archived_files` is non-zero and aligns with seeded persisted state; `total_files` remains populated
    Evidence: .sisyphus/evidence/task-5-overview.json

  Scenario: Tree endpoint reflects recovered state
    Tool: Bash
    Steps: Run `curl -s "http://localhost:8099/api/stats/tree?depth=1" > .sisyphus/evidence/task-5-tree.json`
    Expected: Response contains `directories` with at least one directory showing non-zero downloaded/verified/archived counts when seeded data exists
    Evidence: .sisyphus/evidence/task-5-tree.json
  ```

  **Commit**: NO | Message: `test(server): verify stats recovery and conflict bubbling` | Files: `internal/server/*`, `.sisyphus/evidence/*`

- [x] F1. Plan Compliance Audit - oracle
- [x] F2. Code Quality Review - unspecified-high
- [x] F3. Real Manual QA - unspecified-high (+ playwright if UI)
- [x] F4. Scope Fidelity Check - deep


## Commit Strategy
- Keep implementation uncommitted until all verification evidence is collected.
- If the user later requests a commit, use one minimal bug-fix commit covering DB scan, tree hydration, startup wiring, and conflict bubbling together.
- Recommended commit message: `fix(server): restore persisted status recovery`

## Success Criteria
- Existing stats endpoints expose persisted historical status again after restart without adding new APIs.
- Live reports keep conflict counts accurate after startup.
- Scope remains minimal and does not alter deprecated claim behavior or status semantics.
