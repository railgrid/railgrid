# Bring-your-own providers (org-owned providers)

**Status:** Implemented (phase 1)
**Reads as a delta on:** [providers.md](./providers.md), [provider-scoping.md](./provider-scoping.md), [organizations.md](./organizations.md)

An organization can register a provider it runs itself — typically inside its
own Kubernetes cluster, reached over an edge — and have it appear in that
organization's provider catalog alongside the platform ones. Its Workspaces
Enable it through the ordinary Enable flow.

> **How the hub reaches an org-owned provider's backend** is a separate design:
> [byo-provider-edge-transport.md](./byo-provider-edge-transport.md). It makes
> the edge tunnel the transport (the hub cannot dial into a tenant cluster) and
> makes a connected edge a **precondition of registration**, so an install that
> could not work is refused before a credential is minted.

## Relationship to provider-scoping.md

[provider-scoping.md](./provider-scoping.md) pinned decisions for org-scoped
providers as **bring-your-own-URL**: a `CatalogEntry` in the Org workspace
pointing at a backend the Org hosts, explicitly with "no managed
ServiceAccount, no kcp provider workspace bootstrap."

This delta goes further, and supersedes that shape for the org scope. An
org-owned provider here gets a **real provider workspace and a real APIExport**,
so it can contribute CRDs to the workspaces that enable it — not just a UI and a
backend URL. Two consequences worth stating plainly:

- The `CatalogEntry` does **not** live in the Org workspace. It lives in the
  provider's own workspace, written by the provider's `init` exactly as a
  platform provider writes its own.
- P-1's UUID-identity and slug scheme is **not** implemented. Providers are
  still named, and names are unique within an Org. A name that matches a platform
  provider is allowed and means "we run our own copy of that one" (see
  *Name collisions* below).

Still unimplemented from that doc, and still relevant here: the per-Org `bind`
ClusterRole (P-3), `MaximalPermissionPolicy`, and the Disable confirm-gate (P-7).

## Workspace layout

The org-owned layout mirrors the platform one exactly, one level down:

```
root:faros
  providers:<name>                        platform provider workspace
  tenants:<orgUUID>                       Org workspace          (type: organization)
    <wsUUID>                              team workspace         (type: workspace)
    providers                             well-known container   (type: universal)
      <name>                              org provider workspace (type: provider)
```

`root:faros:tenants:<orgUUID>:providers` is a plain `universal` workspace, just
like `root:faros:providers`. That is the load-bearing detail: the `provider`
WorkspaceType's `limitAllowedParents` requires a universal parent, so making the
container universal lets each org provider reuse **the same `provider`
WorkspaceType** platform providers use. No new WorkspaceType ships with this
feature.

Reusing that type is what makes the rest fall out for free:

- It carries `defaultAPIBindings: providers.faros.sh`, so the workspace binds the
  CatalogEntry API on creation. The hub's catalog manager watches every cluster
  that binds `providers.faros.sh`, so an org provider joins the catalog watch
  **with no change to the manager** and no second watch.
- It has no `extend: universal`, so a provider holding cluster-admin over its own
  workspace still cannot spawn workspaces.
- `provider-sdk/install` (schemas → APIExport → endpoint slice → CatalogEntry)
  runs unmodified.

The container is created lazily on first registration — most Orgs never register
a provider, and an empty workspace each would be pure overhead.

`providers` is a reserved child name. It cannot collide with a team workspace,
which is always UUID-named.

## Registration flow

Everything below is hub-mediated. Per decision O-10 tenants hold no credential
that reaches an Org workspace, so an Org admin cannot build any of this by hand;
the REST surface is where the membership and role checks live.

1. `POST /api/orgs/{org}/providers` with `{"name": "vault"}`.
   The hub creates the `providers` container if needed, creates the provider
   workspace, mints a `provider` ServiceAccount that is cluster-admin **in that
   workspace only**, and returns a kubeconfig scoped to it.
