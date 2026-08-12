# Provider Actions

Provider Actions is the catalog-backed, synchronous action contract for
server-side generated applications. Providers publish versioned action
metadata in their `CatalogEntry`; App Studio grants an exact action and
resource to a Project and materializes the grant as **kcp RBAC** on the
workload identity; invocations ride the ordinary hub backend proxy to the
provider's **embedded virtual workspace** — a resource-addressed data-plane
route the provider itself serves and authorizes with caller-scoped
SelfSubjectAccessReviews, exactly like the infrastructure data plane's exec
verb. There is no dedicated hub action router. The public contract is
generic, but the only shipped action is Databricks `query_table/v1`.

The action route grammar is the platform data-plane grammar:

```text
POST /services/providers/{provider}/actions/clusters/{clusterID}/{resource}/{name}/{action}/{version}
```

The URL is the resource reference — cluster ID, resource, name, and verb are
all addressed in the path; the body carries only `{"input": {...}}`. Because
authentication is the caller's bearer validated against kcp (never
proxy-injected headers) and addressing is by cluster ID, the same handler can
later be split out of the provider binary or promoted to a real kcp virtual
workspace without any consumer change.

## Catalog contract

`CatalogEntry.spec.actions` is the provider's public action catalog. Each
entry is keyed by an ID such as `query_table/v1` and declares all policy and
validation data needed by callers without exposing a provider URL or
credential model:

| Field | Meaning |
|---|---|
| `id`, `displayName`, `description` | Stable name/version plus human-facing metadata. IDs are `name/vN`. |
| `boundResource` | Exact API version, kind, and resource whose identity is supplied by the Project binding. |
| `inputSchema`, `outputSchema` | JSON Schemas for caller input and provider result. Schemas are local, bounded, and compiled by the hub. |
| `schemaDigest` | `sha256:` digest over the canonical input/output schema envelope. The hub recomputes it at catalog admission; App Studio pins it at grant time and re-verifies it on every invoke. |
| `executionMode` | `sync` or `async`; the current hub transport accepts `sync` only. |
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

Databricks publishes `query_table/v1` bound to
`databricks.faros.sh/v1alpha1 / Table / tables`. Its catalog declaration
is `sync`, `readOnly: true`, `risk: low`, `idempotency: inherent`, with a
45-second timeout, 8 KiB input cap, 64 KiB output cap, and 100 result-item
cap. Consent is not required. Its input schema permits only optional exact
`columns` (at most 64) and `limit` (1–100); its output schema contains
`actionVersion`, `tableRef`, column metadata, rows (at most 100), and an
optional `truncated` flag. The declaration's schema digest is
`sha256:9d466354d5434778c39c74123156aba76510128b0d48c5f521836770561ab853`.

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
  apiVersion: databricks.faros.sh/v1alpha1
  kind: Table
  resource: tables
  name: order-history
allowedActions:
  - name: query_table
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
  -> @faros/actions-node
  -> App Studio integration invoke
       verify persisted grant (non-revoked, complete audit)
       re-verify the grant digest against the live catalog (409 on drift)
       POST /services/providers/{provider}/actions/clusters/{cluster}/{resource}/{name}/{action}/{version}
  -> hub backend proxy
       strip + re-inject X-Faros-* identity hints, forward the bearer
  -> provider action handler (embedded virtual workspace)
       parse identity from the route; bearer is the only trust root
       gate 1: SSAR get on the addressed resource, as the caller
       gate 2: SSAR create on {resource}/{action} — the verb grant — as the caller
       enforce the declared input schema, byte/result/time limits
       return the stable envelope with a bounded JSON result
```

Authorization is kcp RBAC, uniform for every caller class. A human invokes an
action iff their workspace RBAC allows `create` on the action's virtual
subresource (`tables/query_table`), the same way exec on a sandbox is
authorized. A workload identity carries exactly the rules App Studio's grants
materialized — granting an action *is* writing the RBAC rule, revoking it
removes the rule. The subresource is an RBAC coordinate only; no API server
serves it.

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

For `query_table/v1`, an unknown column in the exact bound Table is
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

There is deliberately no hub invoke route: the action route through the
backend proxy is the public data-plane surface, and calling it directly is
legitimate — the provider's caller-scoped SSAR gates are the enforcement, so
"bypassing App Studio" gains a caller nothing kcp RBAC does not already
allow. What the proxy does reserve is the **hub-only** prefix
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
   resource references, then issues a short-lived Faros ServiceAccount token
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
runtime's `FAROS_ACTIONS_CA_FILE` can add an explicitly configured CA for the
workload exchange, but the source does not provide automatic custom-CA
distribution. Production external URLs therefore require HTTPS with a
system- or publicly-trusted certificate unless deployment configuration
explicitly supplies the CA.

## Server-side SDK

The published artifact is `@crwilhit/faros-actions-node@0.1.0`. Generated
server components must install it under the stable consumer name with this
exact npm alias in their `package.json`; the artifact name and import name are
intentionally different:

```json
{
  "dependencies": {
    "@faros/actions-node": "npm:@crwilhit/faros-actions-node@0.1.0"
  }
}
```

Use the generic `integration(alias).invoke` API with the stable consumer import.
The SDK never exposes a provider-specific convenience method:

```js
import { createActionsClient } from '@faros/actions-node';

