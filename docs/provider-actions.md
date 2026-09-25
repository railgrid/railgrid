# Provider Actions

Provider Actions is the catalog-backed, synchronous action contract for
server-side generated applications. Providers publish versioned action
metadata in their `CatalogEntry`; App Studio grants an exact action and
resource to a Project and materializes the grant as **kcp RBAC** on the
workload identity; an invocation is a request to a **kcp custom subresource**
`{resource}/{action}` published on the provider's APIExport, which kcp
authorizes with ordinary RBAC and reverse-proxies to the provider with the
caller's identity stamped, exactly like the infrastructure data plane's exec
verb. There is no dedicated hub action router and no hub-proxied action
route. The public contract is generic, but the only shipped action is
Databricks `query-table/v1`.

The action route grammar is the kube path of the custom subresource:

```text
POST /clusters/{clusterID}/apis/{group}/{version}/{resource}/{name}/{action}
```

The URL is the resource reference — cluster ID, resource, name, and verb are
all addressed in the path; the body carries only `{"input": {...}}`. The
action's contract version (`/v<n>` in its catalog id) is **not** in the path:
`provider-sdk/serve` restores it from the provider's declaration before
dispatching. Authentication is kcp's — the caller presents a bearer to the
kcp front door (the hub's `/clusters/{id}`), never to the provider — and
addressing is by cluster ID, so the same handler can be moved between
binaries without any consumer change.

## Catalog contract

`CatalogEntry.spec.actions` is the provider's public action catalog. Each
entry is keyed by an ID such as `query-table/v1` and declares all policy and
validation data needed by callers without exposing a provider URL or
credential model:

| Field | Meaning |
|---|---|
| `id`, `displayName`, `description` | Stable name/version plus human-facing metadata. IDs are `name/vN`. |
| `boundResource` | Exact API version, kind, and resource whose identity is supplied by the Project binding. |
| `inputSchema`, `outputSchema` | JSON Schemas for caller input and provider result. Schemas are local, bounded, and compiled by the hub. |
| `schemaDigest` | `sha256:` digest over the canonical input/output schema envelope. The hub recomputes it at catalog admission; App Studio pins it at grant time and re-verifies it on every invoke. |
| `executionMode` | `sync` (the result is the effect) or `async` (the call records intent and a controller applies it later, e.g. code's `commit/v1` creates a `RepositoryCommit` the controller then pushes). The transport is the same for both; the mode tells the caller whether to poll the bound object for the outcome. |
| `readOnly` | Provider declaration that the action does not mutate the bound resource. |
| `risk` | `low`, `medium`, or `high`, used by consent and UI policy. |
| `idempotency` | `inherent`, `keyed`, or `none`; keyed idempotency returns `501` until durable deduplication exists. |
| `limits` | Timeout, input bytes, output bytes, and result-item bounds. |
| `consent` | Whether explicit approval is required, including its prompt and scope. |
| `deprecation` | Optional deprecation message, replacement action ID, and sunset timestamp. Deprecated actions cannot receive new grants. |

The hub validates the complete declaration, canonicalizes and compiles both
schemas, and stores the normalized metadata in its provider registry. Malformed
catalog state fails closed before it can enter the action router. The
portal-facing `/api/providers` projection exposes discovery and consent
metadata, but not transport URLs.

Databricks publishes `query-table/v1` bound to
`databricks.railgrid.ai/v1alpha1 / Table / tables`. Its catalog declaration
is `sync`, `readOnly: true`, `risk: low`, `idempotency: inherent`, with a
45-second timeout, 8 KiB input cap, 64 KiB output cap, and 100 result-item
cap. Consent is not required. Its input schema permits only optional exact
`columns` (at most 64) and `limit` (1–100); its output schema contains
`actionVersion`, `tableRef`, column metadata, rows (at most 100), and an
optional `truncated` flag. The declaration's schema digest is
`sha256:9d466354d5434778c39c74123156aba76510128b0d48c5f521836770561ab853`.

### Uncatalogued large-upload verbs

Two verbs in the tree are served on the action grammar and gated exactly like a
catalogued action, yet appear in no `CatalogEntry`. Both belong to the code
provider:

- `stage-snapshot` uploads a git bundle (25 MiB decoded, 36 MiB on the wire)
  and returns an opaque `bundleRef` that the catalogued `prepare-snapshot` and
  `publish-snapshot` then name in their own small inputs.
- `stage-commit-bundle` uploads a source tree (48 MiB decoded, 68 MiB on the
  wire, 500 files) into the provider's commit-bundle store and returns the
  `bundleRef`/`bundleDigest` pair the catalogued `commit` names instead of
  inline `files`. A commit whose files fit the 1 MiB ceiling never touches it;
  App Studio, which commits whole generated applications, normally does.

They are uncatalogued because the catalog cannot describe them honestly.
`limits.maxInputBytes` is capped at 1 MiB by the CatalogEntry API itself
(`validateProviderActionLimits` in `apis/providers/v1alpha1/actions.go`, and
the CRD's `maximum: 1048576`), and the hub fails a whole CatalogEntry closed
when one declaration is malformed. Declaring 64 KiB for a 25 MiB upload would
be a lie the hub compiles and App Studio pins a schema digest over; declaring
25 MiB would be rejected, taking the provider's other fourteen actions down
with it. The honest declaration does not exist, so the verbs are documented
here instead of misdeclared there.

The exception is narrow. A verb qualifies only when all four hold:

1. it exists to carry a bounded artifact larger than the catalog's input
   ceiling, and it returns a handle rather than the artifact;
2. it is declared as a data-plane verb (`spec.dataPlane.verbs`) so it is
   published as a custom subresource and gated exactly like an action — kcp
   authorizes `{resource}/{verb}`, the provider's gate reviews `get` on the
   bound resource as the caller — so RBAC still authorizes it per verb and
   per object, and the same limits are enforced server-side;
3. what it stores is a transient artifact under the Pillar 1 carve-out in
   [provider-connectivity-contract.md](./provider-connectivity-contract.md):
   consumed-and-deleted or TTL-swept, and nothing is lost if it is gone;
4. every catalogued action that consumes the handle *is* declared, so the part
   of the flow a consumer binds to stays in the catalog.

A verb that misses any of the four is catalogued or removed. The cost of the
exception is real and intended: because they are not in the catalog, App Studio
cannot grant them through a project binding, so only a caller whose workspace
RBAC already allows `create` on `repositories/stage-snapshot` or
`repositories/stage-commit-bundle` can invoke them. A project identity that
needs to commit more than a mebibyte therefore carries the staging verb as an
explicit clause-C rule next to `repositories/commit`, and a consumer that only
ever commits small inputs carries neither.

## Project grants and audit

An App Studio Project environment stores a provider reference as
`kind: providerReference`. It is non-owning: App Studio may GET the referenced
provider object for status, but never creates, updates, deletes, or owns it.
The binding carries the exact `resourceRef` and a list of action grants:

```yaml
name: sales
provider: databricks
kind: providerReference
resourceRef:
  apiVersion: databricks.railgrid.ai/v1alpha1
  kind: Table
  resource: tables
  name: order-history
allowedActions:
  - name: query-table
    version: v1
    schemaDigest: sha256:<catalog-digest>
```

On integration create or reactivation, App Studio fetches the caller-scoped
hub catalog and requires an exact provider, action/version, bound resource,
schema digest, and non-deprecated action. If catalog consent is required,
`consentAccepted: true` is also required. The server writes
`grantedBy` and `grantedAt`; client-supplied audit values are ignored. A
revoke preserves the grant and its original digest/audit, then records
server-owned `revokedBy` and `revokedAt`. Repeated revocation is idempotent;
reactivation requires a fresh catalog verification and consent.

This is generic catalog-backed authorization, not a provider-specific App
Studio adapter. Integration CRUD is exposed under
`/services/providers/app-studio/api/projects/{project}/integrations`; invoke
uses the same alias and accepts a provider-neutral action name/version.

## Invocation and security boundary

```text
generated server application
  -> @railgrid/actions-node
  -> App Studio integration invoke
       verify persisted grant (non-revoked, complete audit)
       re-verify the grant digest against the live catalog (409 on drift)
       POST {hub}/clusters/{cluster}/apis/{group}/{version}/{resource}/{name}/{action}
  -> hub kcp proxy
       membership check, forward to kcp as the caller
  -> kcp shard
       authenticate the caller; RBAC on {resource}/{action} (the verb grant)
       reverse-proxy to the provider's DataPlaneEndpointSlice URL,
       caller stamped in X-Remote-User / X-Remote-Group / X-Remote-Extra-*
  -> provider action handler (provider-sdk/serve adapter → dataplane.Gate)
       refuse an undeclared coordinate, a request with no stamped caller
       gate: SubjectAccessReview for get on {resource}/{name} on the caller's
             behalf, then read the object as the provider (export VW)
       enforce the declared input schema, byte/result/time limits
       return the stable envelope with a bounded JSON result
```

Authorization is kcp RBAC, uniform for every caller class. A human invokes an
action iff their workspace RBAC allows the action's custom subresource
(`tables/query-table`), the same way exec on a sandbox is authorized. A
workload identity carries exactly the rules App Studio's grants materialized
— granting an action *is* writing the RBAC rule, revoking it removes the
rule. The subresource is served by the shard's reverse proxy; the provider
never sees a bearer.

### The rule is spelled `verbs: ["*"]`

kcp maps the **HTTP method** onto the RBAC verb on a custom subresource (a
POST is `create`, a GET is `get`, a WebSocket upgrade or a proxying verb may
be any of them), so a grant on the coordinate `{resource}/{action}` is minted
with verbs `*`, name-scoped to the addressed object. That is what the hub
writes when it materializes a workload-identity grant
(`pkg/hub/serviceaccounts/workload_identity.go`) and what clause C of the
scoped-identity policy mints whatever verbs a requester spells
(`pkg/hub/identity/policy.go`); a `composes[]` claim on a `{resource}/{verb}`
is written `verbs: ["*"]` for the same reason. The coordinate is the grant;
the method is not something a caller chooses.

`invoke` is **not** a verb anywhere. It was a bug — a dialect that grew up in
provider code and in the planner's `examples/consumer-rbac.yaml`, never in
the hub, where it silently broke every workload identity. It is removed: no
provider reviews it, and no RBAC grants it. (Before the subresource became
the only transport, the provider itself reviewed `create`; that review is
gone with the hub-proxied route, and the grant is kcp's alone.)

### The gate is a SubjectAccessReview, then a read as the provider

The provider holds the caller's name and groups and **no credential**, so
visibility is proven with a `SubjectAccessReview` — not the *Self* variant —
for `get` on `{resource}/{name}` on the caller's behalf, and only then does
the handler read the object **as the provider**, through its APIExport
virtual workspace (`dataplane.Gate`). It still has to read: a review can pass
for an object that does not exist or is being deleted, and the handler needs
the object to pin the UID and the spec it is about to act on. A
`deletionTimestamp` denies. Every probe-shaped failure is a `404`. The
export must claim `authorization.k8s.io/subjectaccessreviews` (`create`,
tenantScoped) for kcp to serve the review through the virtual workspace.

### The cluster is in the path, and only there

The cluster ID appears once, in the path (`/clusters/{clusterID}/...`) — the
segment kcp routed on. There is no `X-Railgrid-Cluster` on an action: those
headers belong to the hub's backend proxy, which carries no verbs. A
component of a multi-component object is the `component` query parameter.

### Limits and the envelope

The provider enforces its own declared limits — it authored them, and the
catalog's fail-closed validation guarantees the declaration is well-formed.
Caller cancellation and the declared action timeout bound the synchronous
request. The stable response envelope carries `requestID`, provider,
action/version, the bound resource reference, and either `result` or a typed
error (`code`, `message`, `retryable`); App Studio validates the envelope
identity against the bound grant before relaying it.

### Typed provider failures

Provider action failures are a deliberately small, typed boundary. Databricks
must return an allowlisted `code`, a safe bounded `message`, and an explicit
`retryable` boolean; callers must not infer retryability from the HTTP status.
The provider's handler enforces the code/status compatibility table and
sanitizes messages before anything reaches the wire, and it stamps the
envelope identity (`requestID`, provider, action/version, route-derived
`resourceRef`) itself — the route, not the body, is what was authorized. App
Studio then validates that envelope identity against the bound grant and
refuses a response whose identity does not match.

For `query-table/v1`, an unknown column in the exact bound Table is
`schema_projection_invalid`, HTTP 400, and non-retryable. A Databricks
dependency authentication failure is normalized to `backend_failure` at the
gateway (HTTP 502, non-retryable), rather than being exposed as the caller's
HTTP 401. Transient backend/dependency failures may use HTTP 503 with
`retryable: true`; this remains an explicit provider decision.

Malformed, unsafe, unknown-code, status-incompatible, or over-bound typed
errors collapse to the generic `action_failed` failure at the provider
boundary, without raw provider details. The server SDK surfaces accepted
provider failures as a stable `ProviderActionError` (`code`, safe `message`,
`retryable`, request metadata, and binding metadata).

Provider authors should keep this boundary provider-neutral: choose only the
published codes, sanitize messages before writing them, set retryability from
the actual failure policy, and never include credentials, URLs, SQL, tenant
paths, or backend resource details. Application authors should branch on the
typed `code` and `retryable` fields, repair permanent input/schema failures,
and retry only bounded, idempotent transient failures.

There is deliberately no hub invoke route: the custom subresource on the kcp
front door is the public data-plane surface, and calling it directly is
legitimate — kcp's RBAC on the coordinate and the provider's gate are the
enforcement, so "bypassing App Studio" gains a caller nothing kcp RBAC does
not already allow. What the backend proxy does reserve is the **hub-only** prefix
`/workload-identities/*` on every provider backend: the attestation endpoint
must never be reachable with a caller bearer, where it would act as a
TokenReview oracle. App Studio's invoke gateway adds the consumer-side value
on top: revocation is refused immediately (before RBAC reconciliation catches
up on the next exchange), and the persisted grant digest is re-verified
against the live catalog on every invoke, returning `409` on drift.

Workload callers — today minted for the development runtime — use the
workload exchange and a short-lived workload capability:

1. The development runtime reads a projected bootstrap token, posts the exact
   tenant/project/project UID/environment/instance tuple to
   `/api/provider-actions/workload/exchange`, and never exposes that bootstrap
   token to the generated application.
2. The hub asks the Infrastructure provider to perform online attestation at
   `/workload-identities/review` on its backend origin (a hub-only reserved
   prefix the backend proxy refuses to serve). Infrastructure performs an
   audience-bound TokenReview and verifies the pod identity and exact runtime
   tuple.
3. The hub verifies the live Project environment, instance, and provider
   resource references, then issues a short-lived Railgrid ServiceAccount token
   whose ClusterRole carries GET on each granted resource plus `create` on
   each granted action's virtual subresource — the RBAC materialization of
   the Project's action grants. The current token TTL is ten minutes and the
   token is not persisted in a Secret or annotation. Grant changes reconcile
   on the next exchange; the five-field identity tuple is immutable for the
   ServiceAccount's lifetime.
4. The runtime atomically refreshes a mode-`0600` token file. The generated
   server reads that file on each request, or uses a refreshable credential
   provider; a single `401` triggers one forced refresh.

The SDK is server-only. Its base URL must be absolute HTTPS; HTTP is allowed
only for an explicit loopback test override. Do not pass provider URLs,
provider credentials, resource coordinates, or raw SQL in action input. The
runtime's `RAILGRID_ACTIONS_CA_FILE` can add an explicitly configured CA for the
workload exchange, but the source does not provide automatic custom-CA
distribution. Production external URLs therefore require HTTPS with a
system- or publicly-trusted certificate unless deployment configuration
explicitly supplies the CA.

## Server-side SDK

The published artifact is `@crwilhit/railgrid-actions-node@0.1.0`. Generated
server components must install it under the stable consumer name with this
exact npm alias in their `package.json`; the artifact name and import name are
intentionally different:

```json
{
  "dependencies": {
    "@railgrid/actions-node": "npm:@crwilhit/railgrid-actions-node@0.1.0"
  }
}
```

Use the generic `integration(alias).invoke` API with the stable consumer import.
The SDK never exposes a provider-specific convenience method:

```js
import { createActionsClient } from '@railgrid/actions-node';

const railgrid = createActionsClient({
  baseURL: process.env.RAILGRID_ACTIONS_BASE_URL,
  project: process.env.RAILGRID_PROJECT,
  tokenFile: process.env.RAILGRID_ACTIONS_TOKEN_FILE,
});

const result = await railgrid.integration('sales').invoke(
  'query-table/v1',
  { columns: ['order_id', 'total'], limit: 25 },
  { requestID: 'request-42', timeoutMs: 10_000 },
);
console.log(result);
```

`tokenFile` defaults to `RAILGRID_ACTIONS_TOKEN_FILE`; it is read for every
request. A `getToken`/credential provider receives `{ forceRefresh, signal }`
and is retried once after an HTTP `401`. The SDK propagates caller aborts and
local timeouts, rejects browser globals, and returns typed transport or
provider-action errors. There is no development-token fallback.

### Development sandbox delivery

The Infrastructure `railgrid-dev-agent` supplies only the coordinator, runtime
supervisor, executor, and preview-bridge assets. It does not copy, validate,
or mount the Actions SDK. Development components run their normal package
manager against the exact alias in the server `package.json`, writing
dependencies into the shared workspace used by the app and executor. This
keeps the development path aligned with production publication and preserves
the server-only credential boundary; browser components must not import the
SDK or receive its token.

## Databricks implementation

`POST /clusters/{clusterID}/apis/{group}/{version}/tables/{name}/query-table`
is the primary app path, the custom subresource kcp forwards to the provider.
The handler derives the resource reference from the route, then the
request-scoped executor performs delegated authorization for the stamped
caller — a SubjectAccessReview for `get` on the exact imported Table
(visibility), kcp having already authorized the `tables/query-table`
subresource (the verb grant) — before resolving
`Table → Warehouse → Connection → Secret` with provider authority. It
requires current Table/Warehouse `Ready` and Connection `Validated`/`Ready`
conditions, checks the connection references and PAT auth type, then builds a
quoted projection and bounded `SELECT`. It never creates a query resource and
never persists result rows in control-plane status. Provider and credential
details are sanitized from errors.

The optional `/mcp` and `/mcp/sse` surfaces are controlled by
`DATABRICKS_MCP_ENABLED` (enabled by default for compatibility). When enabled,
the MCP `query-table` tool reuses the same request-scoped executor; it is an
optional presentation adapter and is not required by the primary generated-app
action path. Setting `DATABRICKS_MCP_ENABLED=false` leaves direct actions
available.

The provider accepts only the imported Table resource reference, exact column
identifiers, and a limit from 1 through 100. SQL text, hosts, warehouse IDs,
connection handles, and credentials are not caller inputs. The backend uses
the Databricks SQL Statements API and the provider's configured host allowlist.

## Observability and residual limits

With the hub router removed, observability lives where enforcement lives: the
provider logs each action failure with request ID, action identity, outcome
code, error class, and duration — never prompt text, raw input, credentials,
or sensitive backend values. The backend proxy provides the transport-level
request log. When provider-side action metrics are added, keep labels
low-cardinality: provider, action, version, outcome, and error class only —
never tenant IDs, project names, resource names, digests, or arbitrary error
text.

The transport is synchronous and bounded. There are no durable jobs, progress
streams, or resume handles. The portal and grant contract require an exact
resource reference; resource-name discovery is not a picker supplied by the
action transport. Enforcement of declared limits and schemas is per-provider
(the databricks handler is the reference); extracting that into a shared
server-kit package is the planned next step in
[cross-provider-simplification.md](./cross-provider-simplification.md). Only
`query-table/v1` is shipped today.

## Verification commands

The deterministic suite runs the embedded hub, Infrastructure attestation
fixture, App Studio, Databricks, a local TLS fake, and a generated Node app.
It exchanges a workload token, writes it to a token file, disables Databricks
MCP, and verifies direct `/actions` routing, exact Project grants, digest drift,
tenant isolation, bounded results, and credential non-disclosure:

```bash
make e2e-provider-actions
```

SDK unit tests:

```bash
cd provider-sdk/actions-node && npm test
```

The registry-backed clean-install smoke is opt-in because it needs network
access and the published artifact to exist. It stages the generated server
manifest in a fresh directory, installs the exact alias from npm, and imports
`@railgrid/actions-node`:

```bash
make e2e-provider-actions-npm
```

The target sets `RAILGRID_E2E_PROVIDER_ACTIONS_LIVE_ONLY=true` so the smoke does
not start the full hub/provider stack. Set
`RAILGRID_E2E_PROVIDER_ACTIONS_NPM_REGISTRY` first when using a registry mirror.

The opt-in live command reads an already-refreshed workload token file. Set
`RAILGRID_E2E_PROVIDER_ACTIONS_LIVE=true`, `RAILGRID_LIVE_HUB_URL`,
`RAILGRID_LIVE_PROJECT`, and `RAILGRID_LIVE_ACTIONS_TOKEN_FILE` (optionally
`RAILGRID_LIVE_ACTION_ALIAS`, `RAILGRID_LIVE_ORG`, and `RAILGRID_LIVE_WORKSPACE`):

```bash
make e2e-provider-actions-live
```

These are verification commands; this document does not claim that a current
deterministic or live run has passed.

Implementation anchors: [CatalogEntry action types](../apis/providers/v1alpha1/types_catalogentry.go),
[data-plane action handler and route grammar](https://github.com/railgrid/providers/blob/main/providers/databricks/actions/actions.go),
[caller authorization (visibility review as the stamped caller)](https://github.com/railgrid/providers/blob/main/providers/databricks/tenant/action.go),
[hub-only proxy reservations](../pkg/hub/providers/proxy.go),
[hub workload exchange](../pkg/hub/workloadidentity/workloadidentity.go),
[action-grant RBAC materialization](../pkg/hub/serviceaccounts/workload_identity.go),
[App Studio grant verification and invoke-time digest re-check](../providers/app-studio/api/provider_action_catalog.go),
[App Studio forwarding](../providers/app-studio/api/integrations.go),
[Databricks backend error normalization](https://github.com/railgrid/providers/blob/main/providers/databricks/backend/backend.go), and
[server-only SDK](../provider-sdk/actions-node/index.mjs).
