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