2. The Org installs the provider's Helm chart against that kubeconfig. The
   chart's `init` container creates the APIExport, APIResourceSchemas, the
   `APIExportEndpointSlice`, and the `CatalogEntry` — all inside the provider's
   own workspace.
3. The hub's catalog reconciler observes the new `CatalogEntry`, resolves the
   workspace path from the cluster's `LogicalCluster` (`kcp.io/path`), attributes
   it to the Org, and upserts it into the registry under that Org's scope.
4. `GET /api/providers` now returns it for members of that Org, with
   `scope: "org"`. The portal renders it under **Self-managed**.
5. A Workspace enables it with the ordinary
   `POST /api/orgs/{org}/workspaces/{ws}/providers/{name}/enable`, including the
   same permission-claim consent dialog a platform provider gets.

Registration is idempotent: re-posting the same name returns the same workspace
and the same token, so the portal can retry safely.

## Platform prerequisite: a publicly dialable virtual-workspace URL

**A platform whose shard advertises a cluster-internal virtual-workspace URL
cannot host BYO providers at all.** This is a deployment prerequisite, not
something the hub can paper over, and it is invisible until an org actually
self-hosts something.

kcp copies `Shard.spec.virtualWorkspaceURL` verbatim into
`APIExportEndpointSlice.status.endpoints[].url`. That is the address every
consumer dials, and there is exactly one of them per shard. Point it at a
service DNS name and only consumers inside the shard's own cluster can reach it
— which is every in-platform provider, and no self-hosted one:

```
https://alpha-shard-kcp.kcp-system.svc.cluster.local:6443/services/apiexport/…
                        ^ resolves only inside the hub's cluster
```

A provider in an org's own cluster gets `no such host` from its own cluster DNS
and its multicluster manager never starts. The failure is quiet and badly
localized: the provider comes up healthy, serves its API, wins its leader
election, and reconciles **Templates** — those live in the provider's own
workspace and are reached over the provider kubeconfig, which is a different
URL and works fine. Only **Instances** break, because those live in tenant
workspaces and are watched through the virtual workspace. So the provider looks
fine and simply never acts on anything a tenant creates.

The fix is to make that URL reachable, and the right way to do it depends on how
many shards the platform runs.

### One shard, or embedded kcp — the hub can relay, but cannot yet advertise it

The hub relays `/services/apiexport/…` to kcp
([pkg/server/proxy/virtualworkspace.go](../pkg/server/proxy/virtualworkspace.go))
so that a single-shard platform could keep exactly one public address — one
HTTPRoute, one certificate, rather than a second entrypoint existing solely for
virtual workspaces.

**What is missing is a way to advertise it.** `Shard.spec.virtualWorkspaceURL`
is a single global value feeding every `APIExportEndpointSlice`, and the hub's
own multicluster managers are among its consumers: they dial those endpoints
in-process using kcp's CA and kcp's admin token. Point the field at the hub and
they fail the TLS handshake against the hub's own serving certificate —

```
http: TLS handshake error from 127.0.0.1:58754: remote error: tls: unknown certificate
```

— which silently freezes the provider registry. The catalog informer never
syncs, so the portal keeps serving whatever recipe it last saw while everything
else looks healthy. Setting the field by hand has exactly the same effect;
this is a property of the field, not of any default.

Closing this needs the hub's own controllers kept on a kcp-direct URL while
external consumers get the public one — an endpoint override on the multicluster
provider, or a per-consumer view of the slice. Until then the relay is unused,
and single-shard platforms are in the same position as multi-shard ones below.

Relayed requests are authorized structurally, without a lookup: the workspace
the caller's ServiceAccount token was minted in must be the workspace holding
the APIExport. A virtual workspace exists for its export's *owner* to observe
every consumer, and a provider's ServiceAccount lives in the same workspace as
the APIExport its `init` created — so owner-only is both the correct rule and a
single string comparison. Note this deliberately does not reuse the membership
authorizer: that answers "may this user read this workspace", and no amount of
consumer membership implies the right to read *every* consumer at once. kcp then
applies its own RBAC to the relayed request, so the hub narrows rather than
replaces it.