const faros = createActionsClient({
  baseURL: process.env.FAROS_ACTIONS_BASE_URL,
  project: process.env.FAROS_PROJECT,
  tokenFile: process.env.FAROS_ACTIONS_TOKEN_FILE,
});

const result = await faros.integration('sales').invoke(
  'query_table/v1',
  { columns: ['order_id', 'total'], limit: 25 },
  { requestID: 'request-42', timeoutMs: 10_000 },
);
console.log(result);
```

`tokenFile` defaults to `FAROS_ACTIONS_TOKEN_FILE`; it is read for every
request. A `getToken`/credential provider receives `{ forceRefresh, signal }`
and is retried once after an HTTP `401`. The SDK propagates caller aborts and
local timeouts, rejects browser globals, and returns typed transport or
provider-action errors. There is no development-token fallback.

### Development sandbox delivery

The Infrastructure `faros-dev-agent` supplies only the coordinator, runtime
supervisor, executor, and preview-console assets. It does not copy, validate,
or mount the Actions SDK. Development components run their normal package
manager against the exact alias in the server `package.json`, writing
dependencies into the shared workspace used by the app and executor. This
keeps the development path aligned with production publication and preserves
the server-only credential boundary; browser components must not import the
SDK or receive its token.

## Databricks implementation

`POST /actions/clusters/{clusterID}/tables/{name}/query_table/v1` is the
primary app path, mounted under the provider's `/actions/` data-plane root.
The handler derives the resource reference from the route, then the
request-scoped executor performs delegated authorization as the caller — an
SSAR `get` on the exact imported Table (visibility) and an SSAR `create` on
the `tables/query_table` subresource (the verb grant) — before resolving
`Table → Warehouse → Connection → Secret` with provider authority. It
requires current Table/Warehouse `Ready` and Connection `Validated`/`Ready`
conditions, checks the connection references and PAT auth type, then builds a
quoted projection and bounded `SELECT`. It never creates a query resource and
never persists result rows in control-plane status. Provider and credential
details are sanitized from errors.

The optional `/mcp` and `/mcp/sse` surfaces are controlled by
`DATABRICKS_MCP_ENABLED` (enabled by default for compatibility). When enabled,
the MCP `query_table` tool reuses the same request-scoped executor; it is an
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
`query_table/v1` is shipped today.

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
`@faros/actions-node`:

```bash
make e2e-provider-actions-npm
```

The target sets `FAROS_E2E_PROVIDER_ACTIONS_LIVE_ONLY=true` so the smoke does
not start the full hub/provider stack. Set
`FAROS_E2E_PROVIDER_ACTIONS_NPM_REGISTRY` first when using a registry mirror.

The opt-in live command reads an already-refreshed workload token file. Set
`FAROS_E2E_PROVIDER_ACTIONS_LIVE=true`, `FAROS_LIVE_HUB_URL`,
`FAROS_LIVE_PROJECT`, and `FAROS_LIVE_ACTIONS_TOKEN_FILE` (optionally
`FAROS_LIVE_ACTION_ALIAS`, `FAROS_LIVE_ORG`, and `FAROS_LIVE_WORKSPACE`):

```bash
make e2e-provider-actions-live
```

These are verification commands; this document does not claim that a current
deterministic or live run has passed.

Implementation anchors: [CatalogEntry action types](../apis/providers/v1alpha1/types_catalogentry.go),
[data-plane action handler and route grammar](../providers/databricks/actions/actions.go),
[two-gate caller authorization (visibility + verb SSAR)](../providers/databricks/tenant/action.go),
[hub-only proxy reservations](../pkg/hub/providers/proxy.go),
[hub workload exchange](../pkg/hub/workloadidentity/workloadidentity.go),
[action-grant RBAC materialization](../pkg/hub/serviceaccounts/workload_identity.go),
[App Studio grant verification and invoke-time digest re-check](../providers/app-studio/api/provider_action_catalog.go),
[App Studio forwarding](../providers/app-studio/api/integrations.go),
[Databricks backend error normalization](../providers/databricks/backend/backend.go), and
[server-only SDK](../provider-sdk/actions-node/index.mjs).
