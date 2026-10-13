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

The hub canonicalizes its external HTTPS origin for the identity issuer,
including host case, the default `:443` port, and trailing slashes. An HTTP URL
or a URL containing credentials, a path prefix, query, or fragment disables
only the optional identity routes and logs the reason; the hub and ordinary
published-app access authentication continue to start. Enabling Railgrid Auth
still requires a usable HTTPS identity service.

The `authentication-grants` Project custom subresource lists, approves, and
revokes existing production app users. Approval takes `{user, appId}`, where
`user` is an existing platform User identifier and `appId` must identify the
Project's production environment. It creates the same named app-access RBAC
objects used by private-app access, including when the app is public. It never
creates accounts, organization memberships, or workspace memberships.

The hub's existing-account resolver requires the declared
`app-auth.users.resolve` workspace capability, a verified human action proof
bound to the requested user and app ID, and permission to update the exact
instance. This is a hub-access capability, separate from APIBinding permission
claims. The default platform-provider policy allows an undecided capability;
an explicit decline blocks it. When that default is disabled, or App Studio is
organization-owned, an eligible admin must accept it through Review access or
Enable before approving outside accounts. Existing disabled apps and sharing
flows remain usable without it.

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
Publishing and authentication approvals have separate source-labelled roles
and bindings. Only an authentication approval's primary binding grants the
UID-scoped sign-in permission. Lists, revokes, and resets act only on the source selected by the original Project verb. Unpublishing or
resetting sharing preserves authentication approvals, and revoking an auth
approval preserves any sharing grant for that account.
The same hub-access decision policy applies to this capability: platform
defaults can allow it while undecided, but an explicit decline always blocks
the broker until an eligible admin accepts it. Mutating proof redemption is
recorded atomically in the shared hub store per nonce, capability, method and
path, through the proof expiry and accepted clock skew. A resolver call and
its following grant creation can each redeem their own capability once; a
captured creation proof cannot restore a grant after revocation. Read-only
GET calls remain reusable. The signer and verifier use proof version/domain
v2, so proofs minted before redemption tracking cannot become a first-use
replay after an upgrade. Complete the hub replica rollout before relying on
these guarantees: mixed v1/v2 replicas reject each other's proofs, and an old
in-flight action must be retried through a fresh original Project request.
When publishing or preview has no runtime yet, the
hub lets the provider handle reads and policy changes with all broker target
claims cleared. Malformed targets, foreign ownership and API failures remain
closed to broker access.

Managed grants carry Project and Instance ownership. Identity issuance also
requires `instances/access.auth.<scopeHash>`, where the scope hashes a dedicated
authentication-grant domain and the Project and Instance UIDs, in addition to
the named `instances/access` permission. Only the authentication source role
contains that sign-in permission. Legacy secondary identity roles retain their
old permission coordinate, which identity issuance no longer queries. A stale
binding cannot acquire the new permission when an app name is reused. Reusing
a deleted Project's name does not carry its app approvals into the replacement.
Listing grants is strictly read-only, including when the caller also has update
permission. A grant's `migrationPending` flag indicates that its current
source-specific policy or UID ownership still needs migration or could not be
confirmed by a read.
A leader-elected hub controller watches the provider catalog and
each verified platform or organization-owned Infrastructure APIExport. It
replays existing Instances at startup and reconciles Instance changes across
that export's consumer workspaces. Before migrating an approval it re-reads the
Project, its environment binding, the Instance owner, and the existing managed
RBAC objects in the same tenant workspace. Grants belonging to a different or
unknown Project UID are never adopted.
Active reachability roles and bindings use the Project and Instance UID pair.
Migration creates the new binding before deleting an exact recognized legacy
binding with UID and resource-version preconditions; it never updates the
immutable role reference. Structurally recognized static legacy roles are
retired with empty rules, including roles whose Project UID is missing or old.
Their remaining bindings cannot regain access when a role name is reused.
A bounded app-scoped role scan also retires exact managed roles for previous
UID pairs before a replacement app can receive new grants; access does not
depend on asynchronous owner garbage collection.
Unrecognized role shapes and binding objects are preserved for operator review.
Unsourced legacy grants retain the historical publishing intent; authentication
approvals must be explicit rather than inferred from an ambiguous old binding.