`--kcp-shard-virtual-workspace-url` sets the field directly for an embedded kcp
and wants `--kcp-shard-external-url` alongside it, which covers a different slot
in `Shard.spec`. It is subject to the same caveat: any value the hub itself
cannot dial with kcp's CA will freeze the registry.

### More than one shard — per-shard URLs

**The hub cannot front this, and the relay above must not be used for it.** A
multi-shard `APIExportEndpointSlice` publishes one endpoint *per shard*, each
derived from that shard's own `virtualWorkspaceURL`, and consumers watch all of
them. Nothing in the request distinguishes which shard is meant — the cluster
segment names the APIExport's workspace, not the shard serving it — so a single
hub hostname has no way to route them apart.

A SaaS deployment therefore gives each shard its own externally reachable
address and points that shard's `virtualWorkspaceURL` at it. kcp's front proxy
already serves `/services/…` and enforces APIExport ownership through its own
RBAC, so this needs no faros-side routing at all.

In either topology `virtualWorkspaceURL` is one global value per shard, so
in-platform providers dial the public address too and hairpin back in. A small
inefficiency, not a correctness problem.

To check a platform before promising an org it can self-host:

```sh
kubectl get apiexportendpointslices -A \
  -o jsonpath='{range .items[*]}{.status.endpoints[*].url}{"\n"}{end}'
```

If the host is a `.svc`/`.svc.cluster.local` name, a loopback, or a private
address, self-hosted providers will install and then sit idle.

## Self-hosting a platform provider

The flow above covers a provider an org wrote itself. The more common case is an
org wanting to run **the platform's own provider** in its cluster — its own
edges, its own Application Templates. A provider declares how it is deployed in
`CatalogEntry.spec.selfHosting`, and the hub renders per-organization install
instructions from it:

```yaml
selfHosting:
  supported: true
  chart:
    repository: "oci://ghcr.io/faroshq/charts"
    name: "faros-edges-provider"
    version: "0.1.4"          # stamped from .Chart.Version at release
  namespace: "faros-provider-edges"
  releaseName: "edges"
  requiredValues:
    - name: hub.externalURL
      value: "{{hubURL}}"          # substituted by the hub
      description: Address agents reach kcp through.
```

A `requiredValue` with no `value` is one the installer must type. Prefer a
placeholder wherever the hub already knows the answer — every value a person has
to look up and paste is a chance to get it wrong.

As a safety net, a value with no declared `value` is still filled in when the
hub knows it authoritatively: `hub.url` / `hub.externalURL` / `hub.internalURL`,
and the kubeconfig Secret name and key. That covers recipes published before a
placeholder existed, which travel with the provider's chart and so can lag the
hub. Provider-specific values the hub cannot know are still asked for.

### Where the values documentation comes from

The install panel lists only the values the hub could *not* resolve — the ones
the user has to act on. Everything else is already correct in the command, so
restating it would be a second copy to drift.

For the full reference, the chart **embeds its own README**:

```yaml
valuesDoc: |{{ .Files.Get "README.md" | nindent 10 }}
```

which lands in `spec.selfHosting.valuesDoc` and renders inline in the portal.
Embedding rather than linking buys three things:

- it works in an air-gapped or private-repo install;
- it documents the chart version **actually deployed**, not whatever is on the
  default branch;
- the portal shows it without a round trip to an external host.

`.Files.Get` returns the file verbatim (Helm does not template it), so a README
containing `{{ … }}` is safe to embed.

The field is capped at 64 KiB because CatalogEntries are watched objects — every
edit fans out through the catalog watch to every hub replica — so this must stay
a values reference, not a manual. Today's charts land at 4–13 KiB.

