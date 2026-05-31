# Organizations and the workspace tree

**Status:** Design draft
**Owner:** TBD
**Last updated:** 2026-05-30
**Reads as a delta on:** [providers.md](./providers.md)
**Companion doc:** [provider-scoping.md](./provider-scoping.md)

---

## Why this doc exists

Today kedge has one tenancy primitive — `User`
([apis/tenancy/v1alpha1/types_user.go](../apis/tenancy/v1alpha1/types_user.go))
— and each user gets one personal kcp workspace
(`root:kedge:users:{userId}`, materialized in
[pkg/server/proxy/proxy.go](../pkg/server/proxy/proxy.go)). That's enough
for a single-tenant demo and nothing more:

- No way to share installs between teammates.
- No way to separate "my dev sandbox" from "my prod edges".
- Membership is implicit in "you logged in," with no role distinction.

This doc proposes the **Organization** concept and the **workspace
tree** that lives under it. Concretely:

1. An Org is a kcp workspace (using a kcp `WorkspaceType`) that holds
   catalog metadata and membership — **nothing else**.
2. Inside an Org, users create child **Workspaces** ("teams"). All
   actual work — `APIBindings`, edges, MCP instances, any tenant
   object — lives in these children, never in the Org workspace itself.
3. Users can belong to many Orgs and to specific child Workspaces
   without belonging to the parent Org.

Provider visibility/scoping (Public vs Org-Private, who can register
new providers, where catalog entries live) is built on this and lives
in [provider-scoping.md](./provider-scoping.md). Keep this doc to the
tree + membership.

---

## Decisions pinned

Don't re-litigate; the doc body assumes these.

