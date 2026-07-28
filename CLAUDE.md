# Claude Code Rules for Distribution Module

## Code Style

- Early returns, no nested ifs
- Extract logic into small, focused functions
- Flat structure over deep nesting
- Idiomatic Go - if err != nil { return } pattern

## Component Design

- Handle() switch cases should be minimal - delegate to functions
- No JSON parsing in components - SDK handles deserialization

## CRITICAL: Handler Response Propagation

Never ignore handler() return values — it breaks blocking I/O and causes timeouts:

```go
// CORRECT
return handler(ctx, OutPort, data)

// WRONG - breaks blocking I/O
_ = handler(ctx, OutPort, data)
return nil
```

Exception: `_reconcile` and `_control` port handler calls can ignore returns.

## Declaring a Blocking Component: the SyncRPC Capability

If a component **blocks holding a live connection** — emits on a source port and synchronously waits for the downstream chain to deliver a result back within the same request — it MUST declare the `module.SyncRPC` capability:

```go
func (c *Component) SyncRPC() module.SyncRPCInfo { return module.SyncRPCInfo{} }
```

`module build` auto-tags the component `sync_rpc`; the platform keeps the subgraph containing it on blocking request/reply delivery, while trigger-driven subgraphs run durable (fire-and-forget) — modes are derived, never configured. Forgetting this is fatal for a blocking component: durable hops return nothing to their sender, so the awaited response never comes back and the connection times out even with perfect handler-return discipline. Canonical implementer: `http_server` (live socket). Components that merely sit in its subgraph — a Slack command handler, a router — declare nothing; they inherit classic delivery automatically.

## CRITICAL: System Port Delivery Order

System ports (`_settings`, `_control`, `_reconcile`) have NO guaranteed delivery order. On pod restart, `_reconcile` may fire before `_settings`. Components that persist state to metadata must use the `settingsFromPort` guard flag to prevent reconcile from overwriting fresh values with stale metadata.

## Context Pattern for Schema Generation

```go
type Context any

type Request struct {
    Context Context `json:"context,omitempty" configurable:"true" title:"Context"`
}

type Output struct {
    Context Context `json:"context,omitempty" configurable:"true" title:"Context"`
}

type Error struct {
    Context Context `json:"context,omitempty" configurable:"true" title:"Context"`
    Error   string  `json:"error" title:"Error"`
}
```

## Module Components

- **registry_catalog**: Lists repos+tags from a remote registry. Stateless request/response using crane.
- **registry_copy**: Copies images between registries. Stateless request/response using crane.

## Architecture

This module does NOT include a container registry. The registry is external infrastructure (Zot, Harbor, distribution/registry, etc.) installed via its own Helm chart. This module only handles image discovery and replication between registries.
