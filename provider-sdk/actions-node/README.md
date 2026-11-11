# `@railgrid/actions-node`

This server-only SDK invokes an App Studio Project's saved integration through
the project's cluster-qualified kcp subresource. Pin `@railgrid/actions-node`
in the server component's `package.json`:

```json
{
  "dependencies": {
    "@railgrid/actions-node": "0.2.3"
  }
}
```

To migrate an existing app from the old npm alias, update the server manifest
and lockfile through the app's normal dependency update flow. Keep the import
path `@railgrid/actions-node`.

App Studio injects `RAILGRID_ACTIONS_BASE_URL` from trusted Project context. It
has the form
`https://<hub>/clusters/<cluster>/apis/ai.railgrid.ai/v1alpha1/projects/<project>/integration-actions`.
The SDK appends only the saved integration alias; it does not accept a provider
URL, resource reference, tenant scope, or backend topology.

```js
import { createActionsClient } from '@railgrid/actions-node';

const railgrid = createActionsClient({
  baseURL: process.env.RAILGRID_ACTIONS_BASE_URL,
  // App Studio atomically refreshes this short-lived workload credential.
  tokenFile: process.env.RAILGRID_ACTIONS_TOKEN_FILE,
});

const rows = await railgrid.integration('sales').invoke('query_table/v1', {
  columns: ['order_id', 'total'],
  limit: 25,
});
```

The SDK sends `POST .../integration-actions/<alias>` with
`{ action, actionVersion, input }`. The integration alias selects the saved
Project binding. The App Studio handler checks the non-revoked action grant,
rechecks its schema digest against the live catalog, authorizes the caller, and
then forwards the action through the provider's kcp custom subresource. The
provider URL and resource reference never come from application input.

The credential coordinator reports whether the short-lived workload credential
was issued and remains unexpired. That alone does not prove the saved
integration is reachable or authorized. A successful `invoke` verifies the
whole route; an `ActionsClientError.failureKind` distinguishes `route`,
`authentication`, `authorization`, `contract`, `upstream`, `network`,
`credentials`, and local `configuration` failures.

Validated provider error envelopes have `failureKind: 'upstream'` and retain
their domain error code and HTTP status. For example, a provider's
`identity_conflict` with HTTP 409 is distinct from a gateway contract failure.

Never import the module into browser code, expose its token through
client-side configuration, or pass provider URLs, credentials, resource
references, or other topology in action input. The SDK throws when `window` or
`document` is present as a defense against accidental browser bundling.

## Credentials and retries

Use an atomically refreshed token file, a static workload token, or a
refreshable credential provider. A provider is called with
`{ forceRefresh, signal }` and is called again with `forceRefresh: true` after
one gateway HTTP 401 without a provider envelope. A provider's own 401 error
is returned without replaying the action:

```js
const railgrid = createActionsClient({
  baseURL: process.env.RAILGRID_ACTIONS_BASE_URL,
  getToken: ({ forceRefresh }) => tokenStore.get({ forceRefresh }),
});
```

When `tokenFile` is omitted, the SDK reads `RAILGRID_ACTIONS_TOKEN_FILE` on
every request. Do not point it at the coordinator-only bootstrap token.
`baseURL` also defaults to `RAILGRID_ACTIONS_BASE_URL`. Org, workspace, and
project options are intentionally absent: the trusted URL is already scoped to
the current Project and the bearer is the authority.

Requests can carry cancellation, timeout, idempotency, and tracing metadata:

```js
const value = await railgrid.integration('sales').invoke('lookup/v1', { key: 'order-1' }, {
  signal: request.signal,
  timeoutMs: 10_000,
  idempotencyKey: 'job-42-attempt-1',
  requestID: 'request-42',
  actionDeadlineMs: 15_000,
});
```

`invoke` returns the action result. `invokeEnvelope` returns the validated
stable envelope (`requestID`, provider, action/version, bound `resourceRef`,
and result). A provider failure throws `ProviderActionError` with its stable
code, message, retryability, and binding metadata. Transport and configuration
failures throw `ActionsClientError` with a machine-readable `code`, HTTP status,
and `failureKind`.

## Development sandboxes

The server component's manifest owns this dependency, so development and
production use the same declared package source. Infrastructure installs the
declared dependency through the normal package install and reload flow; the
platform-owned `railgrid-dev-agent` supplies the coordinator and runtime
supervisor only. It does not copy, validate, or mount this SDK.

When adding the SDK to an existing customized application, App Studio must
show the exact package manifest change for review before applying it. It must
not silently edit or replace an existing `package.json`.

## Release

Publish and verify the organization-scoped package before deploying App Studio
with the new pin or distributing the updated skill. For the first release, an
npm maintainer with write access to the `railgrid` scope publishes from this
directory with `npm publish --access public`. Verify a clean install of
the published version from the public registry.

Once the package exists, configure `railgrid/railgrid` and workflow filename
`actions-node-release.yaml` as its npm trusted publisher with direct publish
enabled. For subsequent releases, the workflow runs on
`actions-node/v<version>` tags matching `package.json`. It tests the packed
artifact in a clean consumer, publishes with provenance using GitHub OIDC, and
verifies a direct registry install.
