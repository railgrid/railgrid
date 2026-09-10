# Lessons learned: building Faros on kcp, the hard way

Status: living document. Base material for the talk "kcp — platform design the
hard way".

This is the honest record of where kcp's model and our assumptions collided
while building a multi-tenant provider platform: the tenancy model, APIExport
and permission-claim design, identity, topology, and the workarounds each one
forced. Every lesson names the constraint, what it cost us, and what we do
now. Sources are the design docs in this directory — each lesson links the
doc(s) that carry the full story.

Deliberately omitted: limitations we have since fixed upstream in kcp
(delegated TokenReview/SubjectAccessReview for APIExport providers,
TokenRequest for workspace ServiceAccounts). Stale claims about those still
exist in older docs here; do not re-learn them.

---

## Act 1 — Tenancy: the model you build around workspaces

### 1. Naming is hard: the workspace's kube name is a SYSTEM identifier. Treat it like a stable hash — the human-naming layer is DIY.

A kcp workspace's object name becomes its path, and the path is its address.
There is no rename — a "rename" is a new workspace plus a full content
migration, with no forwarding from the old path. And kcp itself embeds paths
into durable state everywhere: `APIBinding.spec.reference.export.path`,
`kcp.io/path` annotations, kubeconfig server URLs — plus whatever your own
controllers key by path (our kuery store rows are `<workspacePath>/<edge>`).

Put a human name in a path and one org rebrand ("acme" → "acme-corp")
dangles every binding export path in the org — kcp flips them NotReady, the
org's provider stops serving, every instance 404s — kills every developer
kubeconfig, and orphans every path-keyed row. A marketing decision becomes a
data migration.

So: name workspaces with stable opaque IDs (we use UUIDs), map user identity
*to* them, and accept that the entire human-facing naming layer — display
names, resolution, search — is yours to build; kcp offers nothing there. The
cost is permanent: `root:faros:tenants:86b7f9e7-…` in every kubeconfig, URL,
and debug session, and the path↔name resolution tooling is on you.
(`organizations.md` O-1)

### 2. Everything has two names — the workspace path and the logical cluster ID — and nothing hands you the pairing.

The hub injects tenant context as a *path* (`root:faros:orgs:acme`);
providers address workspaces by *cluster ID* in URLs. We have built the
path↔ID join at least three times: a provider-side TenantRef table in
Postgres, a hub topology index fed by an informer, and per-object
`kcp.io/path` annotations. Until a user first opens the UI, background
transcripts land under `{org: "unmapped", ws: clusterID}`.
(`hub-proxy-workspace-access.md` A-2, `agents-provider-improvement-plan.md`,
`app-studio-runtime-decoupling.md`)

### 3. WorkspaceType inheritance is all-or-nothing.

`extend: universal` bundles a capability grant (tenants can spawn child
workspaces) with a UX convenience (kcp auto-creates the `default` namespace).
We needed neither-with-the-other: dropping `universal` sealed tenants in, and
then `kubectl apply` of any namespaced resource failed with
`namespaces "default" not found` until the bootstrap controller compensated.
The positive counterpart: `limitAllowedParents` on a `universal` container
workspace let org-owned providers reuse the platform `provider` WorkspaceType
unchanged — zero new types for BYO. (`organizations.md`, `byo-providers.md`
§workspace layout)

### 4. Workspace initializers are async with no rollback.

"Verified PARTIAL: async, no rollback." A partial init leaves silent breakage
that surfaces as a tenant 403 much later. Every initializer must be
idempotent and shadowed by a self-healing reconciler — at which point we
abandoned initializers for the tenant workspace type entirely and let the
bootstrap controller drive all post-create wiring. (`organizations.md` O-11)

### 5. There is no cross-workspace membership or inventory index. You will build denormalized caches and pay for their drift.

"What orgs is Alice in?" is a fan-out over every org workspace, so we built a
cluster-scoped `UserMembershipIndex` maintained by two controllers that must
be kept from fighting. "Who enabled provider X?" is a fan-out over tenant
APIBindings. Listing CatalogEntries across orgs is O(orgs). Fine for
hundreds; awkward at tens of thousands. (`organizations.md` O-3,
`providers.md`, `provider-scoping.md`)