`docsURL` remains as the fallback for providers that would rather link out; the
portal renders the embedded doc when present and the link otherwise.

Markdown is rendered with raw HTML disabled. For an org-owned provider the
CatalogEntry is written by whoever runs it, so this is tenant-authored content
displayed in another tenant admin's browser.

The split is deliberate: the provider is the only party that knows its chart,
and the hub is the only party that knows the org's workspace, credential, and
address. Neither can produce the instructions alone.

Registering with `sourceProvider` set to a platform provider name renders that
provider's recipe:

```
POST /api/orgs/{org}/providers  {"name": "edges", "sourceProvider": "edges"}
```

The response carries the credential plus ordered steps: create the namespace,
store the credential as a Secret, `helm upgrade --install`. The portal renders
these under **Providers → Self-Hosting** with per-step copy buttons.

### The name is the platform provider's name

Self-hosting `edges` registers it as `edges`. The chart writes its CatalogEntry
under its own name, so anything else leaves the workspace and the registered
provider mismatched. The org's copy then **shadows** the platform's for that org
only — which is the intended meaning of "we run this ourselves".

One consequence to keep in view: a workspace that enabled the platform copy
before the switch is still bound to the platform export. Switching is a Disable
then Enable, not an in-place retarget, so
`GET .../providers/enabled` reports `selfHosted` per binding and the portal can
tell the two apart rather than showing both as plain "Enabled".

### Values the hub fills in

| Value | Source |
|---|---|
| `providerKubeconfig.secretName` | fixed — the charts hardcode data key `kubeconfig` |
| `catalogEntry.enabled=true` | so the copy self-registers into the org's workspace |
| `hub.url` | the hub's external URL; must be reachable from the org's cluster |
| identity hashes | resolved from the target APIExport, org's copy preferred |
| `{{workspacePath}}`, `{{namespace}}`, `{{releaseName}}`, `{{kubeconfigSecret}}`, `{{kubeconfigSecretKey}}`, `{{hubURL}}` | substituted into a recipe's literal values |

Identity-hash resolution is the one that matters most. It is the only required
value a person cannot reasonably produce by hand — today it is copied out of an
admin debug view — and getting it wrong yields a provider that binds
successfully and then silently sees none of the resources it claimed. When the
hub cannot resolve one it emits a visible placeholder and a warning rather than
a command that looks correct.

Rendering never fails. A provider with incomplete metadata still produces steps,
with placeholders and warnings for the gaps: the alternative leaves the user
holding a live credential and a fresh workspace with nothing telling them what
to do next.

### Providers that ship a recipe today

All nine, each embedding its own chart values reference:

| Provider | Values it still asks you for |
|---|---|
| `quickstart` | none |
| `edges` | none — `hub.externalURL` is derived from `{{hubURL}}` |
| `infrastructure` | none — self-bootstrap values use `{{workspacePath}}` / `{{kubeconfigSecret}}` |
| `code` | none |
| `databricks` | none |
| `kuery` | none — claims no first-party resources; edge discovery acts as a per-workspace ServiceAccount through each tenant's own edges binding, so no identity hashes are involved |
| `app-studio` | none — claims no first-party resources at all; its reconcilers act as workspace ServiceAccounts through each tenant's own bindings, so no identity hashes are involved |
| `agents` | `store.databaseURLSecretRef.name` — Postgres is its only hard dependency, and the hub cannot invent your database |

Identity hashes are declared with `identityFor` rather than as literals, so the
hub resolves them per organization — preferring the org's own copy of that
provider when it self-hosts one too.

Adding one to another provider means editing **both** `manifest.yaml` and
`deploy/chart/templates/catalogentry.yaml` — the chart copy is the one that
reaches production, and they drift silently (`AGENTS.md` §5.1).

### The workspace-path fix this depended on

