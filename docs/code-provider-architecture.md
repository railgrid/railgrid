# Code provider: git repository management

Status: **Historical design proposal**, with two current sections. Package
discovery and retry behavior are documented in
[the Code provider README](../providers/code/README.md); the
controller/backend ownership described below still applies, and the
staged-delivery list in section 7 is history. Sections 2 and 9 describe the
provider as it is today: the eight kinds it serves, and the two transient
artifact stores it keeps outside kcp under the contract's Pillar 1 carve-out.
Author: 2026-06-09
Related: `providers/infrastructure/` (the standalone-provider pattern this is modeled on), `pkg/hub/providers/` (CatalogEntry provisioning), `docs/providers.md`, `docs/infrastructure-architecture.md`.

## Summary

The **`code`** provider manages source-code repositories the same way the `infrastructure`
provider manages application templates. The motivation: when we provision infrastructure we
also need somewhere for the code to live — so a dedicated provider should, on demand, "give me
a repo, give me a deploy key", driven primarily by **MCP** and a **Vue portal UI**.

It is multi-backend ("sub-providers"): **GitHub first**, GitLab/others later, behind one
pluggable backend interface. v1 scope: **repo create / list / delete** and **access management
(deploy keys + collaborators)**.

The provider is a **standalone deployed service** discovered purely via a `CatalogEntry` —
identical packaging to `infrastructure`, but simpler: its CRDs are **tenant-authored** (not
platform-owned Templates), so there is no CachedResource, no virtual storage, and no kro.

## 1. Credential model (the central decision)

The open question was *which account creates the repositories*. Decision: **PAT-first, behind
a pluggable `Connection` abstraction**, so per-user/per-org **BYO-GitHub** can arrive later
without changing the consumer-facing API.

- `Connection.spec.type` is an enum: `pat | github-app | oauth`. v1 implements `pat`.
- A user onboards in the portal by pasting a Personal Access Token (stored as a `Secret`) and
  creating a `Connection`. Repos are created under the org/account that token controls
  (`Connection.spec.owner`).
- **Future:** `github-app` (the per-user/per-org install flow — short-lived, fine-grained,
  revocable installation tokens) and `oauth` slot in behind the same `Connection.spec.type` +
  a new backend implementation. Consumers (Repository/DeployKey/Collaborator, MCP, portal) are
  unaffected.

## 2. API surface

