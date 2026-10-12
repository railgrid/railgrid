# Railgrid Auth in App Studio

Railgrid Auth lets a supported App Studio application identify approved existing
Railgrid users. It is optional and independent of sharing: a public application
can offer an anonymous landing page while protecting personal data in its API.
The first release supports the React/Node `application` template and platform app
domains. Third-party identity providers, independent customer signup, additional
language SDKs, and custom-domain authentication are deferred.

## Configuration and authorization

`Project.spec.authentication.enabled` defaults to false. The `authentication`
Project custom subresource reads or updates that intent; writes additionally
require `update` on the Project. App Studio reconciles reserved template inputs
from the persisted Project UID and environment. Infrastructure stamps the tenant
Instance UID. These inputs override application-supplied environment variables.
The hub also verifies the live Project owner and its matching environment
binding before accepting that descriptor. Supplying identity values on a
standalone Instance does not create a Project identity or select another app's
audience.

The `authentication-grants` Project custom subresource lists, approves, and
revokes existing production app users. Approval takes `{user, appId}`, where
`user` is an existing platform User identifier and `appId` must identify the
Project's production environment. It creates the same named app-access RBAC
objects used by private-app access, including when the app is public. It never
creates accounts, organization memberships, or workspace memberships.

The hub's existing-account resolver requires the declared
`app-auth.users.resolve` workspace capability, a verified human action proof
bound to the requested user and app ID, and permission to update the exact
instance. Existing deployments must accept this capability through the normal
provider Enable flow before approving outside accounts. Existing disabled apps
and sharing flows remain usable without it.

Sharing and authentication approvals use the hub-owned
`app-access.grants.manage` workspace capability at
`POST /api/orgs/{org}/workspaces/{ws}/app-auth/grants`. The broker accepts only
the exact Project-managed production or preview Instance, verifies both object
UIDs and ownership, and requires the human's `get` permission for listing or
`update` permission for mutations. Its action proof also binds the requested
operation and, for creation or revocation, the stable User or grant name. App
Studio finishes its existing membership or invite flow before the broker
creates a grant. Authentication approvals additionally require that Railgrid
Auth is enabled for that same production Instance and that the signed
`appId` is `<projectUID>/production`. The hub creates only the fixed
`railgrid-app-access` RBAC shape; providers cannot supply rules or subjects.
Existing workspaces must have this capability accepted under their normal
provider Enable policy before the sharing surfaces use the broker.

Managed grants carry Project and Instance ownership. Identity issuance also
requires `instances/access.<scopeHash>`, where the scope hashes the Project and
Instance UIDs, in addition to the existing named `instances/access` permission.
That identity permission
lives in a separate role bound to the Project and Instance UIDs, so a stale
binding cannot acquire it when an app name is reused. Reusing a deleted
Project's name does not carry its app approvals into the replacement.
The broker can migrate grants with the same recorded Project UID when the owner
opens grant management; grants belonging to a different or unknown Project UID
are never adopted. Existing local grants created before this UID check need
that verified migration before they can issue new identities.

Workspace members can sign in to every environment. Development previews and
other non-production environments require workspace membership, even if an old
instance access grant exists. Production can additionally admit explicitly
approved existing accounts. Application roles and record ownership remain
application-owned; platform membership is not an application administrator
role.

The Authentication settings panel reports template compatibility and runtime
configuration separately from application verification. A configured runtime
is not evidence that backend routes enforce ownership. Enabling authentication
requires a compatible hub identity service and template; it does not modify
sharing. Disabling it turns off identity handling in the access gate while
retaining the instance UID and app/environment metadata used to configure the
runtime SDK. The SDK and its required middleware keep running; because the gate
no longer forwards an identity, required-identity routes return 401 until the
application deliberately changes its authentication behavior.

## Browser and API flow

The access proxy reserves these endpoints when Railgrid Auth is enabled:

- `GET /__railgrid/auth/login?returnTo=/` begins sign-in.
- `POST /__railgrid/auth/logout` ends this app session; it requires the exact app
  Origin and does not end the console session.
- `GET /__railgrid/auth/session` returns a bounded profile without an assertion.
- `/__railgrid/auth/identity/callback` completes the browser exchange.