Provider `init` used to hardcode `root:faros:providers:<name>` and write it into
`APIExportEndpointSlice.spec.export.path`, so a chart installed against an org
workspace published endpoints for an export that does not exist at that path.

`WorkspacePath` is now optional in `provider-sdk/install`, and an empty value
omits `spec.export.path` entirely — kcp then resolves the export in the slice's
own logical cluster, which is always where it is. The path was duplicating
information the kubeconfig already carries. Every provider's default is now
empty, which is what makes one published chart work in both a platform and an
org workspace with no values to set.

Already-deployed slices carrying an explicit path are left alone rather than
recreated: `spec.export` is immutable, so recreate is the only way to change it,
and that would briefly drop virtual-workspace endpoints out from under the hub's
multicluster managers for no behavioral gain.

## Endpoints

```
POST   /api/orgs/{org}/providers                      register; returns the install kubeconfig
GET    /api/orgs/{org}/providers                      list this Org's providers + registration state
DELETE /api/orgs/{org}/providers/{name}               delete the provider workspace (cascades)
GET    /api/orgs/{org}/providers/{name}/kubeconfig    re-fetch the install kubeconfig
```

Mutating calls honour `Organization.spec.catalogEntryCreation` (decision O-7):
`members` (the default) lets any Org member register a provider, `admin`
restricts it to Org admins. An unrecognized policy value is treated as the
restrictive setting rather than silently widening access.

The kubeconfig endpoint counts as mutating — it hands out a credential.
It re-mints from the ServiceAccount's existing token Secret, so it returns the
*same* credential rather than minting a second one; "I lost the kubeconfig" must
not silently multiply live credentials for one provider.

`GET /api/orgs/{org}/providers` merges two sources that legitimately disagree:
the kcp workspace (created at registration) and the catalog registry (populated
only once the chart has actually run). The gap between them is the
`registered: false` state — workspace exists, provider not installed yet — which
is the state an operator is most likely to be debugging.

## Isolation

What an Org gets is deliberately narrow.

- **The minted ServiceAccount is cluster-admin in its own provider workspace and
  nowhere else.** It cannot read the Org workspace above it or any team
  workspace beside it.
- **Cross-workspace reach only ever comes from the APIExport's permission
  claims**, which each consuming Workspace accepts individually at Enable time —
  the same consent gate platform providers pass.
- **Enable is hub-mediated**, so it resolves through `GetForOrg` and an Org can
  only ever enable its own providers or platform ones.

What is **not** yet isolated is the raw kcp `bind` verb — see *Known gaps*.

Registry scoping enforces the rest:

| Lookup | Scope | Why |
|---|---|---|
| `Get(name)` | platform only | Backs the bare-name request paths (UI/backend proxy, heartbeat) that carry no tenant context. If org records were reachable, an Org could name a provider after a platform one and capture its route. |
| `GetForOrg(org, name)` | Org's own, else platform | The tenant-scoped Enable path, where the Org is known and verified. |
| `ListForOrg(org)` | Org's own + platform | The catalog. An empty org means platform-only, never everything. |

The registry is keyed by `(orgUUID, name)`, so two Orgs can each register a
`vault` with no collision.

`GET /api/providers` runs behind `tenant.OptionalMiddleware`, which populates
the Org **only after verifying membership**. A supplied-but-unverifiable Org
degrades to the platform catalog rather than 403: the portal fetches this to
build its shell before any Org is selected, and dropping the context can only
ever show *less* than the caller asked for. Handlers behind that middleware must
treat an empty OrgUUID as "global scope only", never as "trusted".

### Ownership is derived from the workspace path, never from the object

The `CatalogEntry` is written by the provider's own `init`, so nothing on it can
be trusted to attribute ownership — a self-declared `ownerOrg` field would let
any provider claim another Org's scope. The reconciler instead reads the
`kcp.io/path` annotation from the cluster's `LogicalCluster` and derives the Org
from the path.

Two details are load-bearing:

