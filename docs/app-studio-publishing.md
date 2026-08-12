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
  default `public`). It is an ordinary template input: flipping it is an
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
