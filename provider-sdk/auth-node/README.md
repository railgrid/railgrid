# `@railgrid/auth-node`

`@railgrid/auth-node` is the stable consumer alias for the server package
`@crwilhit/railgrid-auth-node`. Pin the package in the server component's
manifest so development and production install the same version:

```json
{
  "dependencies": {
    "@railgrid/auth-node": "npm:@crwilhit/railgrid-auth-node@0.1.1"
  }
}
```

Railgrid's app proxy handles browser login and logout. A login link points to
`/__railgrid/auth/login?returnTo=/`; logout is a same-origin `POST` to
`/__railgrid/auth/logout`. The proxy strips inbound identity headers and adds a
short-lived, hub-signed assertion as `X-Railgrid-App-Identity` on `/api/`
requests. The SDK verifies only that header with Ed25519 keys fetched from the
fixed URL `<RAILGRID_AUTH_ISSUER>/jwks`.

The trusted runtime supplies these values:

- `RAILGRID_AUTH_ISSUER`: canonical HTTPS issuer, such as
  `https://hub.example/auth/apps/identity`.
- `RAILGRID_AUTH_AUDIENCE`: `<ProjectUID>/<environment>`, such as
  `01J.../production`.
- `RAILGRID_AUTH_ORIGIN`: the exact published HTTPS origin, such as
  `https://orders.example`.
- `RAILGRID_AUTH_INSTANCE_UID`: the platform-stamped UID for this app instance.
  The SDK requires an exact match with the signed `railgrid_instance_uid` claim.

The issuer, audience, and origin are read from environment variables only.
The SDK rejects insecure or non-canonical URLs, derives the JWKS URL itself,
does not follow redirects, and never reads a bearer token or identity from
application input. Its JWKS cache lasts at most five minutes; a `kid` miss
causes one bounded refresh attempt. During a JWKS outage, a previously cached
key remains usable only for the maximum assertion lifetime after cache expiry,
and each assertion's signature and expiration are still checked.

## Express integration

The package has no Express runtime dependency. Its middleware uses the standard
Express/Connect `(req, res, next)` shape:

```js
import express from 'express';
import { createRailgridAuth } from '@railgrid/auth-node';

const app = express();
const railgridAuth = createRailgridAuth();

app.use('/api', railgridAuth.middleware());

// This is the app's own route and response shape.
app.get('/api/me', async (req, res) => {
  const user = await railgridAuth.getUser(req);
  res.json(user); // { id, displayName? }
});
```

For a route that permits anonymous requests, use `getOptionalUser(req)` instead
of required middleware. It returns `null` only when the identity header is
absent; a supplied but invalid assertion still rejects with `401`.

Missing or invalid identity returns a structured `401`; a bad `Origin` on a
mutation returns `403`; and a JWKS outage without a usable cached key returns
`503`. Authenticated `POST`, `PUT`, `PATCH`, and `DELETE` requests must carry
the exact configured `Origin`. `GET`, `HEAD`, and `OPTIONS` do not use this
CSRF check. The user ID is the opaque `sub`; `displayName` comes from the
optional signed `name` claim. Email is deliberately omitted so apps do not
accidentally make it their account key.

Do not send the assertion to browser code, place it in local storage, or use
the browser's `Authorization` header as an alternate identity source. Browser
code calls the app's own `/api/*` routes. On a `401`, show a sign-in-expired
state with a link to `/__railgrid/auth/login?returnTo=/`. The assertion never
needs to reach React.

The first-party v1 flow covers existing Railgrid accounts. It does not add
organization membership or third-party identity providers. See the bundled
`railgrid-auth` App Studio skill for ownership and migration guidance.

## Local checks

```sh
npm test
npm run test:install
```

The install check packs the local artifact and imports it from a clean consumer
under the stable alias. It does not publish to a registry.
