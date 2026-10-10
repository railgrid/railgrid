# `@crwilhit/railgrid-actions-node`

This server-only SDK invokes an App Studio Project's saved integration through
the project's cluster-qualified kcp subresource. Generated applications import
the stable consumer name, `@railgrid/actions-node`. Version 0.2.0 is currently
available as a locally packed tarball; public npm publication is pending. Atlas
uses the reviewed tarball at `api/vendor/railgrid-actions-node-0.2.0.tgz` with
`"@railgrid/actions-node": "file:vendor/railgrid-actions-node-0.2.0.tgz"`.
After registry publication, consumers can use this npm alias:

```json
{
  "dependencies": {
    "@railgrid/actions-node": "npm:@crwilhit/railgrid-actions-node@0.2.0"
  }
}
```

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

Never import the module into browser code, expose its token through
client-side configuration, or pass provider URLs, credentials, resource
references, or other topology in action input. The SDK throws when `window` or
`document` is present as a defense against accidental browser bundling.

## Credentials and retries

Use an atomically refreshed token file, a static workload token, or a
refreshable credential provider. A provider is called with
`{ forceRefresh, signal }` and is called again with `forceRefresh: true` after
one HTTP 401:

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
production use the same declared package source. Atlas currently uses the
reviewed local tarball while registry publication is pending. Infrastructure
installs the declared dependency through the normal package install and reload
flow; the platform-owned `railgrid-dev-agent` supplies the coordinator and
runtime supervisor only. It does not copy, validate, or mount this SDK.

When adding the SDK to an existing customized application, App Studio must
show the exact package manifest change for review before applying it. It must
not silently edit or replace an existing `package.json`.

## Release

The GitHub Actions workflow publishes from tags named
`actions-node/v<version>`. The tag must exactly match `package.json`. The npm
package configures that workflow as a trusted publisher; no long-lived npm
token is stored in the repository. The workflow runs the unit suite, installs
the packed artifact in a clean consumer under the public alias, publishes with
provenance, and verifies the registry alias install.

The first public version is the bootstrap exception: npm cannot attach a
trusted publisher until the package exists. A maintainer must authenticate with
npm and publish that first version from the reviewed package directory, then
configure `railgrid/railgrid` and `actions-node-release.yaml` as the package's
trusted publisher before creating subsequent release tags.
