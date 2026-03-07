# Backend Ownership Map

Date: 2026-03-07
Scope: `cmd/server/*.go`, `internal/server/*.go`

## Ownership Contract

- Server-wide mutable runtime state must have one package owner.
- Shared package-owned runtime state must follow the worker singleton shape: `Init*`, `Get*`, optional `Run`, and one bootstrap owner in `cmd/server/main.go`.
- Constructor-owned request/runtime objects may remain non-singletons when they are already passed explicitly and are not exposed as package globals.
- Immutable package-level configuration values are allowed as documented exceptions.
- No new backend package state should be introduced outside these two buckets.

## File Map

| File | Current owner/state | Disposition | Target path |
|------|---------------------|-------------|-------------|
| `internal/server/db.go` | `var DB *Store` owns shared PostgreSQL pool and all store methods | align | Replace raw global with `InitDB(ctx, dsn)` + `GetDB()`, keep `cmd/server/main.go` as bootstrap owner, keep `Store.Close()` as shutdown hook |
| `internal/server/tree.go` | `var Tree *ServerTree` owns in-memory aggregate stats and per-file hash state | align | Replace raw global with `InitTree(base)` + `GetTree()`, keep `cmd/server/main.go` as bootstrap owner, keep mutation behind `ServerTree` methods |
| `internal/server/wsrpc.go` | `var upgrader = websocket.Upgrader{...}` | exception | Retain as immutable package config; it is not a runtime owner and does not need worker-style lifecycle hooks |
| `internal/server/wsrpc.go` | `Hub` owns live worker connections, version gate settings, and websocket fanout | exception | Retain constructor ownership via `NewHub()`; single process owner stays in `cmd/server/main.go` and is injected into `Handler` |
| `internal/server/handler.go` | `Handler` holds only `hub *Hub` | exception | Retain constructor ownership via `New(hub)`; no package state |
| `cmd/server/main.go` | bootstrap sequence owns init order for tree, DB, hub, handler, and `http.Server` | exception | Retain as package-level orchestrator; after DB/Tree alignment it remains the only runtime bootstrap owner |

## Decisions By Current Global

### `internal/server/db.go:66` - `var DB *Store`

- Disposition: align
- Why: process-wide mutable owner, read from handlers and websocket paths, same shape as worker singleton candidates
- Required migration:
  - add `sync.Once`-guarded `InitDB`
  - add `GetDB` accessor for all server package callers
  - move all direct `DB.` call sites to `GetDB().`
  - keep shutdown in `cmd/server/main.go` by closing the initialized store

### `internal/server/tree.go:46` - `var Tree *ServerTree`

- Disposition: align
- Why: process-wide mutable owner for in-memory status aggregation and websocket report application
- Required migration:
  - add `sync.Once`-guarded `InitTree`
  - add `GetTree` accessor for all server package callers
  - move all direct `Tree.` call sites to `GetTree().`
  - keep all mutation inside `ServerTree` methods; callers only bootstrap or query

## Server Runtime State Inventory

| File | State | Owner after migration |
|------|-------|-----------------------|
| `internal/server/db.go` | `Store.pool` | `DB` singleton owned by `InitDB`/`GetDB` |
| `internal/server/tree.go` | `ServerTree.base`, `ServerTree.fileHashes`, directory/file ext stats | `Tree` singleton owned by `InitTree`/`GetTree` |
| `internal/server/wsrpc.go` | `Hub.conns`, version metadata | `Hub` instance created in `cmd/server/main.go` |
| `internal/server/handler.go` | `Handler.hub` reference only | `Handler` instance created in `cmd/server/main.go` |
| `cmd/server/main.go` | `http.Server`, signal/shutdown context | `main` bootstrap/orchestrator |

## Guardrails For Later Tasks

- New shared mutable server state must either be attached to an existing owner (`Store`, `ServerTree`, `Hub`) or introduced behind a worker-style `Init*`/`Get*` contract.
- Do not add new package globals in `internal/server/*.go` unless they are immutable config exceptions like `upgrader`.
- Prefer constructor injection for request-scoped helpers and per-connection state.
- The next server ownership refactor should update `internal/server/db.go`, `internal/server/tree.go`, `internal/server/handler.go`, `internal/server/wsrpc.go`, and `cmd/server/main.go` together so call sites move atomically.