Group **`code.railgrid.ai`**. All CRDs are **cluster-scoped**, **tenant-authored** (created
in the tenant's own workspace via the APIBinding), with a `status` subresource and standard
conditions/finalizers.

| Kind | Spec (key fields) | Status |
|---|---|---|
| **Connection** | `provider` (github), `type` (pat\|github-app\|oauth), `owner`, `secretRef`, `baseURL` | `Validated` condition, `login`, `scopes` |
| **Repository** | `connectionRef`, `name`, `owner?`, `visibility`, `description`, `defaultBranch`, `autoInit` | `htmlURL`, `cloneURL`, `sshURL`, `repoID`, conditions |
| **DeployKey** | `repositoryRef`, `publicKey?` (BYO) or generate, `readOnly` | `keyID`, `secretRef` (generated private key) |
| **Collaborator** | `repositoryRef`, `username`, `permission` (pull\|push\|admin) | conditions (e.g. `InvitationPending`) |
| **RepositoryCommit** | `repositoryRef`, `branch`, `message`, `source.bundleRef` (name + digest) | `phase`, `commitSHA`, `commitURL`, `files`, conditions |
| **RepositoryCheckout** | `repositoryRef`, `ref`, `paths` | `phase`, `resolvedCommit`, `files`, conditions |
| **RepositoryBuildStatus** | `repositoryRef`, `commit`, `state`, `context`, `targetURL` | `phase`, conditions |
| **Package** | discovered from the host; `repositoryRef`, `name`, `type`, `version` | `versions`, `lastCrawledAt`, conditions |

The first four kinds are the v1 surface; RepositoryCommit, RepositoryCheckout,
RepositoryBuildStatus and Package arrived with the MCP write tools, the runner
flow and package discovery. **Eight kinds, eight `APIResourceSchema` bodies**
(`deploy/chart/files/schemas/`), and **twelve catalogued actions** plus the one
uncatalogued upload verb of section 9.

DeployKey and Collaborator are **separate CRDs** (not arrays on Repository): one
controller-per-kind with finalizers and per-item status, avoiding racy read-modify-write of a
parent object.

## 3. Pluggable backend (sub-providers)

A `GitBackend` interface + a `Registry` copied from
[`providers/infrastructure/backend/interface.go`](https://github.com/railgrid/railgrid/blob/main/providers/infrastructure/backend/interface.go):

```
GitBackend {
  Name() string
  ValidateConnection(ctx, *Connection, creds) (login string, scopes []string, err error)
  EnsureRepository / DeleteRepository
  EnsureDeployKey  / DeleteDeployKey
  EnsureCollaborator / RemoveCollaborator
}
```

All ensure/delete methods are **idempotent**. Unlike infra's `Backend`, there is **no
`Run(ctx, vwConfig)`** — for `code`, the controllers own the watch loop and the backend is a
pure remote-API dispatcher. v1 implementation: `backend/github` using `google/go-github` +
`oauth2.StaticTokenSource`.

## 4. Controllers — multicluster

`code`'s CRs live across **every tenant workspace**, so the controller manager uses the
**multicluster** shape (the hub's wiring in `pkg/hub/server.go`), *not* infra's single-cluster
manager:

- `apiexport.New(cfg, "code.providers.railgrid.ai", …)` → `mcmanager.New(...)`.
- Each reconciler: `mcbuilder.ControllerManagedBy(mgr).For(&Repository{})`; inside
  `Reconcile(ctx, mcreconcile.Request)` it calls
  `mgr.GetCluster(ctx, req.ClusterName).GetClient()` to act in the tenant workspace.
- Four reconcilers: connection, repository, deploykey, collaborator.

**Credential resolution:** controllers read the PAT `Secret` via their own VW-scoped per-cluster
client (authorized by the `secrets` permission claim), **not** via a caller bearer token. The
caller-token factory is used only by MCP/portal paths. DeployKey writes the generated private
key as a `Secret` in the tenant workspace with an `ownerReference` to the DeployKey CR — the GC
seam the `infrastructure` provider mounts to clone/push.

## 5. MCP + portal

- **MCP tools are CRD-native** (copy the infra pattern): they create/list/delete CRs in the
  tenant workspace *as the caller*; the controller does the real work. Tools:
  `list/get/create/delete_repository`, `add/list_deploy_key`, `add/remove_collaborator`,
  `list/validate_connection`, `create_connection` (references an existing Secret by name).
  **Pasting a PAT is a portal action, never an MCP tool** — the secret is never transported
  through MCP.
- **Portal:** `<railgrid-provider-code>` custom element with nav children **Connections** and
  **Repositories**. Views: Connections (paste PAT → Secret + Connection, show
  `Validated`/login/scopes), Repositories (list/create/delete), RepoDetail (deploy keys +
  collaborators).

## 6. Hub integration & manifest

**No hub code change.** A standalone provider is discovered purely by applying its
`manifest.yaml` `CatalogEntry`; the hub's `CatalogReconciler` + `provisioner` create the
sub-workspace, apply the schemas, mint the SA + kubeconfig.

Manifest specifics (the corrections vs infra):

- `apiExport.permissionClaims`: `secrets` with verbs `[get, list, watch, create, update,
  patch, delete]`, `tenantScoped: true` (write verbs are needed for the DeployKey private-key
  Secret; infra only needed read).
- `apiExport.schemas`: **NON-empty** — eight inline `APIResourceSchema` bodies (one per
  kind in section 2), applied by the hub
  with `storage: {crd: {}}`. Each body's `metadata.name` MUST follow the immutable
  content-versioned format `vYYMMDD-hash.<resource>.code.railgrid.ai` (required by the
  provisioner's `splitSchemaName`).

## 7. Staged delivery

- **PR A — scaffold:** new Go module `providers/code/` (added to `go.work`); API types + CRDs;
  `GitBackend` interface + stub backend; multicluster controller-manager wiring with no-op
  reconciler skeletons; manifest (4 inline schemas, widened secrets claim); Helm chart; portal
  shell. Builds and registers against the hub; the stub flips a Connection to `Validated=true`.
- **PR B — GitHub backend (done):** `backend/github` using `go-github` + a PAT token source —
  `ValidateConnection` (login + `X-OAuth-Scopes`) and `EnsureRepository`/`DeleteRepository`. The
  backend is registered in place of the stub, and the provider now ensures its
  `APIExportEndpointSlice` at startup (section 8). The Connection + Repository controllers were
  already functional against the registry, so they pick up the real backend unchanged.
- **PR C — access + MCP (done):** the github backend's deploy-key + collaborator methods
  (idempotent), the DeployKey controller (ed25519 keygen when no BYO key → private-key Secret
  owned by the CR) and Collaborator controller (grant/revoke + InvitationPending), and CRD-native
  MCP write tools (`create_connection`, `create`/`delete_repository`, `add_deploy_key`,
  `add`/`remove_collaborator`).
- **PR D — portal:** Connections (paste PAT → Secret + Connection, show validation), Repositories
  (list/create/delete), RepoDetail (deploy keys + collaborators).
- **Later:** GitLab backend; `github-app`/`oauth` credential types (per-user UI onboarding).

## 8. Resolved: APIExportEndpointSlice

The hub provisioner does **not** create an `APIExportEndpointSlice` for provider APIExports —
the slice's name and export path are consumer-chosen, so it's the provider's job. The code
provider creates one (`code.providers.railgrid.ai`, referencing its APIExport at
`root:railgrid:providers:code`) idempotently: `serve` ensures it at controller-manager startup and
the `init` subcommand does the same for parity / out-of-band bootstrap. See
`providers/code/install/endpointslice.go` (modeled on the infrastructure provider's
`PlatformAPIExportEndpointSlice`).

## 9. Transient artifacts

Two on-disk stores sit beside the CRs, and both are the **transient artifacts**
carve-out in [provider-connectivity-contract.md](./provider-connectivity-contract.md)
§"Pillar 1 carve-outs" — an uploaded bundle may live on a PVC while the CR
carries its reference and digest, provided it is consumed-and-deleted or swept
on a fixed TTL and **nothing is lost if it is gone**. Neither store is ever the
authority: the CR is.

**Commit bundles** (`commitbundle/store.go`, `CODE_COMMIT_BUNDLE_DIR`). One
executor (`commitexec.Create`) writes the files it was handed into a
content-addressed bundle, then creates a `RepositoryCommit` whose
`spec.source.bundleRef` carries only the bundle's name and digest — the bytes
never enter an API object. Two surfaces call it and neither owns it: the
`repositories/commit/v1` action, which is the contract surface — kcp
authorizes the `repositories/commit` subresource, the gate reviews visibility
for the stamped caller — and the MCP `commit_files` tool, which is a
projection of the same executor for interactive clients. The store is scoped by
the tenant's kcp logical-cluster ID (`X-Railgrid-Cluster` on the MCP request,
the path cluster on an action, `req.ClusterName` in the reconciler: the same
key on all three sides). The RepositoryCommit controller reads the bundle, commits it, and
deletes it; a commit that fails deletes it too. A `Put` announces the arrival
in-process (`commitbundle.Notifier`), which is what wakes a controller that
reached its RepositoryCommit before the bundle landed — the 30-second arrival
bound is only the backstop for a bundle that never arrives. A one-hour sweeper
(`DefaultSweepMaxAge`) reclaims what a crash orphaned. If the bundle is gone
the commit fails and is retried by its author; nothing is unrecoverable, which
is exactly the condition the carve-out sets. Running more than one replica
without shared storage for this directory means a commit can land on a replica
that cannot see its bundle.

**Git snapshots** (`actions/snapshots.go`, under the same directory). The
`stage-snapshot` verb accepts a git bundle and returns an opaque `bundleRef`
scoped by tenant cluster, Repository UID, Connection UID and the caller's own
credential, so a handle is useless to anyone else — and re-uploading after a
credential rotation is expected, not a bug. Handles expire after an hour, are
swept lazily on the next upload, and are bounded per tenant (16 artifacts,
256 MiB). `prepare-snapshot` and `publish-snapshot` take the handle, never an
inline bundle, and re-verify its digest before use.

**Staged commit bundles** (`actions/commit.go`). `commit/v1` is catalogued and
therefore bounded at the CatalogEntry's 1 MiB input ceiling, which is smaller
than a generated application. A caller with more than that uploads the file
list through `stage-commit-bundle`, which writes it into the commit-bundle
store above under the request's cluster scope and returns the
`bundleRef`/`bundleDigest` pair; `commit` then names the handle instead of
inline `files`, re-reads it digest-verified, and creates the same
`RepositoryCommit`. A staged bundle nobody commits is reclaimed by the same
one-hour sweeper as any other orphan.

`stage-snapshot` and `stage-commit-bundle` are the provider's only
**uncatalogued** verbs: a 25 MiB or 48 MiB body cannot be declared under
`CatalogEntry.spec.actions[].limits.maxInputBytes`, which the CatalogEntry API
caps at 1 MiB. Both are declared as data-plane verbs, so they are custom
subresources `repositories/{verb}` at
`/clusters/{id}/apis/code.railgrid.ai/v1alpha1/repositories/{name}/{verb}`
and gated exactly like every catalogued action. The exception, and the four conditions a
verb has to meet to claim it, are written down in
[provider-actions.md](./provider-actions.md) §"Uncatalogued large-upload
verbs".

---

## `commit` — writing files without a git host round-trip

Added 20 September 2026
([provider-contract-remediation.md](./roadmap/provider-contract-remediation.md)
§9 Cut D.1; it closes the "code provider: a `repositories/commit` action"
follow-up recorded on that plan).

```
POST /clusters/{id}/apis/code.railgrid.ai/v1alpha1/repositories/{name}/commit
POST /clusters/{id}/apis/code.railgrid.ai/v1alpha1/repositories/{name}/stage-commit-bundle   (uncatalogued)
```

(The action's `/v1` is its catalog id, not a path segment; `provider-sdk/serve`
restores it from the declaration.)

**Why it exists.** A consumer that generates code — App Studio, above all —
needs to put file contents somewhere only this provider can write, and then
have a `RepositoryCommit` point at them. Until now the only way to do that was
the `commit_files` MCP tool, which meant a background reconciler had to reach
the tenant's MCP aggregate, hold `use` on an `MCPServer`, and parse a tool's
prose error to learn the name of the object it had just created. None of that
is the data-plane contract; all of it was load-bearing.

**Shape.** Input is `{repositoryUID, message?, branch?, files[]}` or
`{repositoryUID, message?, branch?, bundleRef, bundleDigest}`; output is
`{commit: {name, uid}}`. `repositoryUID` pins what gate 1 returned against this
provider's own read through its APIExport, exactly as every other repository
action does — there is simply no Connection and no credential to resolve,
because the verb never reaches a git host.

**Who writes the CR.** The provider, through its own export client, after the
gate has passed. The caller proves it may commit (kcp's RBAC on the
`repositories/commit` subresource, then the gate's `get` review on the
Repository) and does not additionally need
`create` on `repositorycommits` in its own workspace — which is the point: a
consumer composes this provider's behaviour through a declared verb, not
through RBAC on a foreign kind. App Studio's project identity therefore gained
one clause-C rule and kept its read-only composition on `repositorycommits`.

**What it is not.** It is not synchronous in effect: it is declared
`executionMode: async` because the commit lands when the controller applies it,
and the result names the object to watch rather than a SHA. A consumer that
needs the outcome watches the `RepositoryCommit`; App Studio's project
reconciler already did exactly that for the rate-limited case, and now does it
for every commit.

---

## `mint-registry-token` — the one Connection-bound action

Added 19 September 2026
([provider-contract-remediation.md](./roadmap/provider-contract-remediation.md)
§9 Cut C.3).

```
POST /clusters/{id}/apis/code.railgrid.ai/v1alpha1/connections/{name}/mint-registry-token
```

Every other action this provider serves is bound to a `Repository`. This one is
bound to a `Connection`, because what it hands out is derived from the
Connection's credential and nothing else.

**Why it exists.** A container image built from a tenant's repository lives in
that repository's package registry, and a workload cluster needs a credential
to pull it. That credential used to be made by App Studio: it read this
provider's `Connection` Secret and re-minted the raw token into a
`dockerconfigjson` (`api/project_promote.go`). Two things were wrong with it.
The consumer had to hold the credential that can also **push code** in order to
produce one that only needs to **pull**; and it had to know that "the Code
provider keeps a git token under `spec.secretRef`", which is a coupling by
Secret layout rather than by contract
([cross-provider-simplification.md](./cross-provider-simplification.md) §2.1).

**What it returns.** `{registry, username, token, expiresAt?, scoped}` — a pull
credential and what is known about it. Never the Connection's own credential
under another name.

For a **GitHub App** connection the token is a fresh installation token
requested with `permissions: {packages: read}` and about an hour to live
(`tenant/registry_token.go`, `RegistryPullPermissions`). That is the narrowest
credential GitHub will issue, and it matters because a pull secret sits on a
runtime cluster for as long as the workload does.

For a **PAT or OAuth** connection there is no narrowing API. The stored token
is returned with `scoped: false` and no expiry, and the action says so rather
than implying a least-privilege credential it did not issue. A consumer that
requires a genuinely scoped pull secret can refuse an unscoped one. The
credential still never leaves this provider's control path, and the consumer
still never reads the Secret.

**Authorization** is the ordinary pair: kcp authorizes the caller for the
`connections/mint-registry-token` subresource scoped to its name, and the gate
reviews `get` on the `Connection` for the stamped caller. A grant on
`repositories/*` does not reach it and vice versa — the
point of moving the credential behind an action rather than leaving it a Secret
read (`actions/server_test.go`,
`TestConnectionActionIsGatedSeparatelyFromRepositoryActions`). The caller then
pins what it saw with `connectionUID`, and this provider re-reads the
Connection through its own APIExport before opening the Secret, so a Connection
deleted and recreated under the same name between the two reads fails closed.

It is catalogued `readOnly: true`: it mints a credential but changes nothing
about the Connection, the repository or the registry.
