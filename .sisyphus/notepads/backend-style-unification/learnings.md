# Backend Style Unification - Learnings

## Task 1: Baseline Capture (2026-03-07)

### Files Created
- `.sisyphus/evidence/task-1-baseline.txt` - Build output
- `.sisyphus/evidence/task-1-baseline-error.txt` - Error summary
- `.sisyphus/contract/backend-style.md` - Style contract (updated)

### Build Status
**BROKEN** - 5 compilation errors preventing any style work:
1. `worker.InitReporter` - undefined
2. `worker.GetReporter` - undefined
3. `worker.InitVerifier` - undefined
4. `downloader.Run(ctx)` - signature mismatch (wants 0 args)
5. `worker.GetDiskFreeGB` - undefined

### Generated Files (Verified Exclusions)
| File | Generator | Status |
|------|-----------|--------|
| `pkg/myrienttree/MyrientTree/Node.go` | FlatBuffers | DO NOT EDIT - confirmed |

### Handwritten vs Generated Boundary
- **Handwritten**: `cmd/`, `internal/`, most of `pkg/`
- **Generated**: Only `pkg/myrienttree/MyrientTree/*.go`
- **Protocol**: `pkg/protocol/*.go` - handwritten but stable

### Worker Template Patterns (Target Style)
1. **cmd/worker/main.go**: Clean lifecycle with context/signal handling
2. **internal/worker/worker.go**: Bootstrap pattern with documented steps
3. **internal/worker/reporter.go**: Long-running loop with backoff
4. **internal/worker/downloader.go**: sync.Once singleton pattern

### Key Worker Pattern Rules
- Function comments explain pattern/contract, not just behavior
- Component prefix in all log messages: `"reporter: message"`
- Fatal errors: `log.Fatalf("descriptive: %v", err)`
- Exponential backoff: `backoff = min(backoff*2, maxBackoff)`
- Graceful shutdown: `Close()` returns `*sync.WaitGroup`

### Server Divergence Summary
| File | Primary Issues |
|------|----------------|
| `internal/server/db.go` | Global `DB` without sync.Once, no Init/Get pattern |
| `internal/server/tree.go` | Global `Tree` without sync.Once |
| `internal/server/handler.go` | Dead code comments (CORS, latency), minimal docs |
| `internal/server/wsrpc.go` | Hardcoded message strings, missing pattern docs |

### Issues Inventory
**Global Variables (9 total)**:
- `internal/worker/verify.go:16` - `verifyTaskCh`
- `internal/worker/reporter.go:33` - `Reporter`
- `internal/worker/task.go:31` - `iTree`
- `internal/server/tree.go:46` - `Tree`
- `internal/server/db.go:65` - `DB`
- `internal/worker/downloader.go:13-15` - 3 channel globals

**Chinese Comments (2 locations)**:
- `internal/worker/task.go:13` - `// 全局文件索引`
- `internal/worker/task.go:14` - `// 本地路径（最终的绝对路径）`

**Dead Code Blocks (6 locations)**:
- `downloader.go:19-30` - 12 line TODO block
- `downloader.go:91-132` - 42 line commented Run()
- `downloader.go:164-171` - 8 line commented function
- `handler.go:28-36` - CORS comments
- `handler.go:38-46` - Latency logging comments

### Critical Path for Tasks 2-10
**Task 2 (Build Fix) is BLOCKING** - Cannot proceed with style work until:
1. Worker API functions are implemented
2. Downloader.Run signature is fixed

**Recommended Order**:
1. Task 2: Fix build errors (add missing functions)
2. Task 3: Remove dead code blocks
3. Task 4: Translate Chinese comments
4. Task 5-6: Align server handler.go
5. Task 7-8: Align server wsrpc.go
6. Task 9: Standardize globals
7. Task 10: Final verification

### Search Verification Results
All searches confirmed:
- Globals found with `grep -r "var .*"` - correctly excluded generated files
- Chinese characters found only in handwritten files
- Large comments identified in handwritten code only
- `pkg/myrienttree/MyrientTree/Node.go` properly excluded from all analysis

## Task 2: Static Enforcement (2026-03-07)

### Configuration Created

**`.golangci.yml`** - Comprehensive lint configuration:
- Scope: Handwritten code only (cmd/, internal/)
- Generated file exclusions: pkg/myrienttree/MyrientTree/, pkg/protocol/
- Enabled linters (narrow selection):
  - Code quality: gosimple, govet, ineffassign, staticcheck, unused
  - Style: gocritic, gofmt, goimports, misspell, revive
  - Errors: errcheck, errorlint
- Excluded deprecated linters: deadcode, structcheck, varcheck (replaced by 'unused')

