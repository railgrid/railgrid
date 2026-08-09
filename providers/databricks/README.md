# Databricks provider

A kedge provider that exposes imported Databricks SQL Warehouse tables to kedge
workspaces. The provider owns Databricks `Connection`, `Warehouse`, and `Table`
resources in the tenant workspace. App Studio consumes existing `Table`
resources by exact `tableRef` through the catalog-backed Provider Actions
contract; import and pinning remain provider-owned.

For the complete App Studio integration workflow, see the shipped
[`databricks-app-integration` skill](skills/databricks-app-integration/SKILL.md).
It is distributed as an inline, digest-pinned provider package; publication
does not grant action authority. It follows App Studio's system-skill default
of enabled, and each project may disable or re-enable it.

## What works today

- Tenant-facing CRDs for:
  - `Connection`: Databricks workspace host plus a tenant Secret reference.
  - `Warehouse`: SQL warehouse handle.
  - `Table`: stable imported table handle with cached schema metadata.
- Provider Actions on the embedded virtual workspace, addressed with the
  platform data-plane grammar
  `/actions/clusters/{clusterID}/tables/{name}/query_table/v1` and authorized
  as the caller (SSAR `get` on the Table + SSAR `create` on the
  `tables/query_table` subresource):
  - `query_table/v1` (catalog-declared, read-only, bounded)
- Optional MCP tools at `/mcp` and `/mcp/sse`, controlled by
  `DATABRICKS_MCP_ENABLED` and reusing the same executor:
  - `databricks__list_tables`
  - `databricks__describe_table`
  - `databricks__query_table` (versioned `actionVersion: v1`, bounded rows)
- Portal UX for creating and updating `Connection`, `Warehouse`, and `Table`
  handles, plus cached schema inspection.
- Multicluster controllers validate PAT credentials against the Databricks
  current-user API, validate SQL warehouse handles, refresh table schema status,
  and write `Validated` / `Ready` conditions.
- Provider controllers use the provider's accepted APIExport permission claims
  to resolve referenced credential `Secret` resources for validation only.
  Databricks credentials are never returned to App Studio, generated apps, or
  browser clients.
- `query_table/v1` is a request-scoped direct action. The provider authorizes
  the exact imported `Table`, resolves its Warehouse, Connection, and Secret,
  runs a provider-constructed `SELECT`, and returns only bounded structured
  rows. It does not create query resources or persist result rows in
  control-plane status. The optional MCP tool is only a presentation adapter
  over that same executor.

## Current import path

Users can import a table from the provider portal by creating a Connection, a
Warehouse, and a Table handle. A user or admin can also create those tenant
resources directly:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: sales-databricks-token
  namespace: data-creds
type: Opaque
stringData:
  token: "<databricks bearer token>"
---
apiVersion: databricks.kedge.faros.sh/v1alpha1
kind: Connection
metadata:
  name: sales-workspace
spec:
  host: "https://dbc-xyz.cloud.databricks.com"
  authType: pat
  secretRef:
    name: sales-databricks-token
    namespace: data-creds
    key: token
---
apiVersion: databricks.kedge.faros.sh/v1alpha1
kind: Warehouse
metadata:
  name: sales-warehouse
spec:
  connectionRef: sales-workspace
  warehouseID: "abc123def456"
---
apiVersion: databricks.kedge.faros.sh/v1alpha1
kind: Table
metadata:
  name: order-history
spec:
  connectionRef: sales-workspace
  warehouseRef: sales-warehouse
  catalog: sales
  schema: gold
  table: order_history
```

App Studio can then discover `order-history` as a `tableRef` for design-time
metadata, schema inspection, and user-facing planning.

## Runtime data access

The `query_table/v1` action accepts only an imported Table resource reference,
exact optional column names, and a limit from 1 through 100. Raw SQL,
warehouse/connection handles, hosts, and credentials are rejected. The
request-scoped executor authorizes the caller's `get` on the exact Table,
resolves `Table → Warehouse → Connection → Secret`, checks current `Ready` /
`Validated` conditions and matching connection references, then posts a
provider-constructed `SELECT` to `/api/2.0/sql/statements`. Results are capped
at 100 rows, 64 columns, and 64 KiB; `truncated: true` reports bounded
upstream results. Provider errors are sanitized.

The action endpoint requires the hub-resolved tenant and cluster headers and
the hub-injected `resourceRef`; it does not accept an arbitrary table name or
backend target. The hub's public backend proxy reserves `/actions` and returns
`404`, so callers must use the hub Provider Actions route. Failures retain the
published structured envelope (`code`, sanitized `message`, `retryable`, and
request/binding metadata); grant, schema, or bound-resource failures are not
retry instructions.

The companion skill makes the same boundary explicit for app builders: discover
the existing project grant, use its bound `Table` (never a caller-selected
table), invoke only from a server route through `@kedge/actions-node`, honor the
published schema and limits, and handle the structured error envelope. Verify
the action response first, then runtime/preview reachability, then rendered
state and interactions; HTTP 200, `Ready`, or a reachable preview alone is not
data-path evidence. Grants, binding status, and the live catalog remain
authoritative and fail closed.

Connection hosts must be Databricks workspace root URLs over HTTPS. The backend
allows the standard Databricks workspace domains by default; set
`DATABRICKS_ALLOWED_HOST_SUFFIXES` only when the deployment deliberately supports
private Databricks workspace domains.

## Local development

```sh
make build-databricks-provider
make install-provider-databricks
make init-provider-databricks
make run-provider-databricks
```

For a no-kcp smoke test only, set `DATABRICKS_DEV_STATIC_TABLES=true`. That mode
uses a seeded `order-history` table and a stub validator; normal serve mode fails
closed if tenant table lookup or Databricks credentials are unavailable.

For local E2E against an explicitly configured self-signed HTTPS fake on
`127.0.0.1`, set `DATABRICKS_E2E_LOOPBACK=true`. The provider then uses a
loopback-only development transport; production remains strict TLS and host
allowlisting by default.

## Gaps

- Catalog/schema discovery is not implemented yet; the first UX imports a known
  table by reference.
- Generated apps must call `query_table/v1` through the hub Provider Actions
  route and the server-only SDK; do not hardcode provider backend URLs or
  Databricks credentials into App Studio-generated source. MCP is optional and
  is not required by this app path.
- OAuth federation and service-principal token exchange should be reconciled
  into token-bearing Secrets before validation or future provider actions.
