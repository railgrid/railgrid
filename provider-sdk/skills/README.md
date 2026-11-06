# Shared assistant skills

This package contains the skill parser, bounded catalog, immutable snapshots,
provider-published sources, filesystem sources, and activation filtering used by
App Studio and Agents. It has no provider, HTTP, database, or model dependency.

Providers implement `Source` to supply their own scoped package storage. App
Studio keeps its project filesystem adapter and embedded builtins; Agents owns
its workspace library and per-agent opt-in policy. `ScopeWorkspace` identifies
workspace packages without changing App Studio's existing project-qualified IDs.

A snapshot contains validated guidance and supporting resources, not permission
grants. Runtime adapters must authorize the source, restrict the effective
catalog, and check selected identities and digests before injecting guidance.
Never interpret package instructions as tool policy or executable authority.

Run `go test ./skills` from the provider SDK to verify the shared parser and
catalog. Provider-specific storage and authorization tests remain with providers.