### Tools Installed
- `./bin/goimports` - Import formatting with local prefix support
- `./bin/golangci-lint` v1.64.8 - Full linting suite

### Tool Execution Results
| Tool | Status | Notes |
|------|--------|-------|
| go fmt | ✓ PASS | No formatting changes needed |
| goimports | ✓ PASS | No import changes needed |
| go vet | ✗ ERRORS | Build errors + 1 style issue |
| golangci-lint | ⚠ PARTIAL | Version mismatch (Go 1.24 vs 1.25) |

### Key Findings
1. **Go Version Compatibility**: golangci-lint v1.64.8 built with Go 1.24, project uses Go 1.25
   - Results in "package requires newer Go version" errors
   - Config is valid; will work when golangci-lint updates
   - go vet and gofmt work fine

2. **Style Violation Found**: 
   - `cmd/server/server_linux.go:10:33` - net.UnixAddr uses unkeyed fields
   - Should use: `net.UnixAddr{Name: "...", Net: "..."}`

3. **Deterministic Output Verified**:
   - All tools produce consistent results across multiple runs
   - goimports idempotent with --local prefix
   - Config file ensures reproducible linting

### Commands Documented
```bash
# Format check
go fmt ./cmd/... ./internal/...

# Import organization
./bin/goimports -l -w --local github.com/lin/myrient-horizon cmd internal

# Static analysis
go vet ./cmd/... ./internal/...

# Full linting (when version compatible)
./bin/golangci-lint run --config=.golangci.yml ./cmd/... ./internal/...
```

