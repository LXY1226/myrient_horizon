# Backend Style Contract

## Scope Definition

### Handwritten Scope (Target for Standardization)
Files in these locations MUST follow the worker package patterns:
- `cmd/worker/main.go`
- `internal/worker/*.go` (worker.go, reporter.go, downloader.go, task.go, updater.go)
- `internal/server/*.go` (handler.go, wsrpc.go, db.go, tree.go)
- `internal/worker/config/*.go`
- `internal/worker/aria2/*.go`

### Generated Scope (READ-ONLY - Do Not Edit)
Files matching these patterns are auto-generated and should never be manually modified:
- `pkg/myrienttree/MyrientTree/*.go` (FlatBuffers generated)
- `pkg/protocol/*.go` (protocol definitions - may be partially generated)

## Worker Package Patterns (Template Rules)

### 1. Lifecycle Pattern (`cmd/worker/main.go`)
```go
// Standard main.go structure:
// 1. Version logging
// 2. Context with cancel + signal handling
// 3. Config loading via EnsureConfig()
// 4. Dependency initialization (reporter, aria2, verifier, downloader)
// 5. Goroutine startup pattern
// 6. Heartbeat ticker loop
// 7. Graceful shutdown with <-ctx.Done()
```

Key rules:
- Use `context.WithCancel()` at top level
- Signal handler must call `cancel()`
- All long-running components have `Run(ctx)` method
- Defer cleanup in reverse initialization order

### 2. Bootstrap/Init Pattern (`internal/worker/worker.go`)
```go
// EnsureConfig pattern:
// - Load from disk, fail if corrupted
// - Auto-create default if missing (then exit with instructions)
// - Auto-register if no key (one-time setup)
// - Set config.Global singleton
```

Key rules:
- `EnsureConfig()` returns `*config.WorkerConfig`
- Fatal errors use `log.Fatalf()` with clear messages
- Registration prints key prominently

### 3. Stateful Runtime Owner Pattern (`internal/worker/downloader.go`)
```go
// Singleton pattern with sync.Once:
var (
    instance *Component
    once     sync.Once
)

func InitComponent(args) *Component {
    once.Do(func() {
        instance = &Component{...}
    })
    return instance
}

// Accessors:
func GetComponent() *Component { return instance }
```

Key rules:
- One `sync.Once` per singleton
- `InitXXX()` takes dependencies as parameters
- `GetXXX()` returns pointer for external access
- Internal access uses global `xxx` variable

### 4. Long-Running Loop Pattern (`internal/worker/reporter.go`)
```go
// Component structure:
type component struct {
    mu        sync.Mutex
    state     []Item
    ticker    *time.Ticker
    closing   *sync.WaitGroup
}

// Run loop structure:
func (c *component) Run() {
    backoff := time.Second
    const maxBackoff = 30 * time.Second
    for {
        conn, err := connect()
        if err != nil {
            backoff = min(backoff*2, maxBackoff)
            continue
        }
        c.readLoop(conn)
    }
}
```

Key rules:
- Exponential backoff with max cap
- `Run()` never returns unless context cancelled
- `Close()` returns `*sync.WaitGroup` for graceful shutdown
- Prefix all log messages: `component: message`

## Server Divergence Map

### Current Divergences from Worker Pattern

| File | Issue | Worker Pattern |
|------|-------|----------------|
| `internal/server/db.go:66` | Global var without Init/Get | Uses `var DB *Store` without sync.Once |
| `internal/server/db.go` | No lifecycle management | Missing Run/Close pattern |
| `internal/server/handler.go` | Request-scoped, no state | Uses hub pointer, not singleton |
| `internal/server/wsrpc.go` | Hub is manually managed | Uses `NewHub()` constructor pattern |
| `internal/server/tree.go:46` | Global var without Init/Get | Uses `var Tree *ServerTree` |

### Design Differences (Acceptable)

**Server package is request/response oriented:**
- Handlers process HTTP requests (short-lived)
- WebSocket connections are managed per-worker
- Database is global because it's connection-pooled

**Worker package is event/loop oriented:**
- Components run forever in goroutines
- Need explicit lifecycle management
- Must handle reconnection and backoff

## Prohibited Patterns

1. **Global vars without sync.Once initialization**
   ```go
   // BAD:
   var DB *Store  // in server/db.go
   
   // GOOD:
   var (
       instance *Store
       once     sync.Once
   )
   ```

2. **Chinese comments in code**
   - Found in: `internal/worker/downloader.go`, `internal/worker/task.go`
   - Move to English or delete

3. **Dead code blocks**
   - Found large commented sections in `downloader.go`, `task.go`, `handler.go`
   - Delete or move to documentation

4. **TODO/FIXME without context**
   - Many TODOs lack issue references or dates
   - Add `(YYYY-MM-DD)` or `(Issue #N)` prefix

## Migration Path

To bring `server` package in line with `worker` patterns:

1. **db.go**: Add `InitStore()` with sync.Once, `GetStore()` accessor
2. **tree.go**: Add `InitTree()` with sync.Once, `GetTree()` accessor  
3. **handler.go**: No change needed (request-scoped by design)
4. **wsrpc.go**: No change needed (manually managed hub by design)

## Build Evidence

**Captured**: 2026-03-07

**Status**: BROKEN (5 compilation errors in cmd/worker/main.go)

```
# myrient-horizon/cmd/worker
cmd/worker/main.go:38:24: undefined: worker.InitReporter
cmd/worker/main.go:46:16: undefined: worker.GetReporter
cmd/worker/main.go:64:21: undefined: worker.InitVerifier
cmd/worker/main.go:74:20: too many arguments in call to downloader.Run
	have (context.Context)
	want ()
cmd/worker/main.go:93:22: undefined: worker.GetDiskFreeGB
```

