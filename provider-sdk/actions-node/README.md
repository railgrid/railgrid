# `@crwilhit/faros-actions-node`

`@crwilhit/faros-actions-node` is the published server-only SDK artifact for
generated App Studio applications. Consumers intentionally install it under
the stable `@faros/actions-node` import name with this exact npm alias:

```json
{
  "dependencies": {
    "@faros/actions-node": "npm:@crwilhit/faros-actions-node@0.1.0"
  }
}
```

The package invokes a project integration through App Studio's authenticated
gateway; the gateway selects the provider and bound resource:

```js
import { createActionsClient } from '@faros/actions-node';

const faros = createActionsClient({
  baseURL: process.env.FAROS_ACTIONS_BASE_URL,
  project: process.env.FAROS_PROJECT,
  // The coordinator atomically refreshes this file; the SDK reads it for
  // every request so an in-flight workload never needs the bootstrap token.
  tokenFile: process.env.FAROS_ACTIONS_TOKEN_FILE,
});

const rows = await faros.integration('sales').invoke('query_table/v1', {
  columns: ['order_id', 'total'],
  limit: 25,
});
```

The credential is sent only by the server-side process. Do not import this
module into browser code, expose its token through client-side configuration,
or pass provider URLs, credentials, resource references, or other topology in
action input. The SDK throws when `window` or `document` is present as a
defense against accidental browser bundling.

## Installation and development sandboxes

The published artifact is installed through the exact alias shown above. Keep
the alias in the server component's `package.json` and keep application code on
the stable consumer import:

```js
import { createActionsClient } from '@faros/actions-node';
```

App Studio development sandboxes use the component toolchain's normal package
installation and reload flow. The platform-owned `faros-dev-agent` supplies
the coordinator, runtime supervisor, and executor only; it does not copy,
validate, or mount this SDK. This keeps dependency resolution explicit in the
application's manifest and makes development and production use the same
published artifact. The SDK remains server-only and the app still receives
only the short-lived workload credential and non-secret action context.

Use an atomically refreshed token file (the default when
`FAROS_ACTIONS_TOKEN_FILE` is set), a static workload token, or a refreshable credential provider. A provider is
called with `{ forceRefresh, signal }` and is called again with
`forceRefresh: true` after a single HTTP 401:

```js
const faros = createActionsClient({
  baseURL: process.env.FAROS_APP_STUDIO_URL,
  project: process.env.FAROS_PROJECT,
  getToken: ({ forceRefresh }) => tokenStore.get({ forceRefresh }),
});
```

When `tokenFile` is omitted, the SDK reads `FAROS_ACTIONS_TOKEN_FILE` on every
request. This is the shared, read-only application token published by the
development coordinator. Never point it at the coordinator-only projected
bootstrap token path.

`baseURL`, `project`, `org`, and `workspace` default to
`FAROS_ACTIONS_BASE_URL`, `FAROS_PROJECT`, `FAROS_ACTIONS_ORG`, and
`FAROS_ACTIONS_WORKSPACE`. The latter two are sent as `X-Faros-Org` and
`X-Faros-Workspace` headers. The base URL must be absolute HTTPS; tests may
explicitly set `allowInsecureLoopback: true` for an HTTP loopback URL.

Every request can carry retry and tracing metadata. `timeoutMs` aborts the
request locally; `signal` can be used by the enclosing server request:

```js
const value = await faros.integration('sales').invoke('lookup/v1', { key: 'order-1' }, {
  signal: request.signal,
  timeoutMs: 10_000,
  idempotencyKey: 'job-42-attempt-1',
  requestID: 'request-42',
  actionDeadlineMs: 15_000,
});
```

The successful return value is `result`. `invokeEnvelope` returns the complete
stable envelope (`requestID`, provider, action/version, bound `resourceRef`,
and `result`). A provider failure throws `ProviderActionError` with stable
`code`, `message`, `retryable`, request and binding metadata. Transport and
configuration failures throw `ActionsClientError` with a machine-readable
`code` such as `timeout`, `aborted`, `network_error`, or `invalid_response`.

## Release

The GitHub Actions workflow `actions-node-release.yaml` publishes this package
from tags named `actions-node/v<version>`. The tag version must exactly match
`package.json`. The npm package must configure that workflow as a trusted
publisher; no long-lived npm token is stored in the repository.

The first public version is the bootstrap exception: npm cannot attach a
trusted publisher until the package exists. A maintainer must authenticate with
npm and publish that first version from the reviewed package directory. Then
configure `faroshq/faros` and `actions-node-release.yaml` as the package's npm
trusted publisher before creating subsequent release tags.

Before publishing, the workflow runs the unit suite and installs the packed
artifact into a clean consumer under the public `@faros/actions-node` alias.
After publishing, it repeats that alias install from the npm registry so a
successful release proves the exact generated-app dependency contract.