| # | Decision | Rationale |
|---|---|---|
| O-1 | **Identity = UUID** for both Organization and Workspace. `metadata.name = <uuid>`; `spec.displayName` is metadata only. Two Orgs may share a displayName. | Removes a class of collision bugs; portal-side rename is a `displayName` patch and never moves a workspace path. |
| O-2 | **Migration = clean slate.** No existing prod data; existing `root:kedge:users:{userId}` workspaces are dev noise that can be deleted. No migration code, no fallback flag. | No legacy users to preserve; lets us require `X-Kedge-Org` / `X-Kedge-Workspace` from day one. |
| O-3 | **Membership index = separate `UserMembershipIndex` CRD** (one per User, owned by Membership controller). Not `User.status.memberships`. | Trivial RBAC (controller owns its own resource), easier to debug, schema can evolve without touching User. |
| O-4 | **Switcher disambiguation = always show secondary line** (`created {date} by {first admin}`) under every Org row in the portal switcher. Not just when ambiguous. | Unambiguous always, no client-side "is this name a duplicate?" logic. The `UserMembershipIndex` carries the extra fields. |
| O-5 | **Org quota = soft cap, admin-overridable per User.** Default 10 Orgs per User. `User.spec.orgQuota` overrides. 4xx on 11th create with a clear message. | Avoids accidental tree bloat; admins handle real edge cases by hand. |
| O-6 | **Workspace quota = soft cap, admin-overridable per Org.** Default 50 Workspaces per Org. `Organization.spec.workspaceQuota` overrides. | Symmetric with O-5. Tunable when a real team hits it. |
| O-7 | **CatalogEntry creation gating = configurable per Org**, `Organization.spec.catalogEntryCreation: members\|admin`, default `members` (matches `workspaceCreation`). Enforced at the **hub REST endpoint** (`POST /api/orgs/{uuid}/catalog`), not via kcp RBAC — see O-10. | Lets cautious Orgs gate the catalog; default trusts members. |
| O-10 | **Org workspaces are hub-mediated only.** Tenants never receive a kubeconfig that targets `root:kedge:orgs:{uuid}` directly. All Org-workspace operations (CatalogEntry CRUD, Membership CRUD, child Workspace create) flow through hub REST endpoints. The kedge kcp proxy ([pkg/server/proxy/proxy.go](../pkg/server/proxy/proxy.go)) refuses to issue exec-credentials for paths that resolve to a workspace of type `organization`. Child Workspaces (`root:kedge:orgs:{org-uuid}:{ws-uuid}`) are user-facing as today. | Network-level enforcement of \"no APIBindings in Org workspace\" (see [provider-scoping.md](./provider-scoping.md) P-2). Removes the need for any kcp admission webhook or MaximalPermissionPolicy scoping. |
| O-8 | **User delete = soft-delete with 30-day grace.** `User.status.deletionRequestedAt`; controller cascades personal Org + Memberships after the grace expires. Recoverable inside the window. | Protects against accidental delete; defers the "sole admin elsewhere" question until cascade time. |
| O-9 | **Membership removal = block Org removal if user has child Workspace Memberships.** Admin must revoke (or transfer) each Workspace Membership first. UI offers a "remove from all" shortcut that does it as one call. | Explicit; avoids the "why does Bob still see acme/data?" surprise. |
| O-11 | **Workspace initializers must be idempotent + self-healing.** Every initializer checks for existing CRs/RBAC before creating; a post-init reconciler verifies all expected state exists before treating the Org/Workspace as fully provisioned. Failed initializers retry forever; the reconciler is the safety net. | kcp initializers are async with no rollback (verified). Without this rule a partial init leaves silent breakage that surfaces only when a tenant hits 403. |
| O-12 | **Self-leave Org + multiple admins via Membership.role PATCH.** `DELETE /api/orgs/{uuid}/memberships/me` lets a member remove themselves (O-9 sole-admin/child-Workspace blocks still apply, so they must hand off first). Any Org admin can PATCH another Membership.role between `member` and `admin`; multiple admins are allowed. No separate "transfer ownership" endpoint. | Matches GitHub Orgs. Promotion + sole-admin block together cover the handoff case. |
| O-13 | **Soft delete with 30-day grace for both Org and Workspace** (symmetric with O-8). `DELETE /api/orgs/{uuid}` sets `Organization.status.deletionRequestedAt`; same for Workspace. Hidden from switchers immediately, recoverable inside the window via `POST .../undelete`. After grace expires the cascade controller removes child Workspaces/Memberships/CatalogEntries/APIBindings/edges/etc. | One number (30 days) for every soft-delete. Recovery for accidental deletes. Carries cost (state lingers) — acceptable. |
| O-14 | **ServiceAccount CR scoped to one Workspace** for non-human/CI access. Lives in a child Workspace; admins create via `POST /api/orgs/{org}/workspaces/{ws}/serviceaccounts`. Each SA has a token issued/rotated via the hub. Role is `admin` or `member`, same enum as `Membership.role`. Bot identities don't conflate with human Users. | Real platform users will run CI against Workspaces from day one; PATs on humans tie a person's lifecycle to a bot's. |
| O-15 | **Org admin has implicit admin in every child Workspace.** No "private from Org admin" Workspace in v1. Document loudly in onboarding so users understand the privacy boundary is the Org, not the Workspace. | Simplest mental model, matches GitHub Orgs default, makes audit/compliance straightforward. Sensitive teams should use a separate Org, not a private Workspace. |

---

## The tree

```
root
└── kedge
    ├── providers/                  ← Public CatalogEntries (admin-curated)
    └── orgs/
        └── 7f3a91d2.../            ← Organization workspace (UUID-named)
            │                         displayName: "ACME Corp"
            │                         WorkspaceType: organization
            │                         Holds: CatalogEntries (Private to this org),
            │                                Memberships (scope=org)
            │                         No APIBindings, no tenant objects.
            ├── 9c4b8e1f.../        ← Workspace ("team")
            │                         displayName: "platform"
            │                         WorkspaceType: workspace
            │                         Holds: APIBindings, edges, mcp instances,
            │                                Memberships (scope=workspace)
            ├── 5e2d6a8c.../        ← Workspace — displayName: "data"
            └── 3b1f47e9.../        ← Workspace — displayName: "sandbox"
```

> The tree is a *view*. The kcp backend is a flat set of
> `LogicalCluster`s; parent-child structure is reconstructed from
> `Workspace` references. Nothing here changes that — we're just
> declaring which paths the hub creates and what each level is allowed
> to hold.

### Paths are UUIDs; names are metadata

