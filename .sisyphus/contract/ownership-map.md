# Backend Global Ownership Map

## Purpose

This document defines the disposition for every global variable and runtime state owner in the backend (`internal/server/` and `internal/worker/`). It establishes the migration path from current state to worker-style ownership patterns.

## Worker-Style Ownership Patterns (Reference)

| Pattern | Description | Example |
|---------|-------------|---------|
| **Pattern 1: Direct Global** | Simple config struct, no initialization needed | `config.Global WorkerConfig` |
| **Pattern 2: sync.Once Singleton** | Thread-safe singleton with explicit Init/Get | `downloader.InitDownloader()` / `GetDownloader()` |
| **Pattern 3: Global with Init Function** | Explicit initialization before use | `LoadTree()` initializes `iTree` |
| **Pattern 4: Constructor-Owned** | Created in main, passed to dependents | `NewHub()` returns `*Hub` |

## Server Package Inventory

| Global | Location | Current Pattern | Disposition | Target Pattern | Rationale |
|--------|----------|-----------------|-------------|----------------|-----------|
| `DB *Store` | `db.go:65` | Direct global, assigned in main | **ALIGN** | Pattern 2: `InitDB()` / `GetDB()` | Database is connection-pooled singleton, fits sync.Once model |
| `Tree *ServerTree` | `tree.go:46` | Direct global, assigned in main | **ALIGN** | Pattern 2: `InitTree()` / `GetTree()` | ServerTree is singleton with lifecycle, fits sync.Once model |
| `upgrader` | `wsrpc.go:18` | Package var with literal | **RETAIN** | Pattern 1 (immutable config) | Immutable websocket configuration, no state |

## Worker Package Inventory (Reference)

| Global | Location | Current Pattern | Status |
|--------|----------|-----------------|--------|
| `Global WorkerConfig` | `config/config.go:28` | Direct global | ✅ Follows Pattern 1 |
| `Reporter *reporter` | `reporter.go:33` | Direct global, needs Init | ⚠️ Needs Pattern 2 alignment |
| `iTree *Tree` | `task.go:31` | Global with `LoadTree()` | ✅ Follows Pattern 3 |
| `downloaderInstance` | `downloader.go:33` | sync.Once singleton | ✅ Follows Pattern 2 |

## Migration Paths

### Path 1: `internal/server/db.go`

**Current State:**
```go
var DB *Store  // Line 65

// In cmd/server/main.go:
stree.DB, err = stree.NewStore(ctx, *dbURL)  // Line 47
```

**Target State:**
```go
var (
    dbInstance *Store
    dbOnce     sync.Once
)

func InitDB(connString string) *Store {
    dbOnce.Do(func() {
        pool, err := pgxpool.New(context.Background(), connString)
        if err != nil {
            log.Fatalf("db: failed to connect: %v", err)
        }
        dbInstance = &Store{pool: pool}
    })
    return dbInstance
}

func GetDB() *Store {
    return dbInstance
}
```

**Migration Steps:**
1. Add `sync.Once` and `dbInstance` variables
2. Create `InitDB()` function with `sync.Once`
3. Create `GetDB()` accessor
4. Deprecate `NewStore()` or make it internal
5. Update `cmd/server/main.go` to call `InitDB()` instead of direct assignment
6. Update all `DB.` references to `GetDB().`

### Path 2: `internal/server/tree.go`

**Current State:**
```go
var Tree *ServerTree  // Line 46

// In cmd/server/main.go:
stree.Tree = stree.NewTree(baseTree)  // Line 55
```

**Target State:**
```go
var (
    treeInstance *ServerTree
    treeOnce     sync.Once
)

func InitTree(base *mt.Tree[DirExt, FileExt]) *ServerTree {
    treeOnce.Do(func() {
        treeInstance = NewTree(base)
    })
    return treeInstance
}

func GetTree() *ServerTree {
    return treeInstance
}
```

**Migration Steps:**
1. Add `sync.Once` and `treeInstance` variables
2. Create `InitTree()` function with `sync.Once`
3. Create `GetTree()` accessor
4. Keep `NewTree()` for custom creation (testing)
5. Update `cmd/server/main.go` to call `InitTree()` instead of direct assignment
6. Update all `Tree.` references to `GetTree().`

## Documented Exceptions

### Exception 1: `wsrpc.go:upgrader`

**Rationale for Retention:**
- Immutable configuration struct with no mutable state
- Used only as parameter to `websocket.Upgrade()`
- No lifecycle concerns (no initialization, shutdown)
- Changing to sync.Once adds complexity without benefit

**Documented As:**
```go
// upgrader is package-level configuration (immutable).
// Exception to Init/Get pattern: no mutable state, no lifecycle.
var upgrader = websocket.Upgrader{...}
```

## Ownership Rules

### New Code Guidelines

1. **Package-level mutable state MUST use Pattern 2** (sync.Once + Init/Get)
2. **Package-level immutable config MAY use Pattern 1** (direct global with comment)
3. **Request-scoped state MUST use Pattern 4** (constructor injection)
4. **No new raw globals** without explicit disposition in this document

### Call Site Guidelines

1. **Inside package:** May use internal global directly (e.g., `dbInstance`)
2. **Outside package:** MUST use Get accessor (e.g., `GetDB()`)
3. **Initialization:** Main package calls Init, never assigns to globals directly

## Verification Checklist

- [x] All server globals inventoried
- [x] All worker patterns documented (reference)
- [x] Disposition assigned to each global (ALIGN / RETAIN / REMOVE)
- [x] Migration path documented for ALIGN globals
- [x] Exceptions documented with rationale
- [x] Rules established for new code

## References

- Task: Task 5 - Global Ownership Migration
- Contract: `.sisyphus/contract/backend-style.md`
- Evidence: `.sisyphus/evidence/task-5-global-ownership.txt`
