# Published apps: template-native access

This document describes how a faros app gets its public URL and how access to
it is controlled. It is an API and template contract; it does not claim
acceptance in a live cluster.

## Design summary

There is no separate publication plane. A promoted production instance is
always served on one stable URL through the **access gate** — an
infrastructure-owned `faros-access-proxy` container that every publishable
template renders as a component of its own graph. Two native mechanisms
control who can open that URL:

- **Visibility** is the instance's `spec.access` value (`public` | `private`,
  template default `public`). It is an ordinary template input: flipping it is an
  in-place `update_instance` merge patch that only changes the gate's
  configuration — no route rewiring, no redeploy, no data loss.
- **Invitations** are plain kcp RBAC in the tenant workspace. A signed-in
  platform user may open a private app when they hold `get` on the instance's
  `access` subresource (e.g. `applications/my-shop` + subresource `access`).
  Granting access is creating a ClusterRole/ClusterRoleBinding pair; revoking
  is deleting the binding. App Studio's share dialog is a RoleBinding writer.
  Workspace members are effectively workspace admins today, so they can open
  every app in their workspace without an explicit grant.

  The per-app ClusterRole carries two rules: the access tuple
  (`<resource>/access`, resourceName-scoped, verb `get`) **and** kcp's
  workspace-content admission (`access` on nonResourceURL `/`) — kcp
  evaluates the latter before any RBAC rule in the workspace, so without it
  an invited outsider is denied even with a perfect grant. The workspace
  `access` verb only lets requests enter the authorizer chain; everything
  beyond the app-access tuple remains denied by ordinary RBAC.

  The RBAC **subject** is always the account's kcp username —
  `User.Spec.RBACIdentity` (`faros:<email>`, or `faros:static:<hash>` for
  static-token users) — because that is the string every tenant-workspace
  binding (including the workspace-admin ClusterRoleBinding) is written
  against and the string the hub's SubjectAccessReview presents. The User CR
  name is a platform-internal key that appears in no kcp binding; grant
  binding *names* and labels use it for display and dedup only. The hub
  membership API exposes it as `rbacIdentity` on membership views.

No `PublishedApp` or `AppAccessGrant` records exist, no publication
controller runs, and the hub carries no knowledge of any provider CRD schema.

App Studio applies a safer policy to development instances. A URL-backed
development binding is created with `access: private`, and the Project
controller reconciles that input from `Project.spec.sharing.preview.mode`.
`private` means workspace members only; `public` means anyone with the URL.
Development previews deliberately have no per-person invitation flow, and
production grants remain scoped to the separate `*-prod` instance name.

## The access gate

Templates that support publishing render the gate unconditionally and attach
their shared-Gateway `HTTPRoute` to it — in development and production, in
public and private mode. No tenant workload is ever the direct route backend,
so flipping `spec.access` can never be routed around. Path fan-out
(`/api` vs `/`) lives in the gate's controller-derived route table; targets
are confined to cluster-local Services
(`providers/infrastructure/accessproxy`).

The gate holds no credentials — no service-account token, no kubeconfig, no
signing keys — and its availability contract is strict:

- **public**: pure passthrough. No auth code runs on the request path and the
  hub is never contacted. A hub outage cannot affect a public app.
- **private**: requests need a gate-local session. A browser without one is
  redirected to the hub once; afterwards the gate validates its own bounded
  in-memory session locally. A hub outage leaves existing sessions working;
  only new sign-ins fail.

Platform inputs reach the gate as `${faros.*}` tokens substituted at RGD
build time (`${faros.accessProxyImage}`, `${faros.hubUrl}`,
`${faros.hubPublicUrl}`, `${faros.hubInsecure}`), and the application
controller stamps `spec.expose.fqdn` plus `spec.farosCluster` (the tenant
workspace cluster ID) onto the instance. Tenants only ever choose
`spec.access`.

## Private sign-in flow

