---
name: railgrid-auth
description: Add sign-in, show my orders, and protect personal data with Railgrid Auth.
---

Use Railgrid Auth for first-party sign-in to this App Studio project. It gives
the app a stable, opaque user ID and an optional display name. It does not add
organization membership or third-party identity providers.

Before changing platform auth, call `get_project_authentication` to inspect the
current project setting. Discussion, code examples, and loading this skill are
read-only. Call `configure_project_authentication({"enabled":true})` only
after the project owner explicitly asks to enable or set up Railgrid Auth. Do
not change the setting because the user only asked how auth works or asked for
an implementation discussion.

Railgrid's app-auth gate owns login, logout, the browser session cookie, and
the signed app identity header. The generated app owns its `/api/me` route and
all user-owned records. Add the pinned server dependency
`"@railgrid/auth-node": "npm:@crwilhit/railgrid-auth-node@0.1.1"`, then use the
SDK's Express/Connect middleware to verify `X-Railgrid-App-Identity`. Use the
opaque `user.id` as the database key. `displayName` is optional and is only for
presentation. Do not use email as the key, persist the assertion, put it in a
browser bundle, or trust a user ID supplied by the browser. The trusted runtime
also injects issuer, audience, origin, and platform-stamped instance UID
configuration; do not replace those values with request data.

Link sign-in to `/__railgrid/auth/login?returnTo=/`. Send logout as a same-origin
`POST` to `/__railgrid/auth/logout`. A `401` from an app API means the browser
session expired; show a sign-in-again link instead of reading, storing, or
refreshing a token in React.

Load the relevant supporting resource before changing code or data ownership:

- `express.md` for backend middleware and an app-owned `/api/me` endpoint.
- `react.md` for login, logout, and expired-session UI without browser tokens.
- `ownership.md` for SQL ownership keyed by the Railgrid user ID.
- `migration.md` before linking existing app accounts.

This v1 supports existing Railgrid accounts only. If existing app records have
another owner key, stop before assigning them to the first Railgrid user. Ask
the project owner which users own those records and what linking policy to
apply, then migrate only from an approved mapping. Do not add organization
membership or a third-party identity provider under this skill.
