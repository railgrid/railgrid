# Kuery provider

> [!IMPORTANT]
> **Read-only mirror — do not push or open PRs here.**
> The standalone [`faroshq/provider-kuery`](https://github.com/faroshq/provider-kuery)
> repository is **automatically synced** from the kedge monorepo
> [`faroshq/kedge`](https://github.com/faroshq/kedge) (path `providers/kuery/`)
> via [splitsh-lite](https://github.com/splitsh/lite). Every sync force-updates
> the mirror, so any direct change here is overwritten. File issues and PRs
> against [`faroshq/kedge`](https://github.com/faroshq/kedge) instead.

Fleet-wide object search, relationship traversal, and impact analysis across
the edge clusters connected to a kedge workspace — built on
[kuery](https://github.com/faroshq/kuery), a multi-cluster query engine that
syncs objects into a local SQL store and answers relationship queries
(owners, descendants, spec references, selector matches, cross-cluster
links) that plain list/watch can't.

The full design — architecture, tenant isolation, the Enable-time
edges-proxy grant, value ranking, and phasing — lives in the kedge repo at
[`docs/kuery-provider-architecture.md`](../../docs/kuery-provider-architecture.md).

## Status: Phase 1 skeleton

What works today:

- **Registration surface**: `/healthz`, hub heartbeats, the `CatalogEntry`
  (`manifest.yaml`) with the SavedView APIResourceSchema, the
  `edges` permission claim, and `spec.edgeProxyAccess: true`.
- **Portal placeholder**: a custom element showing the portal handshake and
  the backend proxy round-trip (`/api/status`).
- **Helm chart** (`deploy/chart/`) targeting the host cluster.

What lands next (Phase 2, see the design doc): the embedded kuery engine,
the Edge engagement controller (informer sync through the hub's
edges-proxy), the tenant-scoped `/api/query`, and the `kuery_query` /
`kuery_impact` MCP tools.

## Layout

```
main.go          serve loop: healthz, /api/status, portal assets, heartbeat
assets.go        //go:embed of portal/dist
manifest.yaml    CatalogEntry (SavedView schema, edges claim, edgeProxyAccess)
portal/          Vite + TS micro-frontend (custom element)
deploy/chart/    Helm chart (host cluster only)
```

## Local development

From the kedge repo root:

```bash
make build-kuery-provider        # portal build + go build
make run-provider-kuery          # run against a local hub
make install-provider-kuery      # apply manifest.yaml to embedded kcp
```