### 6. When RBAC and admission can't express a rule, the rule moves to the network — and forks your API in two.

"No APIBindings may exist in an org workspace" had no clean kcp expression,
so org workspaces became hub-mediated only: the proxy refuses to serve them.
The price is a parallel REST surface for membership/catalog/workspace CRUD
and a permanently two-column capability table ("Hub-API" vs "Direct-kcp").
(`organizations.md` O-10)

### 7. Our own gate ended up tighter than kcp's.

The hub proxy pinned every user token to `User.Spec.DefaultCluster` — a 403
*before* kcp was consulted, "regardless of whether the user actually has RBAC
there. So kcp would authorize them — but the proxy pre-check funnels
user-token traffic to the single DefaultCluster." Two subsystems then routed
*around our own platform*: App Studio went through the GraphQL gateway, and
provider-Enable went through a hub handler using the kcp-admin client — the
workaround for a too-tight gate was admin credentials. Lesson: if kcp's
authorizer can answer the question, let it; every pre-check you add is a
second policy engine you now maintain. (`hub-proxy-workspace-access.md`)

### 8. The tenancy corners you defer are product decisions users will hit.

Org admin has implicit admin in every child workspace ("the privacy boundary
is the Org, not the Workspace"). Workspaces are leaves because membership
inheritance across nesting was non-trivial. Cross-workspace read-only
visibility "has no clean answer today." Enable is per-workspace, so the same
provider shows Enabled in one tab and absent in the next. Each of these is a
kcp-model consequence surfacing as UX. (`organizations.md` O-15,
`provider-scoping.md`)

---

## Act 2 — APIExport and permission claims: the extension point's sharp edges

### 9. An APIExport can pin exactly ONE identity per claimed resource — for every consumer at once.

The flagship. `APIExport.spec.permissionClaims` is a list-map keyed on
(group, resource); a claim on another provider's resources must carry that
provider's `identityHash`, and there is one slot per resource *shared by all
consumers*. The moment one org self-hosts a dependency while others use the
platform copy, no single pin is correct. kcp intersects export claims and
binding claims on the full (group, resource, identityHash) triple everywhere
— labeler, reconciler, virtual workspace — so a binding carrying the "right"
per-workspace identity is validated against the export, never honored.
Enable-time refusal (a deliberate 409) was the best we could do; the real fix
was removing cross-provider claims entirely (lesson 14). Upstream wish:
resolve-on-bind — let the export claim omit the identity and the binding
carry the resolved one; the binding schema is already keyed on the triple.
(`byo-providers.md`, `pkg/hub/kcp/claimidentity.go`)

### 10. Identity is a hash of a secret, it rotates when you least expect it, and end users are forced to touch it.

The identityHash is derived from a secret (`kcp-system/<export-name>`), which
also determines the etcd prefix the export's data lives under. Re-creating an
export — which `ApplyAPIExport` does when it can't update in place — mints a
new identity *and reassigns the shard*, forcing every consumer to re-bind and
cascade-deleting in-flight CRs. "Discover it live … never a memorized value;
infra's hash has already rotated once." And it is "the only required value a
person cannot reasonably produce by hand" — the most kcp-specific concept our
users were ever forced to copy out of an admin debug view.
(`app-studio-browser-worker-to-infra.md` "Ops caveat (learned the hard way)",
`byo-providers.md`)

### 11. Claims are a distributed-config problem, not an API.

Every provider declares its claims in three places that must stay in sync by
hand: `init_cmd.go` (stamps the export), `manifest.yaml` (dev), and the chart
`catalogentry.yaml` (what the hub writes into tenant bindings at Enable). They
drift silently. kuery drifted **three ways at once** — init stamped
`faros.sh/edges`, the catalog declared `edges.faros.sh/kubernetesclusters`,
and the Makefile resolved the identity from a third export — a combination
under which Enable's identity poll could never succeed. Nothing detected it.
(`AGENTS.md` §5.1, this repo's kuery history)

### 12. The default failure mode of claims is silence.

A claim pinned to the wrong identity produces a binding that reports Ready
with PermissionClaimsValid=True — the claim is well-formed and resolves to a
*real* export, just not the one this workspace binds — and the dependent
provider sees an eternal 404. "Binds successfully and then silently sees none
of the resources it claimed." Downstream, our own reconciler treated that 404
as "object already deleted" and released finalizers over live instances. If
you build on claims, build the loud check yourself; kcp will not tell you.
(`claimidentity.go` package comment, `byo-providers.md`)

### 13. Claims grant you the virtual workspace, not the workspace — and the VW is a filtered universe.

"Permission claims don't help: they grant access via the APIExport virtual
workspace, not direct SAR passes in tenant workspaces." And the VW serves
*only* what the export declares: our catalog reconciler could not read
`LogicalCluster` through its own multicluster client — `providers.faros.sh`
declares nothing but `catalogentries` — so ownership resolution needed a
second, admin client. Wrong-scope addressing fails as silent zero-reconcile,
not as an error. (`kuery-provider-architecture.md`, `byo-providers.md`,
`app-studio-engine-retrofit-plan.md`)

### 14. The way out of cross-provider claims: act as a workspace-local identity through the workspace's own bindings.

The pattern that ended lessons 9–12 for us: the consuming provider
provisions a per-workspace ServiceAccount over *built-in* claims (which carry
no identity), and does its cross-provider reads/writes through the
workspace's own binding of the dependency — whichever copy that is. Identity
pinning disappears structurally. Discovery of enabled workspaces rides the
one thing every consumer exposes through the VW without any claim: the
reflexive APIBinding to your own export. (`provider-sdk/tenantaccess`,
`byo-providers.md`)

### 15. Every new GVK is a claims-and-bind upgrade across every consumer — so we flattened the API, and then had to reimplement the apiserver.

Adding a product as a new kind meant a new APIResourceSchema, a new entry in
the export, a new claim in every consuming provider, and every chart that
enumerated plurals changing. We flattened all products into ONE `Instance`
kind with `spec.template` as data. The bill: validation, defaulting, and CEL
no longer happen at admission — a controller runs the structural-schema
machinery and reports `Valid=False` as a *condition*, async, while last-good
keeps running. Product identity left the type system so the type system would
stop taxing the platform. (`infrastructure-flattened-instances.md`)

### 16. Some APIExport constraints improved the design.

An APIExport requires at least one schema; kuery had none, so it invented a
small `SavedView` CRD to satisfy the requirement — and got GitOps-able saved
queries out of it. Conversely, "the APIExport's `schemas: []` is a tell" was
the diagnostic that exposed a REST service wearing a kcp costume and started
the infrastructure rewrite. (`kuery-provider-architecture.md`,
`infrastructure-architecture.md` §1.2)

### 17. `bind` is not granted by default — and the grant we shipped is platform-wide.

kcp grants nobody `bind` on an APIExport; a platform must pre-grant it. Ours
grants `system:authenticated`, so org-scoped catalogs are a UI illusion: a
user who learns another org's UUID can hand-craft an APIBinding to that org's
provider export and kcp admission allows it. All tenancy enforcement lives in
the hub REST layer; kcp's admission has a flat view. The designed kcp-native
wall — MaximalPermissionPolicy — was never built; a hand-rolled
`tenantScoped` flag on claims is the actual safety mechanism.
(`byo-providers.md` known gaps — "the most important gap to close",
`providers.md` decision 10)

### 18. Blanket `secrets` claims are the universal side-door.

Claims are resource-coarse. Six providers held blanket `secrets` claims, and
that became the channel behind every implicit cross-provider credential
hand-off (one provider reading another's PAT out of a Secret it never
published an API for). Rule that survived the audit: claims on core Secrets
are for provider-owned material only; cross-provider secret reads are
forbidden. (`cross-provider-simplification.md` M3/X-4)

### 19. Re-Enable is destructive, and new claims arrive Rejected.

The hub rewrites permissionClaims only on Enable, and re-Enabling recreates
the tenant APIBinding — wiping the CRs bound through it. Adding a claim to an
already-enabled tenant means hand-patching the binding, where kcp adds new
claims as `state: Rejected` and you must kick the backoff with an annotation.
"NOT a checkbox — an operating rule." (`app-studio-engine-retrofit-plan.md`)

---

## Act 3 — Identity and authorization

### 20. Request-scoped bearers cannot drive long-running work. Every provider eventually mints workspace-local ServiceAccounts.

"A scheduled, heartbeat, wakeup or inbound-channel run has no human to act
as." Repository commits run long after the request that caused the edits.
Three providers independently converged on the same shape — an SA in the
tenant workspace + ClusterRole + token Secret, ownerRef'd to a CR so it
garbage-collects — before it became a shared package. Corollary: "token
present" is not "human watching"; gate interactive-only capabilities on the
*trigger class*, not on token presence. (`agent-web-access.md`,
`agents-invocation-api.md`, `provider-sdk/tenantaccess`)

### 21. kcp ServiceAccount tokens authenticate only in their home logical cluster.

The intuitive "provider calls provider with its own SA" is unbuildable; the
supported inversion is that the *caller holds an SA in the workspace it acts
in*. Where a foreign SA must be authorized anyway, the subject must be the
cluster-qualified form (`system:kcp:serviceaccount:{homeCluster}:{ns}:{name}`)
— a bare `system:serviceaccount:{ns}:{name}` is ambiguous and any tenant can
mint a same-named SA to satisfy the binding. (`kuery-provider-architecture.md`
key decision 2, `agents-invocation-api.md`)

### 22. kcp's workspace-content check runs before any RBAC rule in the workspace.

A foreign identity needs verb `access` on nonResourceURL `/` *in addition to*
its resource rules — "without it an invited outsider is denied even with a
perfect grant," and the SAR also drops the caller's groups. Every
cross-workspace grant we write pairs the two rules. Related: the RBAC subject
is the kcp username string (`faros:<email>`), not your User CR's name — the
CR name appears in no binding. (`app-studio-publishing.md`,
`kuery-provider-architecture.md`)

### 23. Virtual subresources that no server serves are kcp RBAC's best trick.

`agents/delegate`, `instances/access`, `tables/query_table` — RBAC
coordinates with no storage behind them. Granting a capability *is* writing
the rule; revoking removes it; `resourceNames` scopes it per object; `kubectl
get clusterrole` audits it. We learned this the expensive way: Provider
Actions first re-implemented grants as a hub Go authorizer — "same question,
two enforcement planes, different coverage… the single largest source of
conceptual confusion the audit found" — and ~1,400 lines were deleted by
using kcp RBAC on virtual subresources instead.
(`cross-provider-simplification.md` X-2, `provider-actions.md`)

### 24. Read kcp's source before inventing delegation.

kcp already carries `authorization.kcp.io/warrant` and
`authentication.kcp.io/scopes` — "a warrant lets the bearer act *under the
permissions of* another identity, but not *as* that identity" — and the
APIExport VW itself uses them. Its synthetic groups are stripped at the front
proxy and injected only by the component that already authorized them; any
"acting for tenant X" header we add needs the same treatment. And
impersonation as a primary mechanism has already produced a kcp security
advisory. (`platform-internal-networking.md`)

### 25. Fail closed with the right status code, and cache denials.

"Availability failures are 503, not 403. A caller seeing 403 goes and
rewrites RBAC; one seeing 503 retries." Authorization decisions are cached
including denials — a misconfigured caller in a retry loop must not become
load on kcp — keyed so an allow for one resource can never leak to another.
(`agents-invocation-api.md`)

---

## Act 4 — Topology and the data plane

### 26. `Shard.spec.virtualWorkspaceURL` is one global string, and it decides whether self-hosted providers are possible at all.

kcp copies it verbatim into every `APIExportEndpointSlice` endpoint — the
address every consumer dials, one per shard. Point it at an internal `.svc`
name and a provider in an org's own cluster "comes up healthy, serves its
API, wins its leader election … and simply never acts on anything a tenant
creates" — the control loop dies quietly on DNS. Point it at the public hub
and the hub's *own* managers fail the TLS handshake, silently freezing the
provider registry. Multi-shard cannot be fronted by one hostname at all:
nothing in the request names the shard. In-platform providers now hairpin out
through the public address as the cost of BYO working. One operational line
tells you if a platform can host BYO providers:
`kubectl get apiexportendpointslices -A -o jsonpath='…endpoints[*].url'` — if
the host is a `.svc` name, self-hosted providers will install and then sit
idle. (`byo-providers.md` "Platform prerequisite")

### 27. Everything is per-shard, and sharding leaks into every consumer.

One VW endpoint per shard: "binding one URL hides tenants." Consumers need
multi-shard URL extraction, shard probing, and a learned cluster→shard cache;
wrong-shard reads must fail loudly, not guess. Reverse tunnels have the same
shape — one tunnel per replica/shard or connection-aware routing; "there is
no third option," and our recommendation deliberately matches what kcp itself
does per-shard. (`app-studio-engine-retrofit-plan.md`,
`provider-horizontal-scaling.md`)

### 28. kcp gives you an outbound-friendly control plane — and then you discover your product is half data plane.

The provider-watches-tenants half dials *out* through the VW; NAT-friendly
for free. The hub-reaches-provider half — logs, proxy, exec, sync — is
inbound and "hub→provider is not configurable." A self-hosted provider
"reconciles Instances correctly and cannot serve a single data-plane call."
Everything we built before fixing this — public HTTPRoute per workload, an
nginx bearer sidecar, tokens bridged across clusters — "exists only to
compensate for the absence of an internal path" and was deleted after the
edges tunnel became the provider transport. Rejected on the record: public
ingress on the tenant side (confused deputy: the hub dialing a
tenant-controlled host with the caller's bearer attached).
(`byo-provider-edge-transport.md`, `platform-internal-networking.md`)

### 29. kcp built the tunnel we needed, then deleted it.

The TMC syncer tunnel modeled the reverse tunnel as an RBAC-gated subresource
on a first-class object (`synctargets/<name>/tunnel`), went per-shard, and
"was removed in June 2023 for lack of maintainers, not because the design
failed." Nothing in the ecosystem replaced it; that hole is what our edges
provider fills. When you build on a young control plane, features you depend
on can be deleted for non-technical reasons — read the pre-removal tree, it's
Apache-2.0. (`platform-internal-networking.md`)

### 30. `services/proxy` is the free tunnel, with a volume boundary and no APF protection.

The apiserver proxy subresource is how Gardener, vcluster, and Cluster API
reach workloads, and it is exempt from API Priority and Fairness — it won't
be throttled and won't protect the apiserver either. Control-plane-shaped
calls are fine; sustained end-user data traffic through it "is abuse of the
control plane." It resolves to a random ready endpoint (wrong for anything
stateful), and gRPC does not work through it. (`platform-internal-networking.md`)

### 31. The ergonomic multicluster abstraction is a scaling coupling.

Leader election worked only for providers whose managers host pure write
loops. Four providers pinned `replicaCount: 1` because the multicluster
manager doubled as the request path's tenant-client resolver — the consumer
takes exactly one thing from it: `cluster.GetClient()`. The scalable shape is
the raw primitive: read the endpoint-slice URLs yourself, probe per shard.
Where work is partitioned (per project, per edge), "leader election is the
wrong tool" — per-item claims with TTL takeover are; the pod-local state was
what actually pinned us ("with two replicas, the first poll that lands on the
wrong one kills a live run"). (`provider-horizontal-scaling.md`,
`app-studio-replica-awareness.md`)

---

## Act 5 — State, lifecycle, and the ecosystem

### 32. Spec belongs in kcp; execution state belongs in your database.

"A Run CRD existed briefly, was never instantiated, and only created room for
the schema and the execution reality to drift." Transcripts, checkpoints,
fire times live in the provider's Postgres; the APIExport resources are the
source of truth for spec. The counter-tension is real too: a `Session` CR
exists purely as a *projection* so `kubectl get` shows something — Postgres
stays authoritative. And every declared-but-unimplemented spec field "is a
silent no-op lying to users." (`agents-provider-architecture.md`,
`app-studio-engine-retrofit-plan.md`)

### 33. GC stops at the workspace boundary. Beyond it, you write finalizers, sweepers, and label-prunes.

OwnerRefs are the GC seam *inside* a workspace and nothing across it.
Cross-cluster teardown is finalizer-driven with the target persisted in
status ("so deletion works even if the Template is retired while instances
exist"); workspace deletion needs an orphan sweeper; edge-side workloads are
pruned by label + server-side apply because control-plane ownerRefs never
reach them. Deleting a provider leaves tenant APIBindings NotReady per kcp's
semantics — cleanup is your controller's job, best-effort.
(`infrastructure-flattened-instances.md`, `code-provider-architecture.md`,
`edges-marketplace.md`, `providers.md`)

### 34. Treat the kcp control plane as a low-trust, size-budgeted store.

No secret transits it in clear text — generated in-graph on the compute side,
bridged by reference, "kcp never sees any of these values." Rendered
manifests carry no secrets because they land in etcd. Watched objects are a
fan-out cost: CatalogEntry docs are capped (64 KiB) because every edit hits
every hub replica through the catalog watch.
(`application-template-architecture.md`,
`edges-marketplace.md`, `byo-providers.md`)

### 35. Don't fork ecosystem controllers to make them kcp-aware. Bridge them.

We ran a kro fork (`kro-multicluster`) against kcp workspaces, with a kcp
credential seeded onto the runtime cluster. The durable shape: kro runs
vanilla and single-cluster; *our* controller bridges kcp Instances to
per-instance kro CRs, and "the runtime cluster holds no kcp credential."
Same conclusion for kuery: upstream refactor over sidecar. Anything that
syncs kcp objects generically still has to learn APIExport identity
disambiguation — the identity hash leaks into third-party tooling.
(`infrastructure-flattened-instances.md`, `kuery-provider-architecture.md`)

---

## Act 6 — Meta-lessons

### 36. "Mirror their patterns; do not link their modules" means every trap is re-learned unless it is written down.

Provider isolation as policy guarantees pattern-copying — and with it, trap
repetition. That's why sentences like "the same trap the edges provider hit"
exist in our docs, and why this document exists. The shared pieces that
survived extraction into `provider-sdk` (leader election, tenant access,
install) are precisely the ones that encode a trap's answer.
(`agents-provider-architecture.md`)

### 37. Entropy is measurable. Audit it before it audits you.

Five providers in: "roughly 40 distinct channels … reducible to eight
mechanisms, three separate identity-minting implementations, and two
competing grant systems," plus dead channels nobody noticed — a permission
claim dangling on a resource its export no longer serves is invisible by
design (lesson 12, fleet edition). The target is three integration
primitives: bound APIs, declared verbs, MCP projections of both.
(`cross-provider-simplification.md`)

### 38. "Does the normal Kubernetes thing work here?" is a research task, not an assumption.

CEL immutability rules, initializer atomicity, VW behavior under RBAC,
cross-shard CachedResource — each shipped as a "verify with a spike" item,
and more than one came back PARTIAL. Budget for it: kcp is
Kubernetes-shaped until it isn't, and the difference is exactly where your
platform lives. (`organizations.md`, `provider-scoping.md`,
`agents-provider-architecture.md`)

### 39. Let designs be superseded in writing.

Our best sources for this document were docs that record their own defeat:
"Status: design draft — partly superseded by what shipped," "Ops caveat
(learned the hard way this session)," deviation sections listing what the
implementation did differently and why. The doc that never admits deviation
is the one that lies to the next reader. (`provider-scoping.md`,
`app-studio-runtime-decoupling.md`, `byo-provider-edge-transport.md`)

---

## Appendix: slide-ready quotes

- "We built a parallel identity system on top of kcp that disagrees with kcp."
- "The provider comes up healthy, serves its API, wins its leader election —
  and simply never acts on anything a tenant creates."
- "Binds successfully and then silently sees none of the resources it
  claimed."
- "So kcp would authorize them — but the proxy pre-check funnels user-token
  traffic to the single DefaultCluster."
- "Permission claims don't help: they grant access via the APIExport virtual
  workspace, not direct SAR passes in tenant workspaces."
- "One identity per claimed resource — for every consumer at once."
- "Discover it live … never a memorized value; infra's hash has already
  rotated once."
- "Re-Enabling RECREATES the tenant APIBinding and WIPES all Project CRs."
- "Removed in June 2023 for lack of maintainers, not because the design
  failed."
- "Same question, two enforcement planes, different coverage."
- "Leader election is the wrong tool here because the work is inherently
  partitioned by project, not singleton."
- "kcp evaluates [workspace-content `access`] before any RBAC rule in the
  workspace, so without it an invited outsider is denied even with a perfect
  grant."
- "Every dead field is a silent no-op lying to users."
- "Availability failures are 503, not 403."
