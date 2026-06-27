# Hub kcp-proxy — per-workspace access (membership-gated)

**Status:** Design draft (proposed — not yet implemented)
**Owner:** TBD
**Last updated:** 2026-06-27
**Reads as a delta on:** [organizations.md](./organizations.md) (decision O-10), [provider-connectivity-contract.md](./provider-connectivity-contract.md)
**Companion:** [app-studio-sandbox-runtime.md](./app-studio-sandbox-runtime.md) (the provider that hit this first)

---

## Why this doc exists

The hub's user-facing kcp proxy
([pkg/server/proxy/proxy.go](../pkg/server/proxy/proxy.go)) forwards a user's
own bearer token to kcp so the request runs with the user's identity and kcp
enforces their RBAC natively. Before it forwards, it does a **cluster pre-check**:
it only lets a request through to the **one** workspace recorded in
`User.Spec.DefaultCluster`. Every other workspace is rejected with a 403
`cluster access denied` — *before* the request ever reaches kcp, and regardless
of whether the user actually has RBAC there.

That single-workspace funnel is the right default for the simplest case, but it
breaks any user-facing flow that needs a **non-default** workspace. App Studio
hit it head-on and had to route around the proxy entirely (see
[Relationship to App Studio](#relationship-to-app-studio-option-b)). This doc
proposes the platform-wide fix: **authorize the requested cluster against the
user's membership, not against a single fixed `DefaultCluster`.**

---

## Current model

### `DefaultCluster` is a fixed "home" pointer

`User.Spec.DefaultCluster` is written **once** by the organization controller
([pkg/hub/controllers/organization/controller.go](../pkg/hub/controllers/organization/controller.go),
"Step J") to the kcp logical-cluster ID of the user's **default** workspace
(the default child Workspace of their personal Org). It is **not** updated when
the user switches workspaces in the portal — there is no server-side "current
workspace" concept.

### The proxy gate

`resolveKCPPath` ([proxy.go](../pkg/server/proxy/proxy.go)) accepts only that
one cluster (or a mount under it):

```go
// /clusters/{id}/... — validated against defaultCluster
if clusterID != defaultCluster && !strings.HasPrefix(clusterID, defaultCluster+":") {
    return "", http.StatusForbidden, `{... "message":"cluster access denied" ...}`
}
// bare /api|/apis path → scoped to /clusters/{defaultCluster}/...
```

A **second** gate (O-10) refuses any `root:kedge:tenants:*` *path* outright
(`OrgWorkspaceNotDirectlyAccessible`), steering Org-scoped operations to the hub
REST surface.

### What this means for multi-workspace users

A user can belong to many Orgs and many Workspaces — the
`UserMembershipIndex` ([apis/tenancy/v1alpha1/types_user_membership_index.go](../apis/tenancy/v1alpha1/types_user_membership_index.go))
lists every `(OrgUUID, WorkspaceUUID)` they hold a Membership in, and the
organization controller's "Step H-backfill" grants them cluster-admin RBAC in
each of those workspaces. So **kcp would authorize them** in any of their
workspaces — but the proxy pre-check funnels user-token traffic to the single
`DefaultCluster` and 403s the rest.

This limitation is already acknowledged in code, in the comment on the
provider-enable handler
([pkg/hub/restapi/providers_enable.go](../pkg/hub/restapi/providers_enable.go)):

> the hub's kcp user-proxy pre-checks the cluster path against
> `User.Spec.DefaultCluster` and 403s every non-default workspace BEFORE
> forwarding to kcp — even when commit #220's per-workspace RBAC grants would
> have allowed it.

The enable flow worked around it by going through a hub REST handler that uses a
kcp-admin client instead of the user proxy.

---

## Proposal (Option A)

Make the proxy authorize the requested cluster against the **caller's
membership**, and let kcp RBAC remain the real enforcement boundary.

### A-1 — Authorize against `UserMembershipIndex`, not a single `DefaultCluster`

`resolveKCPPath` (and the SA path) change from "is this the default cluster?"
to "is this a workspace the caller is a member of?". Concretely:

- For `/clusters/{id}/...`: allow when `{id}` maps to a workspace in the
  caller's `UserMembershipIndex` (or a mount under such a workspace). Keep the
  exact-`DefaultCluster` match as a fast path.
- For bare `/api|/apis` paths (legacy kubeconfigs with no cluster segment):
  keep scoping to `DefaultCluster`. Bare paths carry no workspace selector, so
  the home workspace stays the sensible default.

**Back the authorization with an informer/watch on `UserMembershipIndex`, not a
TTL cache.** The index is continuously reconciled by the Membership controller
(O-3: it owns the index and keeps it in sync with every Membership write), so an
informer-backed local view is as fresh as the controller — authorization reads a
hot in-memory set with no per-request kcp round-trip and no TTL staleness window.
The proxy already holds `kedgeClient`; add a shared informer for the index and
gate off its lister.

### A-2 — Cluster → (org, workspace) topology index

Requests address clusters by **ID**; the membership index keys off Org/Workspace
**UUIDs** (path components). Rather than push cluster IDs into every membership
entry — or resolve `LogicalCluster` per request — keep a **separate
reconciler-maintained topology index**: `clusterID → (orgUUID, wsUUID)` over the
Org/Workspace tree.

- A small hub reconciler (or an informer-derived index over the kcp `Workspace`
  objects, which carry both `spec.cluster` and their path) maintains
  `map[clusterID] → (org, ws)`. It's tenant-wide, not per-user, and reflects
  workspace create/delete continuously — the same freshness model as A-1.
- The `LogicalCluster`/`newClusterIDResolver` primitive
  ([pkg/hub/provider_cluster_resolver.go](../pkg/hub/provider_cluster_resolver.go))
  is the per-entry resolve the reconciler uses to populate the index; it is
  **not** on the request path.

This is the key simplification: with the topology index, **org-scope and
workspace-scope authorization become the same O(1) check** (see A-3). No cluster
IDs duplicated into membership entries, no per-request kcp call, no fan-out of
org-scope memberships into synthetic per-workspace entries.

### A-3 — Authorization check (one rule for both scopes)

For a request to `/clusters/{id}`:

1. **Topology (A-2):** `id → (org, ws)`. If `id` isn't in the index it isn't a
   kedge child workspace → fall through to the existing gates (O-10 / 403).
2. **Membership (A-1):** the caller's `UserMembershipIndex` covers `(org, ws)`
   when it holds **either** a workspace-scope entry `(org, ws)` **or** an
   org-scope entry `(org, "")`. Org-scope is just the `(org, *)` case of the
   same lookup — no special path.

O-10 (no direct access to **Org** workspaces) stays: a request whose target
resolves to the Org workspace itself (`root:kedge:tenants:{org}`, no `:{ws}`)
never matches a child entry and is refused as today. So the relaxation is
strictly "a member may reach their **child** workspaces"; the Org workspace
remains hub-mediated.

### A-4 — Drop the "current cluster" idea on the client

With A-1 in place there is no need for a server-side "current workspace". The
client simply addresses `/clusters/{id}` for whichever workspace it's operating
in; the proxy authorizes it against membership. `DefaultCluster` reverts to its
honest role: the default for bare paths and first-login landing.

### A-5 — Clients learn the cluster ID via a hub REST endpoint

The client still needs the cluster **ID** to address `/clusters/{id}`. Expose it
through the existing membership-gated org/workspace REST surface (O-10), reusing
the A-2 topology index for the `(org, ws) → clusterID` direction:

- **Resolve one:** `GET /api/orgs/{org}/workspaces/{ws}` returns the workspace's
  `clusterID` (add the field; the CLI plugin resolves a workspace name/UUID →
  ID, then writes a kubeconfig server URL of `<front-proxy>/clusters/{id}`).
- **List many:** `GET /api/orgs/{org}/workspaces` (and the switcher's
  `UserMembershipIndex`-backed listing) carry `clusterID` per row, so the portal
  retargets its kcp/GraphQL client on a workspace switch without an extra call.

Because these endpoints are already gated by `tenant.Middleware` (the caller
must hold a Membership in `(org, ws)`), a client can only resolve IDs for
workspaces it can actually reach — the same authorization the proxy then
re-checks (A-3), so REST and proxy never disagree. This is the symmetric,
provider-agnostic equivalent of the `X-Kedge-Cluster` header the backend proxy
injects for provider HTTP traffic: REST hands the **client** the ID; the header
hands the **provider** the ID; both come from the one topology index.

---

## Security analysis

- **kcp RBAC is unchanged and remains authoritative.** The proxy forwards the
  user's own token; kcp evaluates the user's RBAC in the target workspace. The
  proxy gate is **defense-in-depth**, not the primary control. Today it is
  *too tight* (single cluster); A-1 makes it match reality (the workspaces the
  user is a member of) while still failing closed for everything else.
- **No new trust in client input.** Authorization keys off the authenticated
  user's `UserMembershipIndex`, which the user cannot forge — exactly the model
  the tenant resolver already uses for the `X-Kedge-Org`/`X-Kedge-Workspace`
  headers ([provider_tenant_resolver.go](../pkg/hub/provider_tenant_resolver.go)).
- **Org workspaces stay sealed** (A-3 / O-10).
- **Revocation is reconciler-driven, not time-bounded.** Removing a Membership
  makes the Membership controller delete the matching `UserMembershipIndex`
  entry **and** tear down the per-workspace RBAC grant (the inverse of the
  organization controller's Step-H backfill). The proxy's informer reflects the
  index deletion within its propagation latency, and kcp denies independently
  once the RBAC grant is gone — two reconciler-driven controls, no TTL window to
  reason about.
- **Failure mode is closed:** unknown cluster, non-member, or index-lookup
  error → 403, same as today.
- **Blast radius is the most security-sensitive path in the system** (every
  user, every `kubectl`, every portal kcp call). This is the reason to document
  and review the design before implementing, and to land it behind tests that
  assert: member→allowed, non-member→403, Org-workspace→403, bare-path→default,
  cross-Org isolation.

---

## Relationship to App Studio (Option B)

App Studio needed per-workspace access *now* and could not wait on a change to
the shared proxy, so it took **Option B**: route tenant traffic through the
hub's embedded **GraphQL gateway** (`/graphql/{clusterID}`), which serves any
workspace the caller has RBAC in and is **not** `DefaultCluster`-gated. That
work added two pieces this proposal builds on:

- The backend proxy injects **`X-Kedge-Cluster`** — the resolved tenant's
  logical-cluster ID
  ([pkg/hub/provider_cluster_resolver.go](../pkg/hub/provider_cluster_resolver.go),
  wired in [pkg/hub/providers/proxy.go](../pkg/hub/providers/proxy.go)). The
  same resolver is reusable for A-2.2.
- It demonstrated, in production-shaped local runs, that a user token reaching a
  **non-default** workspace works end-to-end once the addressing is right — i.e.
  kcp authorizes it. That is the empirical basis for A-1.

Option A does **not** replace Option B. GraphQL remains the right surface for
provider data planes (typed schema, subscriptions, the `*Yaml`/`applyYaml`
conveniences). Option A is about the **raw kcp proxy** — `kubectl`, the portal's
direct kcp calls, and any future provider that wants user-identity kcp access
without standing up a GraphQL client. Once A-1 lands, a provider could choose
either surface; today the proxy forces non-default workspaces onto GraphQL or
the hub REST handlers.

---

## Decided

- **Freshness = informer, not TTL.** Authorization gates off an informer-backed
  lister of `UserMembershipIndex`, kept current by the Membership controller —
  as fresh as the controller, no staleness window (see A-1).
- **Revocation = reconciler-driven.** Removing the Membership makes the
  controller delete the index entry and tear down the per-workspace RBAC grant;
  both the proxy informer and kcp deny without any time bound (see Security
  analysis).
- **No feature flag.** Ship the membership gate directly, guarded by the test
  matrix above rather than a runtime flag.
- **Org-scope authorization = topology index, not membership fan-out.** A
  separate reconciler-maintained `clusterID → (org, ws)` topology index (A-2)
  turns authorization into two O(1) in-memory lookups (topology then
  membership), with org-scope as the `(org, *)` case of the same check (A-3). No
  cluster→org resolve on the request path and no fan-out of org-scope
  memberships into synthetic per-workspace entries.
- **Client gets the cluster ID from hub REST.** The membership-gated
  org/workspace endpoints return `clusterID` (single + listing), reusing the
  topology index (A-5). CLI plugin and UI resolve workspace → ID there, then
  address `/clusters/{id}`; no client-side kcp resolve.

## Open questions

- **Static-token and SA-user paths.** A-1 covers the OIDC user path; the proxy's
  `serveStaticToken` and workspace-SA identities (O-14) have a different
  membership model. Define how they authorize cluster access (likely: an SA is
  pinned to its one workspace, not membership-expanded).
- **Mounts.** The existing `{clusterName}:{mountName}` allowance must be
  re-expressed against membership (allow mounts under any member workspace, not
  just `DefaultCluster`).
- **Bare-path silent default.** Keeping bare `/api|/apis` → `DefaultCluster`
  means a user who primarily works in a non-default workspace has legacy/CLI
  tools silently hit the *wrong* workspace. Acceptable, but a known foot-gun to
  document for users.
