# Databricks provider

> [!IMPORTANT]
> **Read-only mirror — do not push or open PRs here.**
> The standalone [`faroshq/provider-databricks`](https://github.com/faroshq/provider-databricks)
> repository is **automatically synced** from the
> [`faroshq/faros`](https://github.com/faroshq/faros) monorepo
> (path `providers/databricks/`) via
> [splitsh-lite](https://github.com/splitsh/lite). Every sync force-updates
> the mirror, so direct changes here are overwritten. File issues and PRs
> against [`faroshq/faros`](https://github.com/faroshq/faros) instead.
> See the [provider publishing documentation](https://github.com/faroshq/faros/blob/main/docs/provider-publishing.md)
> for details.

A faros provider that exposes imported Databricks SQL Warehouse tables to faros
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

## Runtime lifecycle and deployment

The provider exposes separate process and dependency probes:

- `/healthz` is liveness. It answers while the HTTP process can serve, even
  while the controller is starting or recovering.
- `/readyz` is readiness. In the chart's default
  `DATABRICKS_CONTROLLER_MODE=required` mode it stays `503` until the
  multicluster controller manager has started and its dependency prerequisites
  are available. A manager exit clears readiness; startup retries use
  `DATABRICKS_CONTROLLER_RETRY_INTERVAL` (default `15s`).
- Heartbeats are eligible only while the required controller is ready. This
  prevents a provider that can answer HTTP but cannot reconcile tenant objects
  from being advertised as healthy. `DATABRICKS_CONTROLLER_MODE=rest-only`
  explicitly disables controller startup for local REST/UI work; readiness and
  heartbeat then describe that intentional mode.

The Helm chart makes bootstrap ownership explicit. The default
`bootstrap.mode=init` mounts the provider-workspace kubeconfig and runs the
image's `init` command before serving; it also installs the rendered
`CatalogEntry`. Set `bootstrap.mode=external` (or the compatibility switch
`bootstrap.enabled=false`) and `catalogEntry.enabled=false` only when an
external operator/GitOps process has already installed the APIExport, endpoint
slice, schemas, and CatalogEntry. The kubeconfig Secret is required in both
modes. The chart probe and CatalogEntry health path are both `/readyz`.

Chart configuration exposes the MCP and host policy explicitly:

- `mcp.enabled` controls the optional `/mcp` and `/mcp/sse` adapters.
- `mcp.disableLocalhostProtection` is a local-development-only escape hatch
  and defaults to `false`.
- `allowedHostSuffixes` renders `DATABRICKS_ALLOWED_HOST_SUFFIXES`; leave it
  empty for the standard Databricks suffix allowlist and add private suffixes
  only deliberately.

The release tag is the single provider version: the Makefile and Dockerfile
inject it into `main.buildVersion`, the chart uses it as `appVersion`, and the
chart passes it as `FAROS_PROVIDER_VERSION` so the heartbeat reports the same
release value.

## Creating a connection

A `Connection` needs two values, both taken from the Databricks workspace you
want to attach:

- **Workspace host** — the URL your browser shows when you are logged into that
  workspace: `https://dbc-….cloud.databricks.com` (AWS),
  `https://adb-….azuredatabricks.net` (Azure) or `https://….gcp.databricks.com`
  (GCP). Scheme and host only, no path.
- **Token** — a Databricks personal access token, created in that workspace via
  the avatar menu → Settings → Developer, then Manage on the "Access tokens"
  card → Generate new token. The token's identity needs `SELECT` on the
  catalogs and schemas you plan to import tables from, plus access to a running
  SQL warehouse for `query_table/v1`.

The token is stored as a `Secret` in the tenant workspace; the provider
validates it against the Databricks current-user API and stamps the
connection's `Validated` condition with the result.

## Adding a warehouse

A `Warehouse` is a handle to one Databricks SQL warehouse under an existing
connection; imported tables use it to run `query_table/v1`.

- **Warehouse ID** — in the Databricks workspace: SQL → SQL Warehouses → open
  the warehouse. The ID is the 16-character hex value shown on its overview
  page and is also the last segment of the HTTP path under Connection details
  (`/sql/1.0/warehouses/<id>`). It is not the long numeric `?o=` value in the
  browser URL — that is the workspace org ID, and pasting it fails validation
  with `404 Not Found`.
- The connection's token identity needs the warehouse's "Can use" permission;
  the warehouse must be startable (serverless or with auto-start) for queries
  to succeed.

The provider validates the handle against the Databricks warehouses API and
stamps the warehouse's `Ready` condition with its state.

## Importing warehouses and tables

`New warehouse` and `New table` are split actions. The import wizard proceeds
through **Source**, **Browse**, **Review**, and **Results**. On Source, the user
selects a Connection; table imports also select a registered query Warehouse on
that same Connection. The selected Connection is not a node in the Browse
tree. The adjacent menu keeps **Enter manually** available for exact-ID,
advanced, and recovery workflows.

Browse is a lazy, paginated tree. Warehouse imports show warehouses as leaf
nodes. Table imports use the hierarchy **Catalog → Schema → Table**; each root
or branch is expanded and paged independently. Unsupported and already
registered resources are disabled. Selecting a branch resolves all eligible
descendant leaves in a private snapshot before changing the visible selection;
it succeeds only when the complete branch fits within the remaining slots of
the 50-resource batch limit and fails closed on overflow or incomplete
discovery, without partial selection.
The user then reviews generated Faros resource names before the single bounded
registration request is sent after final confirmation.

Discovery returns metadata only; credentials remain in the tenant Secret and
row data is never fetched. Registration acts as the caller, reports a result
for every selected item, treats an exact existing spec as idempotent, and never
overwrites a conflicting resource. Table registration additionally proves the
selected Warehouse is visible to the caller and belongs to the same Connection
before creating any Table. A user or admin can also create the tenant resources
directly:

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
apiVersion: databricks.faros.sh/v1alpha1
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
apiVersion: databricks.faros.sh/v1alpha1
kind: Warehouse
metadata:
  name: sales-warehouse
spec:
  connectionRef: sales-workspace
  warehouseID: "abc123def456"
---
apiVersion: databricks.faros.sh/v1alpha1
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
table), invoke only from a server route through `@faros/actions-node`, honor the
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

- Generated apps must call `query_table/v1` through the hub Provider Actions
  route and the server-only SDK; do not hardcode provider backend URLs or
  Databricks credentials into App Studio-generated source. MCP is optional and
  is not required by this app path.
- OAuth federation and service-principal token exchange should be reconciled
  into token-bearing Secrets before validation or future provider actions.

## Running it yourself

This provider can run in your own cluster instead of on the platform. faros
creates a workspace for it in your organization, mints a credential scoped to
that workspace alone, and generates the exact `helm` commands — under
**Providers → Self-Hosting** in the portal.

Nothing to fill in by faros. Databricks workspace credentials are supplied per
`Connection` at runtime, not as chart values.

Once installed, the provider registers itself and your workspaces enable it
exactly like the platform copy. See
[docs/byo-providers.md](../../docs/byo-providers.md) for how the flow works, and
[deploy/chart/README.md](deploy/chart/README.md) for every chart value.