### Generated File Exclusions
Confirmed exclusions working correctly:
- pkg/myrienttree/MyrientTree/*.go (FlatBuffers generated)
- pkg/protocol/*.go (protocol definitions)
- *_gen.go, generated_*.go patterns

## Task 3: Align cmd/server/main.go to Worker Pattern (2026-03-07)

### Changes Made
Refactored `cmd/server/main.go` to match the `cmd/worker/main.go` lifecycle orchestration pattern:

1. **Simplified Signal Handling** (lines 29-35):
   - Changed from multi-signal escalation (buffer size 3) to simple cancel (buffer size 1)
   - One goroutine that logs once and calls cancel()
   - Removed "second/third Ctrl+C" escalation logic

2. **Strict Initialization Order** - All sync deps BEFORE any goroutines:
   ```
   Parse flags
   Create ctx/cancel
   Install signal handler
   Load tree (sync)
   Connect DB (sync)
   Build stree.Tree (sync)
   Create/configure hub (sync) - WorkerVersion fields set HERE
   Build handlers (sync)
   Create http.Server (sync)
   Create listener (sync) - getListener() moved out of goroutine
   THEN start goroutines (hub.Run, ServeTLS)
   ```

3. **Synchronous Listener Creation** (lines 81-86):
   - Moved `getListener(*addr)` BEFORE goroutine startup
   - Startup failures now happen before background work begins
   - Matches worker pattern where all init is synchronous

4. **Clustered Goroutine Startup** (lines 90-96):
   - Adjacent goroutines for `hub.Run(ctx)` and `server.ServeTLS`
   - Clear visual separation between sync init and async runtime

5. **Fixed Error Handling** (lines 92-96, 101-104):
   - Replaced `log.Fatalf` inside serve goroutine with buffered `errCh`
   - Main select waits on `ctx.Done()` OR `errCh`
   - On serve error: log, cancel(), single shutdown path

6. **Simplified Shutdown** (lines 98-113):
   - One select: `<-ctx.Done()` or `errCh`
   - One shutdown context with 10s timeout
   - One `server.Shutdown()` call
   - Removed redundant `cancel()` after shutdown
   - Single terminal log

7. **Fixed Race Condition** (critical):
   - **Before**: `go hub.Run(ctx)` at line 73, THEN set `hub.WorkerVersion` at line 74
   - **After**: Set `hub.WorkerVersion`, `WorkerDownloadURL`, `WorkerSHA256` BEFORE `go hub.Run(ctx)`
   - Hub fields now configured while still single-threaded

### Pattern Comparison

| Aspect | Worker Pattern | Server (Before) | Server (After) |
|--------|---------------|-----------------|----------------|
| Signal channel | Buffer 1 | Buffer 3 | Buffer 1 ✓ |
| Signal handler | Simple cancel | Multi-escalation | Simple cancel ✓ |
| Init order | All sync first | Mixed | All sync first ✓ |
| Listener | Sync | Async in goroutine | Sync ✓ |
| Hub config | N/A | AFTER goroutine | BEFORE goroutine ✓ |
| Error handling | N/A (no serve) | log.Fatalf | errCh + select ✓ |
| Shutdown | Single path | Redundant cancel | Single path ✓ |

### Verification
- `go build ./cmd/server`: ✓ SUCCESS
- `go build ./cmd/verifier`: ✓ SUCCESS
- `go vet ./cmd/server`: 1 pre-existing warning (unkeyed fields in server_linux.go)

### Key Insight
The worker pattern prioritizes **predictable startup** and **single-path shutdown**:
- Synchronous initialization ensures all dependencies ready before any background work
- Context-driven shutdown means one cancel() triggers graceful termination everywhere
- Error channels (not log.Fatalf) allow main goroutine to orchestrate shutdown

### Race Condition Lesson
Setting mutable fields on a struct AFTER starting a goroutine that reads them is a data race. Always complete configuration of shared objects before starting goroutines that access them.

## Task 4: Shared Package Boundaries (2026-03-07)

### Learnings

**Generated vs Handwritten Code Identification:**
- FlatBuffers generated files have clear `// Code generated by...` headers
- Always check first line to determine if file is generated
- Never modify generated files - they will be overwritten by build process

**Style Normalization Patterns:**
- Package-level comments should describe purpose and scope
- Function comments for exported functions are mandatory
- Error handling: use `fmt.Errorf` with context instead of `errors.New`
- Silent error handling (discarding returns) should use `_ = ` to be explicit

**Generic Type Documentation:**
- For generic types like `Tree[DirExt, FileExt]`, document what the type parameters represent
- Explain constraints/requirements on type parameters
- Document memory management implications (e.g., flatbuffer string references)

**Successful Approach:**
1. Read generated file first to establish boundary
2. Review handwritten files against worker patterns
3. Add missing comments at package and function level
4. Improve error handling to use fmt.Errorf with context
5. Build verify after each file change
6. Confirm generated files unchanged with git diff

### Pattern Consistency Achieved

Both handwritten packages now match worker-derived standards:
- Package comments: ✓
- Function documentation: ✓
- Error handling (fmt.Errorf): ✓
- No Chinese comments: ✓
- Consistent naming: ✓

## Task 5: Global Ownership Migration Path

### Completed: 2026-03-07

### Summary

Defined the ownership migration path for all backend globals and runtime state.
Every server-side instance has been assigned an explicit disposition:

### Dispositions Assigned

| Global | File | Disposition | Rationale |
|--------|------|-------------|-----------|
| `DB *Store` | db.go:65 | ALIGN → Pattern 2 | Mutable singleton with lifecycle |
| `Tree *ServerTree` | tree.go:46 | ALIGN → Pattern 2 | Mutable singleton with lifecycle |
| `upgrader` | wsrpc.go:18 | RETAIN (exception) | Immutable config, no lifecycle |

### Worker Patterns Catalogued

1. **Pattern 1: Direct Global** - `config.Global` - simple config struct
2. **Pattern 2: sync.Once Singleton** - `downloader` - thread-safe init/get
3. **Pattern 3: Global with Init** - `reporter`, `iTree` - explicit init function
4. **Pattern 4: Constructor-Owned** - `Hub` - created in main, passed around
5. **Pattern 5: Server Singleton** (NEW) - InitDB/GetDB, InitTree/GetTree
6. **Pattern 6: Immutable Config Exception** (NEW) - documented exceptions

### Key Decisions

1. **No forced dependency injection** - Worker-style Init/Get is sufficient
2. **Exceptions documented** - `upgrader` kept with rationale
3. **Clear migration path** - Step-by-step for each ALIGN global
4. **New code rules** - Explicit requirements for future globals

### Artifacts Created

- `.sisyphus/contract/ownership-map.md` - Complete ownership map
- `.sisyphus/evidence/task-5-global-ownership.txt` - Evidence
- `.sisyphus/evidence/task-5-global-ownership-error.txt` - Blocked tasks
- Updated `.sisyphus/contract/backend-style.md` - Added ownership rules

### Next Tasks Unblocked

- Task 6: Handler refactor
- Task 7: Server state alignment  
- Task 8, 9, 10: Implementation of migration


## Task 6: Server Handler Refactoring (2026-03-07T21:58:03+08:00)

### Patterns Applied

1. **Logging Pattern**: Applied 'handler:' prefix to all request logging
   - Matches worker pattern: 'reporter: message' format
   - Uses log.Printf() at runtime boundaries

2. **Dead Code Removal**: Eliminated large commented blocks
   - CORS header comments (9 lines)
   - Latency logging comments (3 lines)
   - Kept informative comment about deprecated endpoints

3. **Helper Naming**: writeJSON() follows conventions
   - Simple, focused utility function
   - No state, just formatting

4. **State Access Documentation**: Added package-level comment
   - Documents TEMPORARY use of direct DB/Tree globals
   - References ownership-map.md for migration plan
   - Explicitly notes this will change to GetDB()/GetTree() accessors

### Key Insight

Handler package is request-scoped by design, unlike worker components which are
long-running goroutines. The Constructor pattern (NewHandler with hub pointer) is
appropriate here, following Pattern 4 from the backend-style contract.

## Task 7: Server Runtime Ownership Alignment

Date: 2026-03-07

### Pattern Applied: sync.Once Singleton (Pattern 2)

Successfully aligned server globals to worker-style ownership:
- DB: InitDB/GetDB with sync.Once
- Tree: InitTree/GetTree with sync.Once
- upgrader: Retained as immutable config exception

### Key Implementation Details

1. Protocol Constants: Added missing message type constants to avoid hardcoded strings
2. Logging Prefixes: Standardized to package-level prefixes (db:, tree:, wsrpc:)
3. Backward Compatibility: Maintained DB and Tree globals for existing code

### Files Modified
- pkg/protocol/messages.go
- internal/server/db.go
- internal/server/tree.go
- internal/server/wsrpc.go
- internal/server/handler.go
- cmd/server/main.go

---


## Task 8: Worker Core Contract Adjustments

Worker patterns established:
- Pattern 1: Direct global (config.Global) - bootstrap config access
- Pattern 2: sync.Once singleton with Init/Get - Downloader, Verifier
- Pattern 3: Global with explicit init - LoadTree for iTree
- Pattern 4: Constructor-owned - not used in worker

## Task 9: Final Sweep - Server Divergence Resolution (2026-03-07)

### Summary
Completed bounded sweep across `internal/server/*`, `cmd/server/*` to identify and fix remaining divergences from worker contract.

### Divergences Found and Fixed

#### 1. Logging Prefix Inconsistencies
| Location | Before | After |
|----------|--------|-------|
| handler.go:39 | `log.Printf("%s %s...")` (no prefix) | `log.Printf("handler: %s %s...")` |
| main.go:33 | `log.Println("Shutting down...")` | `log.Println("server: shutting down...")` |
| main.go:110 | `log.Println("Server stopped")` | `log.Println("server: stopped")` |

#### 2. Code Quality Issues
| Location | Issue | Fix |
|----------|-------|-----|
| server_linux.go:10 | Unkeyed fields (go vet) | Added field names to net.UnixAddr |

#### 3. Documentation Gaps
| File | Additions |
|------|-----------|
| handler.go | Package comment with pattern documentation |
| handler.go | New() - Pattern 4 reference |
| handler.go | Register() - Route listing |
| handler.go | writeJSON() - Helper documentation |
| handler.go | corsMiddleware() - Pattern note |
| wsrpc.go | WorkerConn struct - Connection pattern |
| wsrpc.go | Hub struct - Runtime pattern, Pattern 4 |
| wsrpc.go | NewHub() - Constructor pattern |
| wsrpc.go | Hub.Run() - Long-running goroutine pattern |
| wsrpc.go | Hub.Close() - Idempotent shutdown pattern |
| wsrpc.go | GetWorkerStatus() - Thread-safety note |
| tree.go | LoadTree() - Deprecation clarity |
| db.go | NewStore() - Deprecation clarity |
| db.go | Close() - Lifecycle pattern |

### Verification Results

```bash
$ go build ./cmd/server
✓ SUCCESS

$ go vet ./internal/server/...
✓ SUCCESS (no issues)

$ go fmt ./internal/server/...
✓ SUCCESS (no changes)
```

### Pattern Consistency Status

| Pattern | Worker | Server | Status |
|---------|--------|--------|--------|
| Pattern 2: sync.Once singleton | downloader | DB, Tree | ✓ Aligned |
| Pattern 4: Constructor-owned | (not used) | Handler, Hub | ✓ Aligned |
| Pattern 6: Immutable config | N/A | upgrader | ✓ Documented |
| Logging prefixes | "reporter:" | "handler:", "db:", etc. | ✓ Aligned |
| Package documentation | Extensive | Extensive | ✓ Aligned |
| Function documentation | Pattern-focused | Pattern-focused | ✓ Aligned |

### Evidence Files
- `.sisyphus/evidence/task-9-sweep.txt` - Full evidence
- `.sisyphus/evidence/task-9-sweep-error.txt` - Error log (empty)


---

## Task 10: Final Backend Verification (2026-03-07)

All verification checks PASSED.

### Evidence Files
- task-10-final-verification.txt
- task-10-final-verification-error.txt (empty)

Status: BACKEND STYLE UNIFICATION COMPLETE