- **The read uses the hub's kcp-admin config addressed at `/clusters/<id>`, not
  the reconcile request's multicluster client.** That client is scoped to the
  `providers.faros.sh` APIExport virtual workspace, and a VW serves only the
  resources its APIExport declares. `providers.faros.sh` declares nothing but
  `catalogentries`, so `core.kcp.io/LogicalCluster` is not reachable there at
  all — a read through it fails for every cluster.
- **A failed resolution fails the reconcile; it does not default the scope.**
  The default would be `""`, the platform-global scope, which is the *widest*
  one in the registry — so guessing it on failure would publish an Org's
  provider to every tenant and make it routable by bare name. Requeueing only
  delays the entry appearing. A *successful* read with no path annotation is
  different, and is treated as platform: every workspace the hub creates goes
  through the Workspace API, which always stamps the annotation, so an
  unannotated cluster cannot be an org provider workspace.

Successful lookups are cached per cluster (a workspace cannot be renamed or
re-parented); failures are not cached, so a transient error cannot pin a scope
for the process's lifetime.

## Name collisions

Registering a name that matches a platform provider is **allowed**, and is how
self-hosting works: an org running its own `edges` registers it as `edges`,
because the chart writes its CatalogEntry under its own name.

Resolution prefers the Org's own provider, so the copy shadows the platform one
**for that Org and no one else**. `Registry.Get` stays platform-only, so an org
copy can never capture a platform provider's proxy or heartbeat route by name;
only the tenant-scoped lookups (`GetForOrg`, `ListForOrg`) see it. Tests pin both
halves.

Names must be RFC1123 labels: the name becomes a kcp workspace name and appears
in URL paths.

## Known gaps

- **The agent holds cluster-admin in the tenant's cluster, and its ClusterRole
  now says so.** It used to be an allowlist of API groups, which read like a
  containment boundary and was not one: it granted
  `rbac.authorization.k8s.io/*` with `verbs: ["*"]`, and `*` covers `escalate`
  and `bind`, so the agent could always mint a ClusterRole with any permission
  and bind itself to it. Verified by doing exactly that with the old rules — the
  ServiceAccount self-granted `cluster-admin`, after which
  `auth can-i '*' '*'` returned `yes`.

  What the list did buy was a confusing failure. Installing a provider through
  an edge kubeconfig got far enough to create the CRD (`apiextensions.k8s.io`
  was on the list) and then died on the provider's own custom resource:

  ```
  infrastructureproviders.infrastructure.faros.sh "…" is forbidden:
  User "system:serviceaccount:faros-agent:faros-agent" cannot get …
  ```

  Bounding the agent for real means removing that escalate/bind path, which
  needs its own design — the agent legitimately creates RBAC for the workloads
  it deploys. Until then the grant is stated honestly rather than implying a
  limit that does not hold.