The hub serves `/auth/apps/identity/authorize`, `/exchange`, and `/jwks`. The
one-use code is bound to the exact app origin, audience, environment and tenant
Instance UID. Its purpose is separate from preview and app-access codes. The hub
rechecks the persisted descriptor and authorization at redemption.
Authorization and exchange both re-read the platform User record. Deleted,
missing, or changed accounts cannot obtain a new assertion from an old hub
session; unavailable account lookups fail closed. Existing assertions retain
their bounded lifetime.

The exchange issues an Ed25519-signed identity assertion with a lifetime of at
most fifteen minutes. Its subject is an opaque application-scoped identifier;
email and RBAC usernames are not application database keys. The key ring is
persisted in the hub's controllers workspace, with an independent subject key
and an explicit rotation operation that retains prior public keys through the
assertion lifetime. No hub or provider bearer reaches the application.

Each login attempt uses a Secure, HttpOnly `__Host-` return-state cookie with
`Path=/` and no Domain attribute, so sibling applications cannot plant the
callback state. The gate strips current and legacy platform state cookies
before forwarding application requests.

The gate stores the assertion in a Secure, HttpOnly, host-scoped cookie. It
strips inbound platform identity headers, verifies the cookie, and forwards
`X-Railgrid-App-Identity` only to the API route. The Node SDK independently checks
the signature, issuer, audience, environment, Instance UID and expiry. Assertions
are never returned to browser code or placed in browser storage. The existing
private access gate remains the outer admission check.

Protected APIs return a structured 401 when authentication is absent or expired;
they do not redirect API requests. The frontend offers sign-in again. Authenticated
mutations require the configured exact Origin. Revocation is bounded by the
fifteen-minute session lifetime. Cached known verification keys keep valid
sessions usable during a hub outage; new sign-ins, unknown keys and renewals
fail closed.

## SDK and assistant skill

The server-only SDK lives in `provider-sdk/auth-node`. Generated applications
use the stable consumer alias:

```json
{
  "dependencies": {
    "@railgrid/auth-node": "npm:@crwilhit/railgrid-auth-node@0.1.1"
  }
}
```

The package provides `createRailgridAuth`, required middleware, `getUser`, and
`getOptionalUser`. Optional identity is null only when the identity header is
absent; invalid assertions still fail. Trusted configuration is injected through
`RAILGRID_AUTH_ISSUER`, `RAILGRID_AUTH_AUDIENCE`, `RAILGRID_AUTH_ORIGIN`, and
`RAILGRID_AUTH_INSTANCE_UID`. Backend import in browser code is rejected.

The application owns its `/api/me` endpoint, user mappings and business checks.
Associate the verified opaque ID with an application user ID and constrain data
queries by that owner. Do not accept a browser-supplied owner or assign existing
unowned records to the first person who logs in.

The discoverable `system:railgrid-auth` skill uses the existing `load_skill` and
`read_skill_resource` lifecycle. Its examples cover Express, React, ownership,
and migration. `get_project_authentication` is read-only;
`configure_project_authentication` implements an explicit configuration request.
Loading or discussing the skill does not enable authentication. Package versions
are pinned, and the runtime performs its normal dependency installation.

The SDK can be packed locally with `make pack-railgrid-auth-sdk`. Local smoke
validation can install that exact artifact through an owned, internal ClusterIP
registry without publishing a package externally. Production distribution needs
the versioned package available in the configured registry; a local fixture is
not a public package release.

## Validation

Run `make test-railgrid-auth`, `make lint-railgrid-auth`, and the aggregate checks
in `docs/app-studio-harness-validation.md`. Authentication-specific regressions
cover app-only grants, incompatible enablement, sharing preservation, optional
identity, signature and environment/instance mismatches, code replay, origin
checks, key rotation, replica changes, logout and cached-key outages.

Live validation uses an owned App Studio application and normal browser sessions.
Check anonymous access, two users' separate records, denied access, logout,
configuration changes and the assistant's skill/tool receipts. Record native
audits, durable outcomes, source revisions and application assertions separately.
Exercise sign-in from a fresh browser context: open the application, select
Railgrid sign-in, authenticate through the hub's login form, and verify the
browser returns to the application with a usable protected API session. Test
this continuation for each enabled hub login method.
Never put cookies, authorization codes, assertions or private keys in evidence.