Every Organization and Workspace is keyed by a server-assigned UUID,
**not** the user-provided name. Two users can each create an Org with
`displayName: "ACME Corp"` and they get distinct workspaces
(`root:kedge:orgs:7f3a91d2…` and `root:kedge:orgs:b62e4a09…`). The
human-readable name lives in `spec.displayName` and exists only for
the portal, CLI output, and email subjects.

Consequences:

- The `X-Kedge-Org` / `X-Kedge-Workspace` headers carry UUIDs, never
  display names.
- REST paths look like `/api/orgs/{org-uuid}/workspaces/{ws-uuid}/…`.
  Display-name lookup is a portal-side convenience scoped to the
  caller's own Memberships; the backend only accepts UUIDs.
- Renaming an Org or Workspace is a `displayName` patch — cheap and
  safe. The underlying workspace path never changes.
- Membership references are by UUID too (`spec.userRef` is already a
  User ref; Org/Workspace identity is encoded by *where* the
  Membership lives, which is itself a UUID-named workspace).

---

## Core invariants

These keep the model from drifting back into the "everything in one
namespace" state we have today:

1. **No tenant work in the Org workspace.** No `APIBindings` to provider
   APIExports, no edges, no MCP instances. Only catalog metadata and
   membership.
2. **No catalog metadata in the Workspace.** `CatalogEntry`s live one
   level up (or platform-wide); the Workspace is for consuming them via
   `APIBinding`, not registering new ones.
3. **One Workspace per logical unit of work.** Two teams in the same Org
   that should see each other's objects share a Workspace; teams that
   should not, don't. Cross-workspace visibility is opt-in (v2).
4. **Org workspaces are hub-mediated only (O-10).** Tenants get no
   direct kubeconfig to a workspace of type `organization`. Every
   write into the Org workspace happens via a hub REST endpoint that
   uses the hub's privileged service account. The kedge kcp proxy
   refuses to mint exec-credentials for Org-typed workspaces.

