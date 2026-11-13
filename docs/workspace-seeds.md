# Workspace seeds — backend functionality in every workspace

**Status:** Implemented in the hub (`pkg/hub/workspaceseeds`, `pkg/hub/kcp/workspaceseed.go`, `pkg/hub/providers/system_workspace*.go`) and in the admin area. No seed ships with the hub.
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

## The whole flow

A service that is not a provider is onboarded in three steps, all in the
admin area (`/bonkers`) or with `kubectl` against
`root:railgrid:system:providers`:

1. **Create a system service** (admin area: *System services*). The hub
   provisions the workspace `root:railgrid:system:<name>` and a kubeconfig.
2. **Run the service's installer** with that kubeconfig. It creates the
   service's APIExports, schemas and configuration in the workspace.
3. **Create a workspace seed** (admin area: *Workspace seeds*) that binds
   those exports and creates the objects every workspace should have.

Step 3 comes after step 2 because a seed names exports, and those have to
exist to be bound. What the hub does with a seed saved before its export
exists has not been tested.

## System services

A `SystemWorkspace` (`admin.railgrid.ai/v1alpha1`) is the `Provider` object's
counterpart for these services. Creating one makes the hub provision:

- the workspace `root:railgrid:system:<name>`, of the `system-service`
  WorkspaceType;
- a `provider` ServiceAccount in it, bound to `cluster-admin` in that
  workspace;
- a kubeconfig for that ServiceAccount, in the Secret
  `<name>-system-kubeconfig` in `root:railgrid:system:providers`.

```yaml
apiVersion: admin.railgrid.ai/v1alpha1
kind: SystemWorkspace
metadata:
  name: switchboard        # -> root:railgrid:system:switchboard
spec:
  displayName: Switchboard
```

What differs from a provider's workspace, and why:

| | Provider | System service |
|---|---|---|
| Path | `root:railgrid:providers:<name>` | `root:railgrid:system:<name>` |
| CatalogEntry export bound | yes | no, so it cannot register in the catalog |
| ServiceAccount role | per `--provider-workspace-cluster-admin` | always `cluster-admin` in its own workspace |
| Reaches tenant workspaces | when a tenant enables it | through a workspace seed |

Neither kind of workspace serves `tenancy.kcp.io`, so neither can create
child workspaces, and the credential has no access outside its own
workspace.

The names `controllers`, `providers` and `tenants` are the hub's own and are
refused (`Ready=False`, reason `NameReserved`). A workspace of the requested
name that the hub did not create for this object, for example
`root:railgrid:system:metering`, is neither provisioned into nor deleted
(`Ready=False`, reason `WorkspaceNotOwned`): the hub recognises its own by the
`admin.railgrid.ai/system-workspace` annotation and the workspace type.

**Deleting a `SystemWorkspace` deletes the workspace and every APIExport in
it.** Tenant workspaces bound to those exports lose the API and its objects.
The admin API refuses the delete while a seed references the workspace;
`kubectl delete` does not check.

Credentials are rotated the way a provider's are
(`POST /api/admin/system-workspaces/{name}/credentials/rotate`): a second
token is issued, the Secret is rewritten, and the previous token stops working
after the grace period.

## The admin area

| Section | What it does |
|---------|--------------|
| *System services* | Create and delete system services, see whether the workspace is ready, download the kubeconfig, see which seeds reference each |
| *Workspace seeds* | List seeds with their `Accepted` condition, create one, edit its spec as YAML, delete it |

The seed editor sends the spec to the hub, which runs the validation below
before storing anything, so a seed the hub would refuse is rejected at save
with the reason, and unknown fields are an error instead of being dropped.
Saving has no confirmation step beyond the button: it reaches every tenant
workspace.

The routes are under `/api/admin`, gated by `--admin-users` like the rest of
the admin API: `system-workspaces` (GET, POST), `system-workspaces/{name}`
(DELETE), `system-workspaces/{name}/kubeconfig` (GET),
`system-workspaces/{name}/credentials/rotate` (POST), `workspace-seeds` (GET,
POST), `workspace-seeds/{name}` (PUT, DELETE).

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
  there, with no tenant consent step. What guards it is write access to
  `root:railgrid:system:providers` and membership of `--admin-users`, which
  the admin area's seed editor requires.
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
  Put a seeded service's workspace under `root:railgrid:system`, which is
  what creating a system service does.

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

`TestYSystemWorkspace` in the same suite: a `SystemWorkspace` becomes a
`system-service` workspace with a kubeconfig Secret, that credential creates
an APIResourceSchema and an APIExport in its workspace, finds no CatalogEntry
API there, cannot create a child workspace and cannot list Secrets in
`root:railgrid:system:providers`; a reserved name and a hand-made workspace
are refused, the hand-made workspace survives its `SystemWorkspace` being
deleted, and deleting a provisioned one removes the workspace and the Secret.
The admin routes have unit tests against a fake client only: the e2e hub runs
without `--admin-users`. The two admin sections are type-checked and their
helpers unit-tested; they have not been exercised in a browser against a
hub. Not tested: credential rotation for a system service, and the token sweep
that follows it.
