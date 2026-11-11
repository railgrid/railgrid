# Express integration

Install the exact server dependency in the app's server `package.json`:

```json
{
  "dependencies": {
    "@railgrid/auth-node": "0.1.1"
  }
}
```

For the `application` template, edit `api/package.json`. Once App Studio syncs
the change, the API component installs dependencies and reloads the process
automatically. A separate registry lookup or install command is unnecessary.
A separate `restart_runtime` is unnecessary for this manifest change. Run
`verify_development_runtime` to check sync and process health. If
verification reports a blocker, use the API component's install and startup
logs to diagnose it. Report any registry or install error and keep the pinned
SDK. The production build uses the same manifest.

```js
import express from 'express';
import { createRailgridAuth } from '@railgrid/auth-node';

const app = express();
const auth = createRailgridAuth();
app.use('/api', auth.middleware());

// The application owns this route and decides its public JSON shape.
app.get('/api/me', async (req, res) => {
  const user = await auth.getUser(req);
  res.json({ id: user.id, displayName: user.displayName });
});
```

The trusted runtime injects `RAILGRID_AUTH_ISSUER`,
`RAILGRID_AUTH_AUDIENCE`, `RAILGRID_AUTH_ORIGIN`, and
`RAILGRID_AUTH_INSTANCE_UID`. Do not set these from request parameters. The
audience is `<ProjectUID>/<environment>`; the instance UID must match the
signed `railgrid_instance_uid` claim exactly.

Only the signed `X-Railgrid-App-Identity` header is an identity source. The
hub proxy strips inbound copies before adding its own assertion. Missing or
invalid identity produces a structured `401`; a failed key lookup with no
usable cache produces `503`. Unsafe methods require the exact configured
`Origin` and receive `403` otherwise.

The user ID is the assertion's opaque `sub`. The optional signed `name` claim
becomes `displayName`. Do not return or persist the assertion, use email as a
key, or create a second auth cookie inside the app.

Use `get_project_authentication` to confirm `enabled` for the project and
`configured` and `runtimeReady` for development. Verify through the app's
preview URL so requests pass through the access gate. Check that `/api/me`
returns 401 without an app session, returns the user's ID after sign-in, and
returns 401 again after logout. Keep preview access requirements in place
during these checks. If the browser needs the user to complete sign-in, report
which checks are pending.
