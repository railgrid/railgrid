# Databricks provider

A kedge provider that exposes imported Databricks SQL Warehouse tables to kedge
workspaces. The provider owns Databricks `Connection`, `Warehouse`, and `Table`
resources in the tenant workspace. App Studio can inspect existing `Table`
resources by `tableRef` for design-time guidance; generated apps do not yet have
a sanctioned runtime data-access bridge to Databricks.

## What works today

- Tenant-facing CRDs for:
  - `Connection`: Databricks workspace host plus a tenant Secret reference.
  - `Warehouse`: SQL warehouse handle.
  - `Table`: stable imported table handle with cached schema metadata.
- MCP tools at `/mcp` and `/mcp/sse`, federated through the hub as:
  - `databricks__list_tables`
  - `databricks__describe_table`
- Portal UX for creating and updating `Connection`, `Warehouse`, and `Table`
  handles, plus cached schema inspection.
- Multicluster controllers validate PAT credentials against the Databricks
  current-user API, validate SQL warehouse handles, refresh table schema status,
  and write `Validated` / `Ready` conditions.
- Provider controllers use the provider's accepted APIExport permission claims
  to resolve referenced credential `Secret` resources for validation only.
  Databricks credentials are never returned to App Studio, generated apps, or
  browser clients.

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

Runtime row access is intentionally not exposed by this provider yet. App Studio
and generated apps should treat Databricks `Table` resources as metadata until a
provider action contract and App Studio runtime data-access bridge exist.

The provider still posts `DESCRIBE TABLE` statements to
`/api/2.0/sql/statements` from its controllers to validate imported tables and
cache schema metadata on `Table.status`.

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

## Gaps

- Catalog/schema discovery is not implemented yet; the first UX imports a known
  table by reference.
- Generated-app runtime access is not implemented yet. Do not hardcode provider
  backend URLs or Databricks credentials into App Studio-generated source.
- OAuth federation and service-principal token exchange should be reconciled
  into token-bearing Secrets before validation or future provider actions.