The migrated labels, owner references, UID-specific roles and versioned
completion marker are durable KRM state. Status-only Instance updates do not
trigger migration, and a current marker avoids per-binding scans. Clients are
reused per tenant, and grant lists use app-scoped selectors and bounded pages.
Malformed legacy bindings are reported and skipped while valid bindings migrate.
Reconciliation is idempotent: a failed write retries with controller backoff,
and a restart replays the persisted Instances. Revocation deletes the primary
grant before attempting legacy identity cleanup. The primary auth binding owns
the current sign-in permission, so cleanup drift cannot retain fresh sign-in
access even when a separate sharing grant remains. Already issued assertions
still obey their existing bounded lifetime. This migration preserves existing
sharing grants and creates no new accounts or memberships. An ambiguous legacy
grant does not authorize app identity; approve authentication explicitly after
the migration. New approval,
revocation, and reset operations remain explicit request-time mutations.

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
Instance UID, plus an S256 PKCE challenge. The access proxy generates a random
verifier for each login and keeps it in the protected return-state cookie;
only its hash travels through the hub login URL. Exchange requires the matching
verifier, so an intercepted authorization code alone cannot obtain an assertion.
Missing, malformed, or mismatched PKCE inputs fail closed. Its purpose is
separate from preview and app-access codes. The hub rechecks the persisted
descriptor and authorization at redemption.
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

Signing and JWKS responses each use one coherent persisted-key snapshot. The
hub coalesces simultaneous cache misses and caches that snapshot for at most
ten seconds, or until an overlapping public key expires. Rotation invalidates
the rotating process's cache; other hub replicas observe the new key within
ten seconds. An expired cache never hides a failed Secret read.

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

Deploy the updated hub and access gates together when adding PKCE. Old login
attempts and gates without a verifier are rejected; users can start a new login
after the rollout. There is no fallback to code-only redemption.

## Key rotation

An operator with `get` and `update` access to the existing
`railgrid-app-identity-keyring` Secret in the hub's controllers workspace can
rotate the signing key with an explicit KCP kubeconfig:

```sh
railgrid-hub rotate-app-identity-key \
  --kubeconfig /path/to/kcp-admin.kubeconfig \
  --context kcp-admin
```

The command scopes the client to `root:railgrid:system:controllers` and uses
the supplied file's current context when `--context` is omitted. It does not
start the hub, create a key ring, use ambient credentials, or bypass KCP RBAC.
It preserves the subject-pseudonym key and existing unexpired public keys,
adds the previous signing key to the fifteen-minute overlap, and updates the
Secret using its resource version. Retry the command if a transient dependency
failure prevents the update; malformed or unavailable key material fails closed.
This is an overlapping signing-key rotation, not immediate revocation of
existing assertions or recovery from compromise of the subject-pseudonym key.

## SDK and assistant skill

The server-only SDK lives in `provider-sdk/auth-node`. Generated applications
use the organization-scoped package:

```json
{
  "dependencies": {
    "@railgrid/auth-node": "0.1.1"
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
not a public package release. The organization-scoped `@railgrid/auth-node@0.1.1`
release must be available before rolling out the updated assistant skill. The
`auth-node/v<version>` release workflow publishes with provenance through npm
trusted publishing after the package maintainer configures the repository and
workflow identity. See the SDK README for the release setup and migration.
This PR prepares the namespace switch; it does not publish to npm.

## Validation

Run `make test-railgrid-auth`, `make lint-railgrid-auth`, and the aggregate checks
in `docs/app-studio-harness-validation.md`. Authentication-specific regressions
cover app-only grants, incompatible enablement, sharing preservation, optional
identity, signature and environment/instance mismatches, code replay, origin
checks, key rotation, replica changes, logout and cached-key outages.
Additional regressions cover intercepted codes and PKCE validation, cached
Secret read counts and concurrent refreshes, optional HTTP-issuer startup,
operator rotation RBAC, and authentication binding repair without a flag change.
Additional checks cover grant-source isolation, immutable role references,
migration completion and status filtering, revocation with drifted identity
bindings, proof redemption across replicas, retryable code exchange dependencies,
non-consuming wrong-verifier attempts, callback refresh after temporary hub
or JWKS failure, HTTPS or loopback hub transport, and
key-cache refreshes that remain independent of individual request cancellation.

Live validation uses an owned App Studio application and normal browser sessions.
Check anonymous access, two users' separate records, denied access, logout,
configuration changes and the assistant's skill/tool receipts. Record native
audits, durable outcomes, source revisions and application assertions separately.
Exercise sign-in from a fresh browser context: open the application, select
Railgrid sign-in, authenticate through the hub's login form, and verify the
browser returns to the application with a usable protected API session. Test
this continuation for each enabled hub login method.
Never put cookies, authorization codes, assertions or private keys in evidence.