Enforcement: O-10's api-proxy mediation makes invariant #1 physically
true (tenants can't reach the workspace to violate it); the
`WorkspaceType` constraints below carry the model into kcp's tree
machinery (allowed children, default bindings).

---

## kcp WorkspaceTypes

Two new types under `tenancy.kcp.io/v1alpha1`, both materialized at
hub bootstrap:

### `organization` (path `root:kedge`)

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: WorkspaceType
metadata:
  name: organization
spec:
  defaultAPIBindings:
    - path: root:kedge:providers
      export: tenancy.kedge.faros.sh   # Organization, Membership, CatalogEntry
  limitAllowedChildren:
    types:
      - { path: root:kedge, name: workspace }
  initializer: true
```

Initializer runs on creation:
- Adds the creating user as a `Membership` scope=`org`, role=`admin`.
- Seeds default RBAC (org-admin ClusterRole bound to the user's rbacIdentity).

### `workspace` (path `root:kedge`)

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: WorkspaceType
metadata:
  name: workspace
spec:
  defaultAPIBindings:
    - path: root:kedge:providers
      export: tenancy.kedge.faros.sh   # Membership only (no Org/CatalogEntry)
  limitAllowedParents:
    types:
      - { path: root:kedge, name: organization }
  limitAllowedChildren:
    none: true                          # v1: workspaces are leaves
  initializer: true
```

Initializer adds the creating user as `Membership` scope=`workspace`,
role=`admin`.

Per P-4 in [provider-scoping.md](./provider-scoping.md), nothing else is
auto-bound — not provider APIExports, not kedge `core.faros.sh`. Every
API a Workspace consumes (including builtins like edges, mcp,
server-edges) requires an explicit Enable that creates an APIBinding.

---

## CRDs

### `Organization` (cluster-scoped, `tenancy.kedge.faros.sh`)

Thin metadata wrapper. The actual storage is the kcp Workspace; this
CR exists so the hub has a single object to list, status, and reconcile.

```go
type Organization struct {
    metav1.TypeMeta

    // metadata.name is a server-assigned UUID (the kubectl name field
    // is generated, never user-supplied). The same UUID is used as the
    // child workspace name under root:kedge:orgs.
    metav1.ObjectMeta

    Spec   OrganizationSpec
    Status OrganizationStatus
}

type OrganizationSpec struct {
    // DisplayName is the human-facing label. Not unique — two Orgs
    // can share a displayName; the UUID in metadata.name disambiguates.
    DisplayName string `json:"displayName"`

    // Personal marks the Org auto-created for a single User at
    // bootstrap. Set once at creation; not mutable. Used by the portal
    // to badge / filter the switcher.
    // +optional
    Personal bool `json:"personal,omitempty"`

    // WorkspaceCreation controls who can create child Workspaces.
    //   members — any org Membership can create (default).
    //   admin   — only org admins can create.
    // +kubebuilder:default=members
    // +kubebuilder:validation:Enum=members;admin
    WorkspaceCreation string `json:"workspaceCreation,omitempty"`

    // CatalogEntryCreation controls who can publish Org-Private
    // CatalogEntries (see provider-scoping.md). Same enum + default as
    // WorkspaceCreation.
    // +kubebuilder:default=members
    // +kubebuilder:validation:Enum=members;admin
    CatalogEntryCreation string `json:"catalogEntryCreation,omitempty"`

    // WorkspaceQuota caps the number of child Workspaces. 0 means use
    // the platform default (50). Platform admin can patch this to lift
    // the cap for an Org that needs more.
    // +optional
    WorkspaceQuota int32 `json:"workspaceQuota,omitempty"`
}

type OrganizationStatus struct {
    // Path to the materialized kcp Workspace.
    // Always root:kedge:orgs:{metadata.name}.
    WorkspacePath string `json:"workspacePath,omitempty"`
    Conditions    []metav1.Condition `json:"conditions,omitempty"`
}
```

### `Workspace` — reuse kcp's

No wrapper CR. A Workspace is a kcp `Workspace` of type `workspace`,
created directly in the parent Org's workspace. Naming
(`root:kedge:orgs:{org}:{ws}`) follows from the parent path.

### `Membership` (namespaced or cluster — see below)

Single CRD covers both org and workspace scope:

```go
type Membership struct {
    metav1.TypeMeta
    metav1.ObjectMeta

    Spec   MembershipSpec
    Status MembershipStatus
}

type MembershipSpec struct {
    // UserRef points to a User CR (always cluster-scoped at root:kedge).
    UserRef corev1.LocalObjectReference `json:"userRef"`

    // Scope chooses the target. The Membership object itself lives in
    // the workspace it grants access to:
    //   scope=org       — Membership in the Org workspace
    //   scope=workspace — Membership in the child Workspace
    // +kubebuilder:validation:Enum=org;workspace
    Scope string `json:"scope"`

    // Role determines RBAC. v1 ships two:
    //   admin  — create/delete child workspaces (scope=org) or full
    //            access including Membership management (scope=workspace)
    //   member — consume the workspace; cannot manage Memberships
    // +kubebuilder:validation:Enum=admin;member
    Role string `json:"role"`
}
```

**Where Memberships live**: in the workspace they apply to. Deleting a
Workspace removes its Memberships for free. Deleting an Org cascades to
children, which cascades to their Memberships.

### `UserMembershipIndex` (cluster-scoped, `tenancy.kedge.faros.sh`)

One per User, owned by the Membership controller. Solves the "what
orgs/workspaces is user Alice in?" fan-out without scanning every Org
workspace per request.

```go
type UserMembershipIndex struct {
    metav1.TypeMeta
    metav1.ObjectMeta  // metadata.name = corresponding User's name

    Spec UserMembershipIndexSpec
}

type UserMembershipIndexSpec struct {
    Entries []MembershipIndexEntry `json:"entries"`
}

type MembershipIndexEntry struct {
    OrgUUID            string      `json:"orgUUID"`
    OrgDisplayName     string      `json:"orgDisplayName"`
    OrgCreatedAt       metav1.Time `json:"orgCreatedAt"`
    OrgFirstAdmin      string      `json:"orgFirstAdmin"`      // username, for switcher subtitle
    WorkspaceUUID      string      `json:"workspaceUUID,omitempty"`
    WorkspaceDisplayName string    `json:"workspaceDisplayName,omitempty"`
    Role               string      `json:"role"`               // admin | member
    Personal           bool        `json:"personal,omitempty"` // mirrors Organization.spec.personal
}
```

The Membership controller updates this index on Membership add/remove
and on Org/Workspace displayName patches. The portal reads exactly one
object per logged-in user to render the switcher (per O-4, the
secondary line carries `OrgCreatedAt` + `OrgFirstAdmin`).

---

## User flows

### Create your own Org

```
POST /api/orgs
{ "displayName": "ACME Corp" }
```

1. Hub generates a UUID, creates an `Organization` CR with
   `metadata.name = <uuid>` and `spec.displayName = "ACME Corp"`. No
   "slug" or `name` field is taken from the request.
2. Org controller creates kcp `Workspace` `root:kedge:orgs:{uuid}` of
   type `organization`. The initializer adds the caller as
   `Membership{scope: org, role: admin}`.
3. Index controller appends a `MembershipIndexEntry` to the caller's
   `UserMembershipIndex`.
4. Returns 201 with `{ "uuid": "...", "displayName": "...",
   "workspacePath": "root:kedge:orgs:..." }`.

### Create a Workspace inside an Org

```
POST /api/orgs/{org-uuid}/workspaces
{ "displayName": "Platform team" }
```

1. Hub looks up caller's Membership in `{org-uuid}`.
2. If `org.spec.workspaceCreation == admin`, require `role=admin`.
   Else any `member` is allowed.
3. Generates a Workspace UUID. Creates kcp `Workspace`
   `root:kedge:orgs:{org-uuid}:{ws-uuid}` of type `workspace`. The
   initializer adds the caller as
   `Membership{scope: workspace, role: admin}`.
4. Index controller appends a `MembershipIndexEntry` (with
   WorkspaceUUID set) to the caller's `UserMembershipIndex`.
5. Returns 201 with the workspace UUID + displayName + path.

### Switch the active context

The portal sends headers on every request:

```
X-Kedge-Org:       7f3a91d2-...     # Org UUID, required for org/workspace-scoped APIs
X-Kedge-Workspace: 9c4b8e1f-...     # Workspace UUID, required for workspace-scoped APIs
```

Display names are never sent on the wire — the portal looks them up
from the caller's `UserMembershipIndex` and renders the switcher locally.

Tenant middleware in `pkg/hub/server.go`:

1. Resolves token → User.
2. Reads `X-Kedge-Org` and `X-Kedge-Workspace`.
3. Validates a matching entry exists in the User's
   `UserMembershipIndex.spec.entries`. Else 403.
4. Stuffs `{user, org, workspace, role}` into `r.Context()`.

No server-side "active org" state — switching is purely a header swap.
Two browser tabs with two different active Orgs work as expected.

### Be a member of many Orgs

A User has any number of Memberships. Memberships in different Orgs are
unrelated; admin in `acme` does not imply anything in `globex`. The
portal renders an org switcher built from the User's
`UserMembershipIndex`.

### Add another user to your Org

```
POST /api/orgs/{org-uuid}/members
{ "userRef": { "name": "bob" }, "role": "member" }
```

Requires caller is `role=admin` in this Org. Creates a Membership in
the Org workspace.

### Add another user to a single Workspace only

```
POST /api/orgs/{org-uuid}/workspaces/{ws-uuid}/members
{ "userRef": { "name": "bob" }, "role": "member" }
```

Requires caller is `role=admin` in either the Workspace or the parent
Org. Creates a Membership in the Workspace. Bob now sees that one
Workspace in his switcher but does **not** see sibling Workspaces in
the same Org.

### Remove a member

`DELETE /api/orgs/{org-uuid}/members/{user-name}` or
`DELETE /api/orgs/{org-uuid}/workspaces/{ws-uuid}/members/{user-name}`
— symmetric to the adds. Hub deletes the Membership; index controller
prunes `UserMembershipIndex.spec.entries`.

Per O-9: removing an Org-scoped Membership is **blocked** if the user
still has any Workspace-scoped Membership in that Org. Response 409
with a body listing the offending Workspaces. The portal calls
`?cascade=true` for the "remove from all" shortcut, which performs
the deletes server-side as one transaction.

### Delete a User

`DELETE /api/users/{name}` — per O-8, this is a soft-delete:

1. Hub sets `User.status.deletionRequestedAt = now()`.
2. Reconciler suspends sessions, hides the User from Org pickers, marks
   their Memberships inactive (still listed for audit but not honored).
3. After 30 days, the cascade controller deletes the personal Org +
   its Workspaces, all Memberships, and finally the User CR itself.
4. Inside the window, `POST /api/users/{name}/undelete` clears
   `deletionRequestedAt` and rehydrates Memberships.

### Leave an Org (self-service, O-12)

`DELETE /api/orgs/{org-uuid}/memberships/me` — caller removes
themselves from the Org without admin involvement.

Same blocks apply as admin-initiated removal:
- 409 if the caller has any child Workspace Membership in this Org
  (use `?cascade=true` to leave everything).
- 409 if the caller is the sole admin (must promote a successor first
  via Membership.role PATCH — see "Promote / demote an admin" below).

### Promote / demote an admin (O-12)

`PATCH /api/orgs/{org-uuid}/members/{user}` body `{ "role": "admin" }`
or `{ "role": "member" }`. Any existing Org admin can do this on any
Membership in their Org. Multiple admins are allowed. Same endpoint
shape exists for Workspace memberships at
`/api/orgs/{org}/workspaces/{ws}/members/{user}`.

### Delete an Org (O-13)

`DELETE /api/orgs/{org-uuid}` — soft-delete with 30-day grace,
symmetric with User delete:

1. Hub sets `Organization.status.deletionRequestedAt = now()`.
   Requires caller is admin in the Org.
2. The Org disappears from every member's switcher immediately;
   `GET /api/orgs/{uuid}/*` returns 404 except the undelete endpoint.
3. Inside the window, `POST /api/orgs/{uuid}/undelete` (any prior
   admin) clears the timestamp and rehydrates the switcher.
4. After 30 days, the cascade controller removes all child Workspaces
   (each going through its own cascade per O-13), Memberships,
   CatalogEntries, then the kcp Workspace + Organization CR itself.

Personal Orgs follow the same flow but only the owning User can
delete; cascade-time deletion is also triggered as part of the User
delete cascade (O-8).

### Delete a Workspace (O-13)

`DELETE /api/orgs/{org-uuid}/workspaces/{ws-uuid}` — soft-delete with
30-day grace.

1. Hub sets `Workspace.status.deletionRequestedAt = now()`.
   Requires caller is Workspace admin or Org admin (O-15).
2. The Workspace disappears from member switchers; existing
   exec-credentials targeting it stop being minted.
3. Inside the window, `POST .../undelete` restores it.
4. After 30 days, the cascade controller deletes all APIBindings,
   tenant objects (edges, MCP instances, ServiceAccounts, …),
   Memberships, then the kcp Workspace itself.

The cascade controller logs the count of objects being deleted in
each phase so an operator inspecting the audit log can see what was
lost.

---

## ServiceAccounts and tokens (O-14)

Bots and CI pipelines authenticate as `ServiceAccount` CRs scoped to
exactly one child Workspace. They are not Users; they do not appear in
the User CR list or in Memberships.

### `ServiceAccount` (workspace-scoped, `tenancy.kedge.faros.sh`)

```go
type ServiceAccount struct {
    metav1.TypeMeta
    metav1.ObjectMeta  // metadata.name = server-assigned UUID; spec.displayName carries the label

    Spec   ServiceAccountSpec
    Status ServiceAccountStatus
}

type ServiceAccountSpec struct {
    DisplayName string `json:"displayName"`

    // Role grants admin or member in the parent Workspace, mirroring
    // Membership.role.
    // +kubebuilder:validation:Enum=admin;member
    Role string `json:"role"`
}

type ServiceAccountStatus struct {
    // LastTokenIssuedAt is set by the token-issuance endpoint and
    // used by the rotation reminder UI.
    LastTokenIssuedAt *metav1.Time `json:"lastTokenIssuedAt,omitempty"`

    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

ServiceAccounts live in the child Workspace, written by the hub.
Workspace admin and Org admin both have permission to create them
(per O-15).

### Endpoints

```
POST   /api/orgs/{org}/workspaces/{ws}/serviceaccounts                       create SA (UUID assigned)
GET    /api/orgs/{org}/workspaces/{ws}/serviceaccounts                       list SAs in this Workspace
DELETE /api/orgs/{org}/workspaces/{ws}/serviceaccounts/{sa-uuid}             delete
POST   /api/orgs/{org}/workspaces/{ws}/serviceaccounts/{sa-uuid}/tokens      issue/rotate token; returns the only copy
DELETE /api/orgs/{org}/workspaces/{ws}/serviceaccounts/{sa-uuid}/tokens      revoke
```

Token issuance returns the credential exactly once (no Get endpoint
to retrieve it later — admins must store it themselves; lost tokens
require a rotation). Tokens carry the SA's `role` and Workspace UUID
as claims; the kedge kcp proxy honors them the same way it honors
User OIDC tokens.

### Lifecycle

- ServiceAccount belongs to its Workspace. Workspace soft-delete /
  cascade also takes the SA and revokes outstanding tokens.
- An SA's `role` can be patched the same way Membership.role is
  patched.
- Sole-admin block (O-9, O-12) ignores ServiceAccounts — they are
  not Users; an Org / Workspace cannot be "owned" by a SA.

---

## RBAC propagation

Capabilities split into two columns because of O-10: Org workspaces
are hub-mediated only (capabilities expressed through REST endpoints),
while child Workspaces are direct kcp access (capabilities expressed
through ClusterRoles in the workspace).

| Scope + Role | Hub-API capabilities (Org workspace, via REST) | Direct-kcp capabilities (child Workspace) |
|---|---|---|
| Org admin | create/delete child Workspaces, manage Org Memberships, manage Workspace Memberships in any child, publish Org-Private CatalogEntries, edit Organization.spec | implicit admin in all child Workspaces (see UX item 10) |
| Org member | list child Workspaces visible to them, see catalog, create child Workspaces if `workspaceCreation=members`, publish CatalogEntries if `catalogEntryCreation=members` | nothing — must be added to each Workspace explicitly |
| Workspace admin | (none specific to Org workspace beyond what they have as Org member, if any) | full access, manage Workspace Memberships |
| Workspace member | (none specific) | edit objects in the Workspace |

**Propagation mechanisms** (two of them, one per column):

- **Hub-API side**: the tenant middleware (`pkg/hub/server.go`)
  resolves Membership → role → permits/denies each REST endpoint based
  on the table above. No kcp ClusterRoles are needed in the Org
  workspace itself, because no tenant ever reaches it (O-10). The hub
  uses its own privileged ServiceAccount for kcp writes there.
- **Direct-kcp side**: cluster-scoped `ClusterRole`s
  (`kedge:workspace:admin`, `kedge:workspace:member`) bound via
  `ClusterRoleBinding` in each child Workspace to the user's
  `rbacIdentity` (existing pattern in
  [pkg/server/proxy/proxy.go](../pkg/server/proxy/proxy.go)). The
  Membership controller maintains these bindings.

---

## Personal Org

Bootstrap creates one Organization per User at User creation, with
`spec.personal: true` and `spec.displayName` defaulting to
`"{username}'s personal"` (editable). The user is the sole admin. The
User CR gains:

```go
type UserSpec struct {
    // ... existing fields ...

    // OrgQuota overrides the platform default (10) for this User.
    // 0 means use the default. Settable only by platform admin.
    // +optional
    OrgQuota int32 `json:"orgQuota,omitempty"`
}

type UserStatus struct {
    // ... existing fields ...

    // PersonalOrg is the UUID of the Organization auto-created for
    // this user. Set once at bootstrap; never reassigned. The portal
    // uses this as the default X-Kedge-Org when the user hasn't
    // explicitly switched orgs.
    PersonalOrg string `json:"personalOrg,omitempty"`

    // DeletionRequestedAt is set when a User delete is initiated; see
    // §Delete a User for the 30-day soft-delete flow (O-8).
    // +optional
    DeletionRequestedAt *metav1.Time `json:"deletionRequestedAt,omitempty"`
}
```

Why: gives every user an immediate place to create Workspaces without
choosing a name first. The personal Org also doubles as the home for
Personal-scoped CatalogEntries (see
[provider-scoping.md](./provider-scoping.md)).

Opt-out: a platform admin can disable personal orgs via a flag, in
which case users must be invited to an existing Org before they can
create any Workspace.

---

## Implementation order

Ten PRs:

1. **`Organization` CRD + bootstrap controller.** Creates the kcp
   Workspace, seeds the personal Org per User.
2. **`WorkspaceType: organization`** registered at hub boot +
   idempotent initializer (O-11) + post-init reconciler. Org
   workspaces become creatable.
3. **`WorkspaceType: workspace`** + idempotent initializer (O-11).
   Users can create Workspaces under their Orgs.
4. **`Membership` + `UserMembershipIndex` CRDs + controller.** Index
   stays in sync with Membership writes and Org/Workspace displayName
   patches.
5. **kcp-proxy Org-workspace gate (O-10).** Update
   [pkg/server/proxy/proxy.go](../pkg/server/proxy/proxy.go) to refuse
   exec-credentials for paths resolving to a workspace of type
   `organization`. Lands before any Org-workspace data is written by
   real users.
6. **Tenant middleware** in `pkg/hub/server.go` resolving headers →
   Membership lookup → request context. Required from day one (per
   O-2 there's no legacy fallback).
7. **Quota controllers** for O-5 (Orgs/User) and O-6 (Workspaces/Org).
8. **Soft-delete reconciler (O-8, O-13).** One controller covering
   User, Org, and Workspace deletion: tracks `deletionRequestedAt`,
   honors undelete inside the 30-day window, runs the cascade after.
9. **`ServiceAccount` CRD + token endpoints (O-14).** Bot identity
   surface for Workspaces, with the once-only token-issuance flow.
10. **Portal switcher UI + REST endpoints** for Org/Workspace/Membership
    CRUD (the hub-mediated surface from O-10), including the
    `?cascade=true` shortcut from O-9, self-leave (O-12), role PATCH
    (O-12), and undelete actions (O-8, O-13).

PRs 1-5 are bottom-up infra with no user-visible change. 6-10 turn it on.

---

## Open questions

Open after this round of decisions:

- **Nested Workspaces.** v1 makes Workspaces leaves. Allow nesting via
  `WorkspaceType: workspace`'s `limitAllowedChildren` — but inheritance
  of Memberships across nested workspaces is non-trivial. Deferred to v2.
- **Cross-workspace visibility.** A team that wants to see another
  workspace's edges read-only has no clean answer today. Probably
  `Membership.role=viewer` on the other workspace, but `viewer` is not
  v1. Deferred to v2.
- **Portal/CLI displayName caching.** Per O-1, rename is a
  `spec.displayName` patch. Audit anything that caches displayName
  longer than a request (CLI configs, browser localStorage) before
  shipping rename.
- **Sole-admin handling at User-delete cascade time (O-8 + O-12).** With
  multiple admins now allowed (O-12) the common case is solved: the
  remaining admins keep the Org. Edge case: a User who was the *only*
  admin of a non-personal Org dies in the soft-delete window without
  promoting anyone. Cascade controller behavior — auto-promote the
  oldest other Member, or delete the Org? Decide before shipping the
  cascade.
- **OIDC group → Membership sync.** If kedge is deployed against an
  IdP that publishes group claims, do those groups map automatically
  to Memberships? Out of scope for v1; flag for v2.

### Verification tasks (not decisions, but blocking)

These need to be confirmed against a real kcp before relying on the
design:

- kcp `Workspace` initializer atomicity — verified PARTIAL: async, no
  rollback. Initializers must be idempotent + self-healing (Membership
  controller checks for existing CRs before creating). See open
  question on whether to pin this as a decision.
- A controller can update a separate CRD (`UserMembershipIndex`) across
  the cluster with one ClusterRole — the easy case for O-3, expected to
  work but worth confirming.
- The kedge kcp proxy can selectively gate by workspace type (refusing
  exec-credentials for `organization`-typed workspaces) — load-bearing
  for O-10. Spike before PR #5.