The hub is the identity authority and appears exactly once per visitor
session (`pkg/hub/appauth`):

1. The gate redirects to `GET /auth/apps/authorize` with the instance
   coordinates (`cluster`, `group`, `resource`, `name`), its callback
   (`https://<app-host>/__faros/auth/callback`), and an opaque one-use state
   bound to the initiating browser.
2. The hub resolves the shared portal browser session (or bounces through the
   normal `/login` flow and back via the portal's `next` continuation), runs
   **one SubjectAccessReview** against the tenant workspace for the `access`
   subresource, and — when allowed — 302s back to the app callback with a
   one-use code. Redirects are only ever issued to hosts directly under the
   configured `--published-apps-domain` zone.
3. The gate exchanges the code server-to-server
   (`POST /auth/apps/exchange`), receives identity metadata plus a session
   TTL (never a credential), and mints its local session cookie
   (`__Host-faros-app-session`).

Revocation lag is bounded by the granted session TTL (15 minutes): after a
RoleBinding is deleted, the next silent re-authorize re-runs the SAR and
denies. Policy is therefore evaluated by kcp's authorizer — the hub never
reads provider objects, and nothing on the per-request path of any app
depends on the hub.

## Programmatic access to private apps

CLIs, CI jobs and AI agents cannot follow the browser redirect. Instead they
trade their hub token, at the hub, for an **app access token**: a short-lived
credential bound to one app. They then send that token to the app:

```bash
HUB=https://hub.example.com
# 1. Get the app's coordinates. The gate's 401 answer to any bearer request
#    names them, along with the token endpoint.
curl -s -H "Authorization: Bearer x" https://<app-host>/ | jq .instance
# 2. Mint an app access token with your hub token.
TOKEN=$(curl -s -X POST "$HUB/auth/apps/token" \
  -H "Authorization: Bearer $HUB_TOKEN" -H "Content-Type: application/json" \
  -d '{"cluster":"<cluster>","group":"infrastructure.faros.sh","resource":"instances","name":"<app>"}' \
  | jq -r .token)
# 3. Call the app.
curl -H "Authorization: Bearer $TOKEN" https://<app-host>/api/health
```

`$HUB_TOKEN` is any bearer the hub API accepts from you. On a token-only hub,
that is your static token. On an OIDC hub, it is your OIDC id_token, which is
the `status.token` that the `faros get-token` exec plugin in your faros
kubeconfig prints. kcp ServiceAccount tokens are refused.

**`POST /auth/apps/token`** (hub; called with your hub bearer):

- Request: `{"cluster", "group", "resource", "name", "ttlSeconds"?}`. The
  default TTL is 600 seconds; allowed values are 60 to 900.
- Response (200): `{"token": "fapp_…", "expiresAt": "<RFC 3339>", "host": "<app host>"}`.
- Errors: `400` malformed, `401` not a hub user credential, `403` no access
  to the app, `404` the app has no published host, `429` too many failed
  attempts, `503` hub dependency unavailable.
- The hub runs the same SubjectAccessReview as a browser sign-in, on the
  `access` subresource and as your RBAC identity. A token never outlives the
  hub token it was minted from.

**At the gate:**

- A request with `Authorization: Bearer fapp_…` is checked once through the
  hub's `POST /auth/apps/verify`. The hub checks four things:
  - the token's seal;
  - that the token is bound to exactly this app (a token minted for app A is
    refused at app B's gate);
  - that the token has not expired;
  - the SubjectAccessReview, re-run so that revoking a grant takes effect.
- The gate caches the result under the token's SHA-256. It never stores the
  token. An allow is cached for the shorter of 15 minutes and the token's
  remaining lifetime; a refusal is cached for 30 seconds.
- An allowed request is proxied. Otherwise the gate answers without
  redirecting:
  - `401` for an invalid, expired or wrong-app token. The JSON body includes
    `tokenEndpoint` and `instance`.
  - `403` when the user has no access.
  - `502` when the hub is unreachable and no cached result exists. Tokens
    already verified keep working until their cache entry expires, the same
    as browser sessions.
- Anything that is not an app access token, including a raw hub token, gets
  `401` from the gate itself. It is never relayed.
- The app never sees any token: the gate strips `Authorization` before
  forwarding. A bearer request never creates an app session cookie.
- Requests without a Bearer `Authorization` header are handled as browser
  requests and are redirected to sign in.

**Threat model.** A gate may be operated by someone other than the platform.
For example, an org's self-hosted infrastructure provider runs its own
Gateway and gate. Your hub token therefore goes only to the hub. What a gate
or anyone else in the path sees is an app access token. That token:

- works only at that one app's gate;
- works only while you still hold access to the app, because the
  SubjectAccessReview is re-run on each verification;
- expires within 15 minutes;
- is sealed (AES-256-GCM), so it does not reveal your identity.

A leaked app token therefore gives someone access to one app, until the
token expires. They get only what your access grant already allows. Revoking
a grant takes effect within the gate's cache window, at most 15 minutes. The
hub limits failed mint and verify attempts per source address.

Tokens are stateless, so any hub replica verifies them. The sealing key is
HKDF-derived, with its own label, from the hub's cross-replica secret
(`faros-delegated-user-proof-key` in namespace `faros-hub` of
`root:faros:system:controllers`). No tenant, provider or user identity can
read that secret. Deleting it and restarting the hub replicas invalidates
every outstanding app token, along with the other credentials derived from
that secret.

## App Studio surface

Publishing endpoints (`providers/app-studio/api/project_publishing.go`) are a
thin veneer over the two mechanisms:

- `GET/POST/DELETE /api/projects/{p}/publishing` reads or writes the `access`
  value on the production binding (the Project reconciler converges the live
  instance); DELETE means "private + delete all grants" — production remains
  deployed and reachable by workspace members.
- `…/publishing/grants` lists/creates/revokes the RBAC pair
  (`faros-app-access.<instance>` ClusterRole, one ClusterRoleBinding per
  invited member, labeled `faros.sh/app-access=<instance>`). Grant
  creation validates current org/workspace membership through the hub API and
  requires private access; revocation is allowed in any mode.

Sharing with someone who is not on the platform yet needs no invite-link
machinery: the share dialog's "Invite by email" path asks the hub membership
API (`invite: true`) to pre-provision a **pending User** — email, display
name, and the email-derived RBAC identity, but deliberately no issuer/subject
binding — plus an org membership, and the app grant is written against that
stable User name immediately. The first OIDC sign-in whose IdP-verified email
matches a pending (and only a pending) account adopts it
(`auth.Handler.adoptInvitedUser`), so everything granted before arrival works
at first sign-in. Accounts already bound to an IdP subject are never matched
by email. Without `invite`, an unknown identifier remains a clean 404 so
typos cannot mint ghost users.

Because grants are ordinary RBAC objects, they are visible outside App
Studio too: the faros portal's Tenant Settings → Members tab lists every
app-access grant in the workspace (hub REST
`GET/DELETE /api/orgs/{org}/workspaces/{ws}/app-access[/{binding}]`, served
with the hub's kcp-admin client like the providers/enabled endpoints), and
workspace admins can revoke from there. `kubectl get clusterrolebindings -l
faros.sh/app-access` shows the same truth.

Promotion is unchanged and independent: digest-pinned image resolution and
`farosRedeployRevision` rollouts keep the production instance's identity
stable, which also keeps its URL and its RBAC grants stable across
re-promotes.

Source anchors:
[access gate](../providers/infrastructure/accessproxy/proxy.go),
[hub appauth](../pkg/hub/appauth/appauth.go),
[template gate component](../providers/infrastructure/install/templates/simple-webapp.yaml),
[cluster stamp](../providers/infrastructure/controller/application/controller.go),
[App Studio publishing API](../providers/app-studio/api/project_publishing.go).
