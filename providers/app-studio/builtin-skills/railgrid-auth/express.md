# Express integration

Install the exact server dependency in the app's server `package.json`:

```json
{
  "dependencies": {
    "@railgrid/auth-node": "npm:@crwilhit/railgrid-auth-node@0.1.1"
  }
}
```

App Studio's normal runtime install flow installs the pinned dependency in both
development and production. Do not ask an LLM to run a separate `npm install`
or use a floating version.

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