- **Nothing checks the virtual-workspace URL before handing out a credential.**
  On a multi-shard platform whose shards advertise unreachable virtual-workspace
  URLs (see [Platform prerequisite](#platform-prerequisite-a-publicly-dialable-virtual-workspace-url)),
  the hub will still register an org provider and render install instructions.
  The org installs successfully, sees a healthy provider with Templates
  reconciling, and discovers only later that Instances never move. The hub could
  read any existing `APIExportEndpointSlice` and warn at render time — it
  already has a `Warnings` channel the portal renders under *Needs your
  attention* — which would turn a multi-hour debug into a line of text.
  Single-shard and embedded platforms are not exposed to this: the hub fronts
  their virtual workspaces itself.
- **No heartbeat.** The heartbeat endpoint addresses providers by bare name with
  no tenant context, so it is platform-only — an org provider cannot beat, and
  cannot keep a platform provider of the same name looking alive. Org providers
  therefore never set `HeartbeatRequired`, leaving readiness resting on endpoint
  validity. An org-scoped heartbeat path is future work.
- **No hub-proxied UI or backend.** `/ui/providers/{name}` and
  `/services/providers/{name}` resolve globally. An org provider that declares
  `spec.ui.url` will not be proxied, and gets no default `iconURL` (the portal
  falls back to its generic glyph). API-only org providers — the common case —
  are unaffected: `EndpointsValid` now accepts an APIExport alone as "this
  provider offers something", which opens no route since the proxies
  independently 404 when the URLs are nil. Closing this is the subject of
  [byo-provider-edge-transport.md](./byo-provider-edge-transport.md), whose
  phase 2 routes both proxies over the edge tunnel.
- **No edge-driven install.** The Org installs the chart itself with the returned
  kubeconfig. One-click install onto a chosen edge needs credential projection
  into the edge cluster (the agent's `Placement` plane ships no kcp credential
  today) and is the natural next increment. Registration does now *require* a
  named, connected edge — see
  [byo-provider-edge-transport.md](./byo-provider-edge-transport.md) E-2 — so
  the target is known; only the credential projection is missing.
- **The `bind` verb is not org-scoped.** The provider's own `init` runs
  `provider-sdk/install`'s `ApplyBindGrant`, which grants `bind` on the APIExport
  to the group `system:authenticated` — platform-wide. Discovery and the
  hub-mediated Enable path are both org-scoped, but a user who learns another
  Org's UUID can hand-craft an `APIBinding` in their own team workspace
  referencing `root:faros:tenants:<otherOrg>:providers:<name>` and kcp's
  admission will allow the bind. Closing this needs the per-Org bind ClusterRole
  from [provider-scoping.md](./provider-scoping.md) P-3, which is unimplemented
  for platform providers too — for them the platform admin is the gatekeeper at
  onboard time, an argument that does not carry over to org-owned providers.
  **This is the most important gap to close.**
- **No `MaximalPermissionPolicy`.** An org provider's permission claims are
  capped only by what each consuming Workspace accepts at Enable. Same posture
  platform providers have today, but it is the thing to fix before any cross-org
  provider sharing.
- **Not federated into the aggregate MCP endpoint.** Federation forwards the
  caller's bearer token to each provider's `/mcp`, and the enumerator in
  `pkg/hub/server.go` has no verified tenant context, so it is restricted to
  platform providers. Including org-owned ones would leak one Org's user tokens
  to another Org's backend. Needs tenant context plumbed through the enumerator.
- **Deleting a provider leaves tenant APIBindings behind.** They go NotReady per
  kcp's semantics. Disable in each Workspace first for a clean teardown.

## Where the code lives

| Concern | File |
|---|---|
| Path constants + `SplitOrgProviderPath` | [pkg/kcppaths/paths.go](../pkg/kcppaths/paths.go) |
| Workspace lifecycle | [pkg/hub/kcp/orgproviders.go](../pkg/hub/kcp/orgproviders.go) |
| REST surface | [pkg/hub/restapi/org_providers.go](../pkg/hub/restapi/org_providers.go) |
| SA + kubeconfig mint (`…AtPath` variants) | [pkg/hub/providers/provision.go](../pkg/hub/providers/provision.go) |
| Registry scoping | [pkg/hub/providers/registry.go](../pkg/hub/providers/registry.go) |
| Scope resolution from cluster path | [pkg/hub/providers/controller.go](../pkg/hub/providers/controller.go) |
| Catalog DTO (`scope`, `ownerOrg`) | [pkg/hub/providers/api.go](../pkg/hub/providers/api.go) |
| Optional tenant context | [pkg/hub/tenant/middleware.go](../pkg/hub/tenant/middleware.go) |
| Enabled-binding filter | [pkg/hub/kcp/bootstrap.go](../pkg/hub/kcp/bootstrap.go) |
| Portal catalog section | [portal/src/pages/ProvidersPage.vue](../portal/src/pages/ProvidersPage.vue) |