**Required Fixes (blocking Tasks 2-10):**
1. Implement `worker.InitReporter()` function
2. Implement `worker.GetReporter()` function
3. Implement `worker.InitVerifier()` function
4. Fix `downloader.Run()` signature to accept context
5. Implement `worker.GetDiskFreeGB()` function

## Pattern Violations Found (Current Baseline)

### Global Pointer Variables
- `internal/worker/verify.go:16` - `var verifyTaskCh = make(chan *Task, 16384)`
- `internal/worker/reporter.go:33` - `var Reporter *reporter`
- `internal/worker/task.go:31` - `var iTree *mt.Tree[dirExt, fileExt]`
- `internal/server/tree.go:46` - `var Tree *ServerTree`
- `internal/server/db.go:65` - `var DB *Store`

### Channel Globals
- `internal/worker/downloader.go:13` - `var badVerifyTaskCh = make(chan Task, 16384)`
- `internal/worker/downloader.go:14` - `var downloadTaskCh = make(chan Task, 16)`
- `internal/worker/downloader.go:15` - `var downloadTaskReplaced = make(chan struct{})`

### Chinese Comments
- `internal/worker/task.go:13` - `// 全局文件索引`
- `internal/worker/task.go:14` - `// 本地路径（最终的绝对路径）`

### Dead Code / Large Comment Blocks
- `internal/worker/downloader.go:19-30` - Worker workflow TODO block (12 lines)
- `internal/worker/downloader.go:91-132` - Entire Run() implementation commented (42 lines)
- `internal/worker/downloader.go:164-171` - Commented failInFlight function (8 lines)
- `internal/server/handler.go:28-36` - Commented CORS headers (9 lines)
- `internal/server/handler.go:38-46` - Commented latency logging (9 lines)
- `internal/server/handler.go:59` - Comment about deprecated endpoints

### Inconsistent Message Type Constants
- `internal/server/wsrpc.go:228` - Hardcoded `"heartbeat"` instead of `protocol.MessageHeartbeat`
- `internal/server/wsrpc.go:230` - Hardcoded `"status_sync_request"` instead of constant
- `internal/server/wsrpc.go:321` - Hardcoded `"status_sync_response"` instead of constant

## Verification Checklist

- [x] Generated files identified: `pkg/myrienttree/MyrientTree/Node.go`
- [x] Global variables catalogued: 9 locations across 5 files
- [x] Chinese characters found: 2 locations in `internal/worker/task.go`
- [x] Large comment blocks found: 6 locations
- [x] Worker patterns documented
- [x] Server divergence mapped
- [x] Build errors captured and enumerated

## References

- Effective Go: https://go.dev/doc/effective_go
- Google Go Style: https://google.github.io/styleguide/go/decisions

## Ownership Rules (Added by Task 5)

### Global Variable Dispositions

All package-level globals in `internal/server/` have been assigned explicit dispositions:

| Global | File | Disposition | Target Pattern |
|--------|------|-------------|----------------|
| `DB *Store` | `db.go:65` | **ALIGN** | Pattern 2: `InitDB()` / `GetDB()` |
| `Tree *ServerTree` | `tree.go:46` | **ALIGN** | Pattern 2: `InitTree()` / `GetTree()` |
| `upgrader` | `wsrpc.go:18` | **RETAIN** | Pattern 1 (immutable config exception) |

### Server Package Patterns

Based on worker patterns, server globals fall into two categories:

#### Pattern 5: Server Singleton Pattern

For server-side singletons with lifecycle:

```go
var (
    instance *ServerComponent
    once     sync.Once
)

// InitServerComponent initializes the singleton. Called once from main.
func InitServerComponent(args) *ServerComponent {
    once.Do(func() {
        instance = &ServerComponent{...}
    })
    return instance
}

// GetServerComponent returns the singleton. Safe for concurrent use.
func GetServerComponent() *ServerComponent {
    return instance
}
```

**Applies to:** `DB`, `Tree`

#### Pattern 6: Immutable Config Exception

For immutable package-level configuration with no mutable state:

```go
// upgrader is package-level configuration (immutable).
// Exception to Init/Get pattern: no mutable state, no lifecycle.
var upgrader = websocket.Upgrader{...}
```

**Applies to:** `upgrader`

**Requirements for exception:**
1. No mutable fields (all fields are const-equivalent after init)
2. No lifecycle (no Init/Shutdown needed)
3. Used as configuration, not state
4. Documented with exception comment

### Call Site Rules

**Inside owning package:**
- May use internal instance directly: `instance.DoSomething()`
- Only for package-internal code

**Outside owning package:**
- MUST use Get accessor: `server.GetDB().DoSomething()`
- Never access global variable directly

**Main package (initialization):**
- Call Init function: `server.InitDB(connString)`
- Never assign to global directly: `server.DB = ...` (FORBIDDEN)

### New Code Requirements

1. **No new raw globals** - Every package-level variable must have explicit disposition
2. **Mutable state MUST use Pattern 5** (Init/Get with sync.Once)
3. **Immutable config MAY use Pattern 6** with documented exception rationale
4. **Request-scoped state MUST use constructor injection** (Pattern 4)
5. **Add to ownership map** when creating new globals

### Migration Status

- [ ] `db.go`: Migrated to `InitDB()` / `GetDB()`
- [ ] `tree.go`: Migrated to `InitTree()` / `GetTree()`
- [x] Ownership map documented in `ownership-map.md`
- [x] Disposition rules added to contract

### References

- Ownership Map: `.sisyphus/contract/ownership-map.md`
- Task 5 Evidence: `.sisyphus/evidence/task-5-global-ownership.txt`
