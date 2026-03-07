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

Captured: 2026-03-07

```
Build status: TIMEOUT (120s)
Likely cause: External dependencies or infinite loop in init
```

## Pattern Violations Found

### Global Pointer Variables
- `internal/worker/reporter.go:28` - `var Reporter *reporter`
- `internal/worker/task.go:31` - `var iTree *mt.Tree[dirExt, fileExt]`
- `internal/server/tree.go:46` - `var Tree *ServerTree`
- `internal/server/db.go:66` - `var DB *Store`

### Chinese Comments
- `internal/worker/downloader.go:20-29` - Block comment
- `internal/worker/task.go:13-14` - Inline comments

### Dead Code
- `cmd/verifier/main.go:95` - Commented code
- `internal/worker/task.go:63-84` - Commented function
- `internal/worker/downloader.go:78-118` - Entire Run() body commented
- `internal/worker/downloader.go:152-158` - Commented function
- `internal/server/handler.go:28-36` - Commented CORS code
- `internal/server/handler.go:45-46` - Commented latency logging
- `internal/server/wsrpc.go:26-30` - Commented fields
- `internal/server/wsrpc.go:127` - Commented cancel field (build error)

## References

- Effective Go: https://go.dev/doc/effective_go
- Google Go Style: https://google.github.io/styleguide/go/decisions
