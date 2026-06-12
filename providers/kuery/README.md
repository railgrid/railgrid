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

## Status: Phase 2 — fleet query engine

What works today:

- **Embedded kuery engine** (`core/`): SQLite (default, on the chart's
  PVC) or Postgres store, the query engine, the multi-cluster sync
  controller, and the stale-cluster GC.
- **Edge engagement** (`engagement/`): watches `Edge` objects across every
  tenant workspace that Enabled the provider (APIExport virtual
  workspace), and syncs each connected kubernetes edge through the hub's
  edges-proxy using the provider SA credential the Enable-time grant
  authorizes. Engaged clusters are keyed `{tenantCluster}/{edgeName}` and
  labelled with their tenant.
- **Tenant-scoped query API** (`queryapi/`): `POST /api/query` takes a
  kuery `QuerySpec`; the cluster filter is force-rewritten to the caller's
  `X-Kedge-Tenant` before it reaches the engine — the only path to the
  store.
- **MCP tools** (`mcpserver/`): `kuery_query` (fleet-wide spec
  passthrough) and `kuery_impact` (declared blast radius of one object) at
  `/mcp` + `/mcp/sse`.
- **Registration surface** from Phase 1: heartbeats, CatalogEntry
  (SavedView schema, `edges` claim, `edgeProxyAccess`), Helm chart.

What lands next (Phase 3, see the design doc): the portal UI — inventory
table first, then the object graph and impact view — plus an e2e suite
asserting edge-object sync end to end.

## Layout

```
main.go          serve loop: healthz, /api/query, /api/status, /mcp, portal, heartbeat
core/            embedded kuery wiring (store, engine, sync, gc)
engagement/      Edge watcher → Engage/Disengage via the edges-proxy
queryapi/        tenant-scoped POST /api/query (the store's only entry point)
mcpserver/       kuery_query + kuery_impact MCP tools
assets.go        //go:embed of portal/dist
manifest.yaml    CatalogEntry (SavedView schema, edges claim, edgeProxyAccess)
portal/          Vite + TS micro-frontend (custom element)
deploy/chart/    Helm chart (host cluster only; PVC for the SQLite store)
```

## Local development

From the kedge repo root:

```bash
make build-kuery-provider        # portal build + go build
make run-provider-kuery          # run against a local hub
make install-provider-kuery      # apply manifest.yaml to embedded kcp
```
