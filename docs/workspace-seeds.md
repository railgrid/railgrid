# Workspace seeds — backend functionality in every workspace

**Status:** Implemented in the hub (`pkg/hub/workspaceseeds`, `pkg/hub/kcp/workspaceseed.go`). No seed ships with the hub.
**Last updated:** 2026-11-13
**Reads beside:** [providers.md](./providers.md), [provider-scoping.md](./provider-scoping.md)

A provider is something a tenant sees and enables: it has a `CatalogEntry`, a
place in the portal, and an Enable step per workspace. Some platform services
are neither. A metered model gateway, for example, should be in every
workspace from the moment it exists, show up nowhere in the catalog, and hand
the tenant one object they can read and not multiply.

A `WorkspaceSeed` (`admin.railgrid.ai/v1alpha1`) is how such a service reaches
every tenant workspace. It says two things: **bind these exports** and
**create these objects**.

```yaml
apiVersion: admin.railgrid.ai/v1alpha1
kind: WorkspaceSeed
metadata:
  name: switchboard
spec:
  bindings:
  - name: switchboard                  # the APIBinding's name in each workspace
    export:
      path: root:railgrid:system:switchboard
      name: switchboard.railgrid.ai
    tenantAccess: ReadOnly             # ReadWrite (default) | ReadOnly | None
    permissionClaims:                  # accepted exactly as listed
    - group: ""
      resource: secrets
      verbs: ["get", "list", "watch", "create", "update", "delete"]
      selector:
        matchExpressions:
        - key: switchboard.railgrid.ai/request
          operator: Exists
  objects:
  - resource: requests                 # plural of the manifest's kind
    manifest:
      apiVersion: switchboard.railgrid.ai/v1alpha1
      kind: Request
      metadata: {name: free, namespace: default}
      spec: {policy: free}
  - resource: modelproviderconnections
    whenBound:                         # only where this export is bound too
    - path: root:railgrid:providers:agents
      name: agents.railgrid.ai
    manifest: {...}
```

Seeds live in `root:railgrid:system:providers`, next to `Provider` objects, and
are served by the `admin.railgrid.ai` export, which is bound nowhere else.

## What the hub does

For every tenant workspace (`root:railgrid:tenants:<org>:<workspace>`; not an
organization's own workspace, an edge mount, or a provider workspace) and
every accepted seed:

1. **Bindings.** Creates each APIBinding with the hub's own credential, the
   listed permission claims `Accepted`, and two annotations:
   `admin.railgrid.ai/workspace-seed` (the seed's name) and
   `admin.railgrid.ai/tenant-access`. An existing binding of the seed is
   brought back to the seed's claims and access.
2. **Objects.** Once every binding of the seed is `Bound`, server-side applies
   each object under the field manager `railgrid-workspace-seed`, with force.
   An object with `whenBound` waits until each export it names has a `Bound`
   binding in that workspace, so an object of a provider's API appears where,
   and as soon as, that provider is enabled.

When it runs:

| Trigger | Reaches |
|---------|---------|
| An APIBinding in a workspace is created, changes or is deleted | that workspace |
| A seed is created, changed or accepted after having been refused | every tenant workspace the hub has engaged |
| The hub's leader starts | every tenant workspace |

A new workspace is seeded by the first row: the hub's own `railgrid` binding
is its first APIBinding. Seeding is asynchronous; workspace creation does not
wait for it. There is no timer, and **the hub does not watch the seeded
objects**: one a tenant deleted comes back at the next trigger, not at once.

## Tenant access

A provider's binding gives the workspace's members full access to the bound
API groups ([`tenantAdminRules`](../pkg/hub/kcp/tenant_admin.go)).
`tenantAccess` narrows that for a seeded binding:

| Value | Members get, on every resource of the bound groups |
|-------|-----------------------------------------------------|
| `ReadWrite` (default) | everything, as for a provider |
| `ReadOnly` | `get`, `list`, `watch` |
| `None` | nothing |

The hub records it as the `admin.railgrid.ai/tenant-access` annotation on the
binding and computes the member role from it. Members only read APIBindings,
so they can neither change the annotation nor unbind. When two bindings serve
the same group the narrower access holds, and an unrecognised value gives
nothing. The member role has no `bind` or `escalate`, so a member cannot write
a role that grants what theirs lacks.

With `ReadOnly` or `None`, only the hub and the export's own controllers
write those objects. That is what makes a seeded object something a tenant
cannot delete and create again, which matters when the object is a grant.

## What is refused

The hub validates every seed and reports the verdict in the `Accepted`
condition. A refused seed is applied nowhere.

- a binding named `railgrid`, or two bindings of one name;
- a claim on core `secrets` or `configmaps` without a selector, the same rule
  a provider's manifest is held to;
- an object of `rbac.authorization.k8s.io`, `apis.kcp.io`, `tenancy.kcp.io`,
  `core.kcp.io`, `admissionregistration.k8s.io` or `apiextensions.k8s.io`:
  seeds create data, not the workspace's access control or its bindings;
- a manifest without `apiVersion`, `kind` or `metadata.name`.

In a workspace, a binding of the seed's name that the seed does not own is
left alone and logged as an error for that workspace: one that references
another export, or one without the seed annotation.

## What to know before writing one

- **It is platform administration.** Whoever may write `WorkspaceSeed`s
  decides what runs in every tenant workspace and which claims are accepted
  there, with no tenant consent step. Nothing but access to
  `root:railgrid:system:providers` guards it.
- **Claims must match the export.** kcp reports `PermissionClaimsValid=False`
  and serves nothing for a claim whose group, resource, verbs or identity hash
  differ from what the export declares. A claim the export declares and the
  seed omits stays unaccepted.
- **An accepted selector is immutable in kcp.** Changing a claim's selector
  in a seed will fail on existing bindings; it takes deleting the binding,
  which deletes that workspace's objects of the API.
- **Nothing is ever removed.** Deleting a seed, or taking a binding or object
  out of it, leaves what was created. Removal is per workspace and by hand.
- **Manifests are literal.** The same object goes into every workspace; there
  is no templating. Give each consumer what it needs through fields of the
  object, not through per-workspace values.
- **`resource` is not checked against `kind`.** The hub runs no discovery per
  workspace; a wrong plural is a 404 in the hub's log for every workspace.
- **Status is per seed, not per workspace.** `Accepted` says the hub applies
  the seed, not how far the fleet has got. A workspace that fails is in the
  hub's log (`workspace-seed-apply`), with backoff.
- **Seeded bindings are not providers.** They do not appear in the portal's
  enabled-providers list, and the provider binding-claims reconciler leaves
  them alone, because their export is not under `root:railgrid:providers`.
  Put a seeded service's workspace under `root:railgrid:system`.

## What has been tested

`TestZWorkspaceSeed` in the provider e2e suite (`make e2e-provider`), against
embedded kcp: an existing workspace is bound and seeded, a label-`Exists`
claim on Secrets is accepted and enforced through the export's virtual
workspace, the tenant reads the seeded object and is refused every write, an
object gated on an unbound export is not created, and a seed claiming every
Secret is refused. Not covered there: a workspace created after the seed, a
`whenBound` object appearing when its provider is enabled later, and a change
to a seed reaching bindings that already exist. Those paths have unit tests
only.
