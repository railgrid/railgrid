# Bring your own provider

An organization administrator can register an authored provider, deploy it on
one of the organization's connected Kubernetes Edges, and enable it in selected
workspaces. A provider need not exist in the platform catalog. Self-hosting a
platform provider uses the same organization boundary and its published chart.

This describes the implementation in this checkout. It does not assert that a
hosted deployment has been upgraded or configured for this flow.

**Local E2E override:** the source-controlled hosted gate remains closed
(`organizationQuotaSelectionSupported = false`). This working copy adds only an
explicit local switch, `RAILGRID_LOCAL_E2E_BYO_QUOTA_BYPASS=1`, which the hub
accepts solely in dev mode with embedded KCP; Tilt sets it for this cluster
along with explicit BYO containment limits. The pinned KCP quota-accounting
defect remains unresolved. The local switch disables KCP ResourceQuota
admission, quota admission probes, and consumer quota inventory and coverage
checks. Railgrid object/request guards, organization authorization, source
ownership, and consent checks remain active. This does not establish hosted
release readiness. See
[KCP upstream requirements](upstream-kcp-requirements.md).

## Author and install

1. Build a provider using `provider-sdk/serve`, `dataplane`, and `install`.
   Supply a CatalogEntry manifest and the generated APIExport and schemas.
   Declare foreign requirements in `spec.requires`; name the provider which
   owns each foreign API group. Core Secret requirements need a selector.
2. As an organization admin, open **Providers → Self-Hosting → Register your
   own provider**. Choose the name from the manifest and the connected cluster
   that will host the deployment.
3. Store the returned provider kubeconfig as a Secret in that hosting cluster,
   using the hosting cluster's kubeconfig to run the install commands. The
   provider kubeconfig is a credential for Railgrid's control plane; it is not
   a credential for installing Helm charts on the hosting cluster.
4. Deploy your chart with that Secret mounted as
   `RAILGRID_PROVIDER_KUBECONFIG`. Configure the hub URL, provider name, generated
   kcp directory, and CatalogEntry file for your binary. The SDK's
   `install.Bootstrap` detects the organization workspace and submits the
   declaration to the hub. Do not apply APIExports, CRDs, endpoint slices, or
   RBAC directly with the provider credential.
5. Use `install.Options.PrivateCRDs` for provider-private CRDs, such as a
   controller's receipt store. The hub validates them and grants access to
   their instances. Conversion webhooks are forbidden. Provider controllers
   must reconcile all workspaces bound to their export, within the owning
   organization.
6. Send the SDK heartbeat only while the backend is ready. Check installation
   in the console, then review permissions and enable the provider in each
   intended workspace.

The manifest's backend address must identify a Kubernetes Service in the
selected Edge. Custom subresources use the authenticated
[organization provider relay](org-provider-relay.md); the shard never dials an
arbitrary URL supplied by the provider.

The UI must register the provider custom element and use
`railgridContext.fetch` through the SDK's PortalKit helpers. Organization UIs
run in an opaque-origin sandbox and cannot depend on console globals, shared
DOM, browser storage, or a raw login token. The bridge permits the active
workspace's own provider APIs and explicitly accepted, applied dependency
claims. An expanded requirement needs renewed consent; declaring it does not
make it available to the bridge. Platform-only dashboard and agent-overlay
mounts offer an organization provider link instead of executing its code in the
console document.

Browser Kubernetes requests from a BYO frame go through the hub-owned route
`/api/orgs/{org}/workspaces/{ws}/providers/{name}/sandbox/{rest}`. The hub
requires the request's org and workspace to match the authenticated tenant
context, accepts only canonical paths for that workspace's cluster, and
dispatches through the ordinary user-authenticated KCP API handler. The
caller's native KCP RBAC still applies. Before dispatch, the hub reads the
current provider binding and effective accepted/applied claims; a foreign
provider dependency also requires its exact current binding and an uncached
composition Grant decision. It repeats authorization before response writes
and stream flushes. If authorization state cannot be read, the request fails
closed. Core Secret reads are buffered as identity-encoded JSON and released
only after the latest selector and returned labels match. A revoke cannot
retract bytes already delivered, and an authorized mutation is not rolled back
if consent changes after the mutation has run.

Org-backed API, MCP/backend, UI-asset, and custom-subresource relay responses
use an allowlist for data and protocol headers. The hub drops provider cookies,
site-data clearing, reporting and origin-policy headers, trailers, and 1xx
responses; direct HTML/SVG navigation stays sandboxed, while validated
non-sandbox protocol upgrades retain only their sanitized handshake headers.
Trusted platform-provider response handling is unchanged.

An authenticated request to an org-owned backend receives a short-lived opaque
Railgrid identity token bound to the user, organization, workspace, provider,
export, and current APIBinding. It is not a KCP credential and cannot be replayed
directly against the KCP API. Backend KCP requests must return through the hub's
broker, which rechecks current membership, provider binding, accepted/applied
claims, and composition consent, then dispatches as the live user's RBAC
identity. The browser bridge applies the same provider permission boundary.
Hub REST calls also pass the provider's declared hub-access capabilities and
the workspace's current Grant. Tokens expire after ten minutes; each mediated
request checks live authority, while bytes already delivered cannot be
retracted. The old native delegated-account username prefix is retired by the
hub proxy and the embedded KCP authentication path before request impersonation,
including on virtual-workspace routes. Existing ServiceAccount and binding
objects are not removed as a startup sweep. Platform delegated credentials use
a new prefix and a separate hub-secret, account-bound TokenRequest audience.
A workspace member minting a native token for the same account cannot assume
the delegated user at hub endpoints. The secret audience also includes the
issuance minute: current and ten preceding minute buckets are accepted, so
copying an expired token's audience cannot renew delegation indefinitely.
Native tokens still expire after ten minutes; audience reuse is bounded to
less than eleven minutes from the bucket start. Active platform provider sessions must
obtain a fresh token.
Direct access to an external KCP shard is outside this embedded-shard policy and
requires a separate operator audit. The local E2E override does not enable
external-shard enrollment.

## Hosted operator boundary

Treat both the provider author and the organization's administrator as
untrusted with respect to Railgrid and other organizations. Admin-only
registration controls authority within the organization; it is not a security
review of the submitted code.

The hub owns bootstrap, storage endpoint publication, and grants. The runtime
credential cannot edit exports, endpoint slices, CRD definitions, roles, or
bindings. Bootstrap removes provider metadata/status, rebuilds claims from the
manifest, rejects reserved API groups and conversion webhooks, and replaces
custom-subresource references with its authenticated relay. Existing schema
names are immutable; changes need new versioned names.

The relay independently verifies the caller's consumer workspace belongs to
the provider's organization and has the exact export bound. The Edge receives
a short-lived signed envelope for the exact destination and request; the
provider backend receives neither the hub credential nor the envelope.
The relay guide describes external-shard transport, but transport configuration
alone does not provide the availability enforcement required for new enrollment.
The supported limit topology in this implementation is one embedded kcp process;
new organization-provider authority is refused with external kcp.

Organization UI code executes with `sandbox="allow-scripts"`, without
`allow-same-origin`. Its context contains no login token. Host-side requests
are scoped to the mounted organization and workspace, bounded, and cancelled
when authority changes. The provider can read or disclose data which its own
organization has authorized; isolation does not make malicious provider code
safe for that organization's data.

Team workspace administrators receive a tenant administration role, rather
than Kubernetes `cluster-admin`. It covers enabled provider API groups and
ordinary workspace data, but excludes API publication, conversion/webhook
configuration, impersonation, and privilege escalation. Direct publication of
custom APIs must use the provider registration flow. Workspace membership
does not authorize access to the provider infrastructure workspaces.

Provider workspaces use kcp's cluster-scoped ResourceQuota, including private
CRs. Deployment must enable the pinned kcp `KCPKubeResourceQuota` admission
plugin and quota controller. Enrollment waits for quota status and probes
admission with a dry-run create; unavailable enforcement fails enrollment.
The probe temporarily installs an `admin`-namespace cluster-wide quota named
`railgrid-quota-probe-<unique-id>` with `count/configmaps: 0`. While it exists,
**all ConfigMap creates in the workspace being probed are blocked** (the
provider workspace or its organization provider parent), including
concurrent runtime writes. The dry-run rejection must name that exact quota;
a rejection by another exhausted quota does not certify enforcement.
Cleanup normally runs immediately after the probe. A hub crash can leave it
behind: the five-minute expiry annotation is a cleanup eligibility timestamp,
not automatic deletion. The next enrollment removes expired probe artifacts.
If no enrollment follows, an operator must inspect the exact quota, verify its
`providers.railgrid.ai/quota-probe=true` label, expired
`providers.railgrid.ai/quota-probe-expires` timestamp, cluster-scoped annotation,
and sole `count/configmaps: 0` limit, then remove only that observed object using
a UID precondition. Do not delete ordinary capacity quotas or unexpired probes.

The implementation caps provider workspaces per organization and private
definitions per provider. Runtime Event writes are excluded because Kubernetes
does not count Events against ResourceQuota. Bootstrap also bounds payload
size and concurrent work. Railgrid-owned native admission checks full serialized
objects, including PATCH results and status updates. Authenticated shard and
virtual-workspace handlers share per-organization request, stream, lifetime and
body limits. These hooks use existing KCP extension points, without modifying
KCP or replacing its implementations.

Object counts multiplied by the admitted object-size ceiling give a conservative
bound for quota-governed logical allocations. Before issuing a new credential or
accepting a consumer, inventory checks refuse oversized existing objects and
incomplete discovery/listing results. This is not an exact physical etcd byte
account: hidden legacy identity data, system objects, encoding overhead, MVCC
history, tombstones and maintenance headroom still require an operator baseline
audit and capacity reserve. Do not call the logical allocation limit a physical
etcd quota or claim a general denial-of-service guarantee.

Consumer quotas use explicit operator limits for objects per resource family and
lifetime resource-family cardinality. They include the tenant-writable builtin
kinds and authoritative APIExport schema families, including ordinary kinds
absent from manifest action lists. Schema-version changes share one family;
retired family count entries are retained. Before binding or accepting composed
write access, preparation includes reachable stored kinds and bound platform
dependencies. A protected marker on the consumer LogicalCluster enables a
write-time admission check: a newly exposed stored resource remains unwritable
until the initialized, unscoped cluster-wide quota covers its GroupResource.
This also applies to later platform export changes. Re-run capacity preparation
before relying on those new kinds; snapshotting the catalog alone is insufficient.
Ordinary virtual-storage schemas that cannot supply the required storage proof
are refused in an allocated consumer rather than silently left unbounded.

An unbound, never-before-reserved family may be absent from consumer discovery
during first Enable or bootstrap. That absence is not proof that no hidden
legacy data exists; the flow relies on the family having no prior binding or
quota history. Its full hard counter is persisted before binding or export
publication. If native quota status has not initialized `status.used` for that
exact newly proposed family, preparation can finish once every mandatory
counter is observed. The native write guard still denies creates and growing
updates to the pending family until its status is initialized. Builtin,
currently bound, previously quota-recorded, and currently served proposed
families must be discoverable and fully inventoried; discovery or list errors
fail closed. Quota keys are retained across Disable, and an interrupted
preparation can leave a new key before its binding or export is published. If
that API is absent on a retry,
the hub cannot distinguish the interrupted reservation from retired data
hidden after Disable, so preparation remains fail-closed. Operators must
reconcile the workspace's binding, quota, and stored-object state before retry;
`status.used=0` alone is not proof that the family has no hidden objects.

The marker is observed by the native informer before preparation snapshots the
workspace. Removing the quota does not remove the requirement. Deletion and
strictly shrinking existing-object updates remain available for cleanup; an
arbitrary admin identity does not bypass coverage. Native quota/control metadata
and the single `admin` Namespace bootstrap are narrow structural exceptions.
Tenant administrators and provider credentials cannot delete the protected
quota or its `admin` Namespace, including through collection deletion. Native
workspace garbage collection retains its narrow control-plane exception.

Consumer workspace capacity is reserved by a persistent `consumer-<clusterID>`
Lease in the organization's protected providers-container workspace, under the
`admin` namespace. Native quota caps those Leases. Existing allocations must
match their recorded organization, workspace path, and logical-cluster identity.
They are not renewed, expired, or deleted on provider Disable: stored objects
and retired resource families can remain. Reclaiming a reservation requires an
operator to establish that the old workspace/data no longer consume the budget;
there is no automatic garbage collector that makes that decision. This bounds
new BYO consumer allocations, not every pre-existing ordinary organization
workspace or all baseline platform storage.

## Operator policy

New organization-provider authority is unavailable by default. Both native
traffic limits and the consumer allocation policy must be explicitly configured,
validated and ready. Registration, bootstrap, credential issuance/rotation and
Enable refuse missing enforcement before granting new authority. Read/list and
safe deletion remain available for cleanup. Lifecycle mutations serialize within
one organization; busy requests are rejected instead of creating an unbounded
waiter queue. Deletion and re-registration audit every remaining APIBinding
reference, including terminating references, so a stale catalog cannot revive a
deleted exporter.

Hosted release acceptance still requires the supported KCP quota-selection
diagnostic to pass against an unmodified dependency, and the source-controlled
gate stays closed until then. The local E2E switch bypasses that gate only for
this dev-mode embedded-KCP cluster; it is not a runtime override for a hosted
deployment. Railgrid's object/request containment policy and admission-guard
readiness checks continue to guard registration, bootstrap, credential
issuance/rotation, and consumer Enable. The local switch additionally bypasses
ResourceQuota-specific readiness and inventory checks.

The CLI exposes the same policy as `hub.organizationLimits` in the Helm chart:

| Control | Flags |
| --- | --- |
| Object and request bytes | `--org-max-object-bytes`, `--org-max-request-bytes` |
| Request rate and burst | `--org-requests-per-second`, `--org-request-burst` |
| Concurrent requests and streams | `--org-max-concurrent-requests`, `--org-max-concurrent-streams` |
| Request and stream lifetime | `--org-request-timeout`, `--org-stream-timeout` |
| In-memory limiter capacity | `--org-max-active-organizations` |
| Persistent consumer allocations | `--org-max-consumer-workspaces` |
| Lifetime stored-resource families | `--org-max-resource-types` |
| Objects in each family | `--org-max-objects-per-resource` |
| Quota-governed logical allocation | `--org-max-live-bytes` |

All limits are required together; no production-sized defaults are supplied.
The family budget includes builtin and retired families. The live-allocation
calculation includes all eight permitted provider workspaces and their maximum
private-resource counts, plus consumer allocations; invalid and overflowing
configurations are rejected. Choose limits from the actual deployment's capacity
and supported object sizes, including ordinary core/platform data affected by
the native guard. The limiter's active-organization capacity is not a count of
persisted organizations or a global storage reservation. Idle account slots
can be reclaimed only after requests and streams finish and the token bucket
fully refills, preserving the account's rate and burst limits.

Per-process counters reset on restart, and a forwarded request may be charged
on more than one HTTP hop. Pre-authentication ingress and platform-wide routes
outside organization scope require their own controls. External shards and
multiple independently serving processes are not covered by this policy.
Existing credentials, unsafe legacy grants and retained storage still need the
operator migration described below before hosted BYO is advertised as supported.

Legacy organization provider credentials with workspace-wide admin privileges
are denied by the retired native username-prefix policy on the hub and embedded
KCP authentication path before impersonation, including main and virtual
workspace routes. Their ServiceAccount and binding objects remain for operator
cleanup. External KCP deployments do not receive this embedded-shard policy and
need a separate credential audit before use. The new bootstrap refuses an
unmarked legacy workspace; adding a marker to a ServiceAccount is not enough.

Upgrading the hub-managed tenant administrator bindings is not sufficient to
trust an existing installation. A previously privileged tenant could have
created additional roles/bindings, APIExports, or conversion webhooks. Audit
and revoke those pre-existing privileges and unsafe definitions before
advertising the new boundary. The code does not silently delete tenant-authored
objects or certify a legacy deployment as safe. This migration requires
operator access and is separate from the local implementation tests. The
[historical credential audit runbook](historical-credential-audit.md) provides
a bounded, read-only inventory for one explicitly selected team or org-provider
workspace, coverage limits, and reviewed UID-specific remediation steps. It
does not infer the issuer of arbitrary credentials or certify a deployment.

## Verification

`make test-byo-providers` covers registration authorization, restricted
credentials, bootstrap admission, quota behavior, claim consent, the SDK/Edge
relay, and portal bridge contracts. The real browser sandbox smoke is a
separate check of browser-enforced isolation. The real
kind integration fixture and its topology limitations are described in the
relay guide. Passing local tests does not establish an external shard's TLS,
network policy, or operational capacity; validate those in the deployment
before enabling hosted BYO enrollment.

`make test-byo-availability` tests the Railgrid-owned guards against unmodified
dependencies in an isolated embedded kcp. It covers full-object byte limits,
actual virtual-workspace requests, wildcard-watch rejection, protected consumer
quota coverage and another organization remaining writable under the rejected
watch workload. Its limits are fixtures, not recommended deployment values;
the test is not a production latency or physical-storage capacity benchmark.
This guard acceptance does not prove KCP quota accounting. The mandatory release
check is `make test-kcp-quota-diagnostic`; it fails on the current pin and must
pass against a supported, unmodified dependency before hosted enrollment is
released. The local E2E override does not satisfy this release check.

The diagnostic intentionally fails on the affected pin; do not weaken the quota
or report full enrollment readiness from tests using a modified dependency.
Railgrid-owned admission and request guards use existing KCP extension points;
they do not modify KCP source or establish that the upstream quota defect is fixed.

## This local E2E session

Tilt explicitly sets `RAILGRID_LOCAL_E2E_BYO_QUOTA_BYPASS=1` for the hub.
This disables KCP `ResourceQuota` and `KCPKubeResourceQuota` admission, quota
admission probes, consumer quota inventory preparation, and consumer quota
coverage proofs. Railgrid's object-size admission and request guards remain
active, as do authentication, organization isolation, source ownership, and
consent checks. The hub rejects the switch outside dev mode with embedded KCP.
It is a user-approved local test bypass, not a fix or security acceptance
result. Remove the environment assignment to restore native quota admission
after testing. The local organization's metering workspace allowance was
separately overridden to 10 to accommodate provider workspaces.

The bypass is process-wide, not scoped to BYO. While the variable is set, every
organization and consumer quota path in this hub process skips its checks; it
is not safe to expose this hub, reuse its kubeconfig, or enroll another tenant
during the test. Follow-up: keep the bypass in a dedicated test process or
cluster, and eventually replace the environment switch with a test-only shim
scoped to one organization/provider (or, at minimum, fail closed when the hub
serves more than the designated test organization).

The adversarial review in this checkout was a code, focused-test, and live
boundary pass; it did not rerun the full live Tilt E2E. The last live pass
found Planner and Databricks `Running` and ready; Factory's init container was
still retrying `503 organization provider lifecycle unavailable`. Nothing in
this review should be read as a green end-to-end run.

### Live boundary probes

After restarting the local Tilt hub, the planner provider's Edge-mounted
`provider.kubeconfig` was used directly as a boundary probe. The credential is
a KCP ServiceAccount token for the planner provider workspace, not a user or
cluster-admin credential. It reported only its own workspace permissions
(secrets, configmaps, leases, its private receipts, discovery/SAR APIs, and
`apiexports/content`). Direct requests with that credential to the Databricks
provider workspace and to the owning organization workspace were rejected with
`Forbidden`. The same token was rejected by the hub's `/api/orgs`, organization
provider, and admin REST surfaces; only `/healthz` was accessible without user
authentication. A disposable second organization was then created as a canary;
the planner credential was denied read, server-side dry-run write, and
APIExport enumeration against its workspace. These are denial checks, not proof
that every relay, UI, or in-flight authorization path is safe. This pass did
not exercise provider-issued requests or relay-token replay.

After the restart, the Planner and Databricks provider pods reached `2/2
Running` with readiness `200`; the Factory provider remained stopped with its
init container reporting `503 organization provider lifecycle unavailable`.
That is a live-control failure, not evidence that the Factory boundary passed.

## Adversarial findings: local E2E compromises

The items below are known weaknesses of this branch or of the local Tilt
cluster. None is a hosted acceptance result. Each is deliberately recorded so it
can be fixed or retested rather than disappearing into a green local run.

### Host vs self-hosted: what the BYO boundary gets right

Platform providers are operator-installed code and have historically been
trusted with host capabilities. An organization-owned provider is tenant code
and is treated more like an untrusted extension. That asymmetry is deliberate,
and on several boundaries the self-hosted BYO path is materially stronger:

| Boundary | Platform provider | Organization BYO provider |
| --- | --- | --- |
| Caller credential at the backend | Receives the caller's hub bearer by default (`--provider-delegated-tokens=off`) | Never receives the hub bearer or a KCP ServiceAccount token; receives a short-lived, live-checked opaque relay credential |
| UI execution | Trusted custom element in the portal document; the platform bundle path still has an unpinned fallback | Opaque-origin iframe with `sandbox="allow-scripts"`, a token-free MessageChannel context, and a host-verified `/main.js` pin |
| Heartbeat identity | `warn` is the default and accepts a beat whose token does not verify | Authentication is always required, even when the hub is in `warn` mode |
| Provider authorization | Registry and binding policy, with platform-only compatibility defaults | Rechecks the live provider source, exact target workspace, exact APIBinding, applied/accepted claims and uncached composition Grant on each identity request |
| Provider workspace role | Cluster administrator by default for this release | Restricted bootstrap policy; no direct declaration or RBAC write access |
| Declared hub capabilities | Allowed by default until a tenant records a decision | Require an explicit tenant decision |

The honest conclusion is narrower than "BYO is safer". BYO is stronger at the
caller-credential, browser, heartbeat and workspace-privilege boundaries. Its
backend and UI are still attacker-controlled code with a path to the APIs the
tenant enabled, and the replay and in-flight revocation gaps below still apply.
The platform path remains weaker because operator trust substitutes for those
technical boundaries; a BYO hardening pass does not prove the platform provider
path is safe.

### Ownership and API-group collisions

An organization provider may self-host a platform provider under the same name;
that is the intended override. A differently named organization provider may
not take over a group already served by a visible provider. Bootstrap validates
every place a group is declared: `spec.export.resources[].apiVersion`, every
supplied `APIResourceSchema.spec.group`, every generated
`APIExport.spec.resources[].group`, and the private CRD groups. The registry
guard deliberately ignores a provider with the same name, so an organization
`edges` copy may serve `edges.railgrid.ai` while an organization `quickstart`
copy may not. The bootstrap test now rewrites the entire quickstart export to
`edges.railgrid.ai` under the name `edges`, so it proves that path for the whole
group rather than only renaming the catalog card.

That override is a group-level replacement, not a per-resource merge. In the
organization's visible catalog the same-name copy becomes the provider for the
group, and an identity it mints can reach every resource its own export serves
in that group, including objects created while the platform copy was in use.
This is the intended shape of a self-hosted commercial replacement, but it is
also why same-name self-hosting must be treated as taking over that API for the
organization rather than as adding a second implementation beside it.

The cutover is not atomic. An APIBinding's export reference is not rewritten in
place, and Enable uses the provider name as the binding name. If the platform
copy is already enabled, Enable on the same-name organization copy now returns
`409 Conflict` instead of updating claims on the platform binding and claiming
success. The operator must Disable the platform provider, wait for its binding
and dependent content to finish deleting, and then Enable the organization
copy. That necessarily creates a service gap and may require dependent workloads
to be disabled first. A future atomic rebind would need an explicit kcp
reference-migration path or distinct binding names; the current safe behaviour
is refusal, not a transparent switch.

Because the organization copy has a different export path, moving a dependency
from the platform to a self-hosted copy also changes the exact source a
dependent's identity policy requires. Re-Enabling only the dependent is not
enough: the dependency's own binding must be Disabled and Enabled again so it
references the organization export, or an operator must reconcile that binding.
Until then, the dependent's foreign reads and verbs are refused with
`provider_not_bound`. The dependent's composition Grant is keyed by the
dependency's group/resource rather than its export path, so it can remain valid;
re-Enable the dependent only if its claims were also lost or rejected. Bootstrap
refuses a requirement whose provider is not yet registered, so an
all-self-hosted commercial stack must be installed in dependency order, with
the replacement gap planned rather than hidden.

**Residual: ownership is checked when a declaration is admitted and when an
identity is minted, not continuously over the installed APIExport.** If an
organization provider is admitted while the competing provider is not yet
visible in the registry (catalog startup, `APIGroups` not read yet, or a
platform provider installed later), its export, schemas, CRDs and eventual
binding remain installed. The identity policy rechecks ownership and refuses a
foreign own-group rule, so this is not a direct grant of the foreign group's
data. It is still an installed export that can collide with the real provider's
group: a tenant can bind it, and kcp can report a resource conflict or expose a
schema from the wrong provider. Follow-up: reconcile ownership over existing
organization declarations whenever the registry changes, mark a conflicting
provider not-ready and refuse Enable, and reject a platform registration that
collides with a differently named organization provider in any organization.
Do not treat the admission check as a permanent ownership claim.

### What a malicious BYO provider still controls

Self-hosting does not make vendor code safe. Once a tenant enables the provider,
the provider author controls the backend and the sandboxed UI, and both can
exercise every API claim the tenant accepted, including dependency reads and
selected Secrets. It can exfiltrate those bytes, retain scoped identities until
the sweep or token expiry, consume the tenant's quota (native quota admission is
bypassed in this local cluster), replay a captured relay or UI grant, and race a
consent change for data already in flight. The isolation claims here describe
what it cannot reach without consent; they do not make the vendor trustworthy.

Conversely, the self-hosted path does not give the provider the caller's hub
bearer or a KCP ServiceAccount token at the backend, write access to its own
CatalogEntry, APIExport, CRDs, RBAC or bindings, identity issuance outside its
own organization and exact target workspace, a core Secret capability beyond a
name-scoped GET selected by an accepted label selector, or an unauthenticated
heartbeat. Platform providers are still operator-reviewed code; a self-hosted
copy is not. The tenant administrator's Enable and claim consent are the
data-access decision in both cases.

### Sandbox request grammar

- **The sandbox parser treats any leading `namespaces` segment as namespace
  scope.** `parseProviderSandboxRoute` decides the resource/name split from the
  path shape alone, without API discovery. A core cluster-scoped request such
  as `GET /api/v1/namespaces/default` is therefore rejected as an incomplete
  namespace-qualified path. A custom cluster-scoped resource named
  `namespaces` is similarly ambiguous, and a cluster-scoped subresource under a
  resource called `namespaces` can be interpreted as a namespace-qualified
  resource. No privilege escalation was demonstrated; native KCP authorization
  and the sandbox claim checks still apply. Follow-up: resolve the selected
  API group/version/resource through discovery before classifying the path, and
  add regression cases for core `namespaces`, custom cluster-scoped
  `namespaces`, and ordinary namespaced resources.

### Provider UI execution

- **Grant issuance never returns an unpinned organization bundle.** With no pin
  for the running provider version, the grant request fails closed with `503`.
  With an upstream ETag, every grant revalidates conditionally: `304` keeps the
  pin, `200` re-pins. A transient revalidation failure keeps the cached pin for
  the same version; if the upstream bytes changed, the browser's SRI check
  refuses them, which is a safe denial. Without an ETag the pin is reused for at
  most ten minutes. Retaining a cached pin is not the same as issuing an
  unverified grant; it is a refusal to downgrade to an unpinned load.
- **SRI covers only `/main.js`.** Dynamic imports, chunks, stylesheets and
  other assets are not individually pinned. A provider can change another asset
  after `main.js` is pinned and still have the browser execute that asset inside
  the opaque-origin frame. The frame limits direct access to the portal document
  and host storage, but the asset runs with the bridge capabilities the frame
  already has. A stale or malicious ETag can cause a safe denial (the pin no
  longer matches), not execution of unverified bytes. Without an upstream ETag,
  the cached pin is reused for up to ten minutes before revalidation. Follow-up:
  ship an explicit asset manifest with per-file SRI pins, or serve one
  integrity-pinned bundle that imports no sibling code from the provider origin.
- **The UI asset grammar is heuristic.** `isAssetPath` only asks whether the
  final path segment contains a dot. Reserved hub-only prefixes are now rejected
  before a grant is redeemed, but any other dotted route on the provider UI
  origin remains grant-reachable. A provider that exposes a state-changing
  dotted endpoint on its UI listener could therefore be invoked through what
  looks like an asset request. Follow-up: replace the heuristic with an explicit
  asset manifest or a strict extension allowlist and path grammar.
- **UI grants are replayable.** Grants are sealed with AES-GCM, HKDF-derived,
  short-lived (five minutes), and bound to user, organization, workspace and
  provider, but they are not single-use and there is no redemption cache. Anyone
  who obtains the URL can redeem it for the same named provider, organization,
  workspace and user until expiry. Multiple asset fetches intentionally reuse
  one grant. The URL also appears in browser history and hub access logs.
  Follow-up: redact the grant from access logs and add a bounded, single-use
  redemption record keyed by the sealed grant id if the asset protocol can be
  made one request per grant.

### Backend relay

- **Relay tokens are replayable and not retractable.** `rgbyo1` tokens are
  HMAC-signed, scoped, and expire after ten minutes, but a captured token can be
  replayed through the hub broker within that window. The broker does not bind
  the token to one request body, method or channel. Live user, membership,
  provider, source, export, binding and cluster checks run on every use, and a
  later consent change blocks the next call; bytes already returned to an
  authorized caller cannot be retracted. Follow-up: add a request-bound nonce or
  one-time token channel and keep the short TTL.
- **Consent and capability checks have a time-of-check/time-of-use gap.** A
  request that was authorized before consent, membership or a composition Grant
  changed can finish and deliver its response. Streaming rechecks authorization
  before later flushes, but data already sent stays valid. Follow-up: define
  whether revocation must terminate in-flight streams, and implement cancellation
  and audit markers if it must.
- **Core Secret access is deliberately narrower than the general claim
  grammar.** The organization UI bridge permits only
  `GET /clusters/<tenant>/api/v1/namespaces/default/secrets/<name>`: no list,
  watch, `status`/`scale`, subresource, mutation or non-`default` namespace.
  The accepted requirement must carry a non-empty selector, and the response is
  fully buffered (32 MiB cap) and released only after its labels match the
  current selector. The provider-declared selector breadth is a tenant consent
  decision, not a hub-enforced sensitivity bound: a broad label selector can
  cover many Secrets. The scoped-identity service is stricter and never mints
  the core group at all. Follow-up: bound selector keys and values, show the
  matching Secret count at Enable, and consider making Secret access an
  explicit per-resource grant rather than a label selector.

### Sandbox request path grammar

- **A leading `namespaces` segment is always interpreted as namespace scope.**
  The sandbox parser treats `namespaces/<value>` as the namespace qualifier for
  every API group and version. It therefore rejects core cluster-scoped
  operations on the `namespaces` resource itself, such as
  `/api/v1/namespaces/<name>`, and makes a custom cluster-scoped resource named
  `namespaces` ambiguous with a namespace-qualified path. The request fails
  closed or is classified as a different resource path; no privilege escalation
  was demonstrated, because the original kube path still reaches KCP and the
  caller's native RBAC remains authoritative. Follow-up: use the selected API
  group, version and resource metadata to distinguish a cluster-scoped
  `namespaces` resource from namespace-qualified paths, and add parser
  regression tests before changing the grammar.

### Dependency freshness and diagnostics

An organization identity request re-resolves the requester's live CatalogEntry
`export` and `requires` (exact deep equality with the registry record), the
owning organization workspace and bootstrap policy, the target team workspace,
LogicalCluster annotations, the exact export binding path and name, accepted and
applied claims, and an uncached composition Grant. Own-group resources are
checked against the live APIExport surface, not only its group list. Each
dependency must be visible in the organization, serve the claimed group, have
the requested resource in its live APIExport, be exactly bound, have an
accepted/applied claim, and have current Grant consent.

- **A platform dependency is not deep-compared to its CatalogEntry.** For a
  platform dependency, `verifyLiveExport` compares the live APIExport's groups
  to the registry's `APIGroups` and checks that the requested resource is
  present; it does not compare every resource, verb and action to that
  provider's current `CatalogEntry.spec.export`. Clause C's declared verbs come
  from the registry CatalogEntry. The consequences are fail-closed or
  dead-grant, not a foreign write: a verb the CatalogEntry still declares but
  the live export no longer serves produces a capability that cannot be
  exercised; a resource the CatalogEntry removed but the live export still
  serves can still be read by name when the requester still declares it and the
  tenant accepted the claim; and if the live export later re-adds a verb the
  CatalogEntry never removed, a long-lived identity can use it without a new
  Enable. Follow-up: compare the platform provider's CatalogEntry resources to
  its live APIExport and refuse the dependency on drift, rather than proving
  only group and resource presence.
- **A transient dependency read is indistinguishable from a policy denial.** A
  failure reading a dependency's source, export, binding or consent is
  `continue`d in the dependency loop. The caller receives the same generic
  "current declaration and accepted dependency grant do not authorize this
  resource" refusal as a real policy decision. That is fail-closed, but it hides
  an availability failure and gives the provider no retry signal. Follow-up:
  return a distinct retryable `dependency_authority_unavailable` error while
  keeping the policy refusal for an actual denial, and log which dependency read
  failed without exposing tenant object contents.

### Identity lifecycle

- **Disable is not revocation.** Disabling or deleting a provider stops new
  identity issuance, but the existing `ScopedIdentity` record, ServiceAccount
  and RBAC stay until the owner object disappears, the sweep collects it, or the
  provider still has a valid credential to call the now-scoped `DELETE`. A
  provider deleted before it revokes its identities may have no way to
  authenticate that final call. Follow-up: add an operator-driven identity
  cleanup keyed by organization provider name that does not require the
  provider's credential, and document the maximum token lifetime after disable.
- **Disable and consent loss are not immediate, and legacy organization records
  remain outside organization list/delete semantics.** For a labelled
  organization record the two-minute sweep re-resolves the live provider
  source, exact target workspace and binding, dependency declarations and
  uncached composition consent before the owner/token fast path, and revokes on
  a definitive policy loss. The cleanup window is still bounded by the sweep
  interval, and an already-issued token can remain usable for its remaining
  lifetime until the provider calls the scoped `DELETE`. A provider deleted
  before it revokes its identities may have no way to authenticate that final
  call. Records written before the organization label and owner UID existed are
  invisible to organization list/delete and can only be collected when the
  owner probe finds the owner gone; if the same owner object survives, they
  remain materialized. Follow-up: add a migration that labels or refuses legacy
  organization records, expose an operator cleanup keyed by organization
  provider name, and document the maximum credential lifetime after disable or
  consent loss.

### Host-side defaults weaker than BYO

- **Platform heartbeats still accept unverified identities in `warn` mode.**
  The default for this release logs a failed TokenReview and records the beat;
  organization-owned beats are rejected even in that mode. A caller able to
  reach the hub can therefore keep a platform provider's liveness state fresh
  without its service-account token. Follow-up: make `enforce` the default and
  require deployments to opt into `warn` explicitly during migration.
- **Platform backends receive the caller's bearer by default.** With
  `--provider-delegated-tokens=off`, a platform provider can use that bearer for
  the caller's other hub capabilities, not just the provider API. Organization
  BYO backends receive neither the user bearer nor a KCP ServiceAccount token.
  Follow-up: default to `platform` delegation, finish the provider exclusions,
  and then remove the bearer-forwarding path.
- **Platform provider workspaces remain cluster-administrators by default.**
  `--provider-workspace-cluster-admin=true` gives operator-installed providers
  more authority than the restricted organization provider workspace. This is
  host trust, not an organization boundary, and must not be used as a model for
  BYO. Follow-up: move providers to the narrow role as their CRD seeding is
  removed, then flip the default.
- **Platform hub capabilities default to allowed.** A platform provider's
  declared `spec.hub.access` is usable before a tenant records acceptance;
  organization providers require an explicit decision. Follow-up: require the
  same consent record for platform providers, with a migration that records the
  current decision rather than silently preserving it.

### Quota and claim authority

- **Native quota admission is off in this cluster.** The local switch disables
  KCP `ResourceQuota` and `KCPKubeResourceQuota` admission, quota probes, and
  consumer inventory and coverage checks. `MaxLiveBytes` remains an admitted
  object-size bound, not an etcd capacity guarantee. Object-size and request
  guards, authorization, isolation and consent stay active. The source-controlled
  hosted gate remains closed; do not treat this cluster's behavior as evidence
  that quota accounting is safe.
- **RELEASE BLOCKER: the permission-claim policy admits installation-wide and
  ambiguous subjects.** The local policy includes the highly privileged
  `kcp-admin` User in `spec.providers` for bootstrap convenience. That User is
  not provider-specific: anything that can authenticate as `kcp-admin` can
  export a reserved group. The plain
  `system:serviceaccount:default:provider` subject is also
  logical-cluster-scoped in kcp, so it can match a tenant-created
  ServiceAccount with the same name in another workspace. kcp's current CRD
  accepts only static User/Group names and cannot express the runtime-qualified
  `system:kcp:serviceaccount:{cluster}:...` form. Narrowing this needs an
  upstream exact-subject or claim-audience mechanism. Until that exists, the
  generated policy is not tenant-safe and hosted BYO enrollment must remain
  disabled even if every other local E2E check passes. This is a hosted release
  blocker, not merely a limitation of this Tilt cluster. A controlled canary
  confirmed the policy gap: an operator-granted same-named `default/provider`
  ServiceAccount created an APIExport in the reserved planner group. The same
  test also bounded reachability: an ordinary organization admin could create
  ClusterRole objects, but KCP refused to let them grant APIExport or
  APIResourceSchema create, and a direct reserved-group create as that admin was
  denied. The flaw therefore requires an existing over-broad RBAC grant or a
  compromised trusted identity; it is not, by itself, an ordinary-tenant
  escalation in this build. The hosted gate stays closed because a future
  provider grant or operator mistake would make the ambiguous subject
  exploitable.

### Local environment

- **Stale platform CatalogEntries can shadow the catalog health surface.** An
  older deployment recreates platform entries such as
  `root:railgrid:providers/{planner,factory,databricks}`. Organization records
  are separate and are not shadowed, but platform catalog health, pins and UI
  can fail while those entries linger. Cleanup is manual:
  `.kcp/byo-commercial/cleanup-platform.py` is dry-run by default and
  `--apply` removes the legacy entries. Follow-up: add a registry guard that
  refuses to recreate a platform entry over an existing organization-owned
  provider name. A residual heartbeat ambiguity remains: when a platform and an
  organization entry of the same name share one catalog workspace, heartbeat
  selection can choose either record from the untrusted JWT cluster hint. The
  same fallback selects the platform record when the token's cluster matches no
  observed entry, which is the transient stale-catalog state. The selected
  organization record is forced through TokenReview even in `warn` mode, so a
  platform token cannot move it. A selected platform record is not: in `warn`
  mode a failed TokenReview is logged and the beat is recorded, so an
  organization provider's token can refresh the platform record's liveness
  (not its identity or data). The reverse is blocked. This is a low-severity
  availability/integrity residual, not cross-tenant access. Follow-up: remove
  stale platform entries, make selection deterministic from the verified token
  workspace and platform identity cluster instead of a name-plus-cluster map
  scan, and make `enforce` the default.
  A live probe confirmed this in the current Tilt configuration: an invalid
  `Authorization: Bearer not-a-valid-token` received `200` for the global
  Databricks and Factory heartbeats, and the planner provider token received
  `200` for planner, Databricks and Factory. The catalog listed those targets
  as `scope: global`; no organization heartbeat record was moved. Treat a
  publicly reachable hub with `warn` mode as accepting unauthenticated
  platform liveness and reported-version updates.
- **Factory has an API-version skew.** Planner and databricks reached `READY`;
  Factory uses `github.com/kcp-dev/apimachinery/v2 v2.33.0` while this embedded
  KCP and the other providers use the newer mjudeikis fork. Its logs show 404s
  on `apibindings.apis.kcp.io` and leases. Factory should be treated as blocked
  on dependency alignment, not as a passing third provider. Align its `go.mod`
  in a separate change and repeat the E2E.
- **Platform bundles can still load unpinned.** The organization bundle path
  now fails closed, but the platform bundle path retains its legacy unpinned
  fallback for compatibility. That is a platform-host trust decision, not an
  organization boundary, but it should not be mistaken for uniform SRI coverage.

### Resolved in this pass

- **Delegated hub-access routes are bound to the proof's tenant.** A
  `rgbyo1` token proved `org-a/team-a`, but the hub-access gate evaluated the
  provider grant without comparing that proof to the REST path. The tenant
  middleware then authorized the target path from `X-Railgrid-Org` /
  `X-Railgrid-Workspace`. A provider could therefore call
  `/api/orgs/org-b/memberships` with a token minted for `org-a/team-a`; if the
  represented user was also a member (or admin) of `org-b`, the provider could
  read that roster or invite into it using the user's authority. The same was
  possible for a different workspace in the same organization. `routeBindsCaller`
  now requires the path organization and, for workspace-scoped routes, the
  exact workspace to equal the signed proof. The regression test
  `TestOrgBackendTokenCannotCrossTenantPath` failed before the fix and now
  covers both mismatches.
- **Organization identity deletion is scoped and available again.** Org
  `DELETE /api/identities/{name}` now looks up the record by its
  hub-written organization label and provider, then deletes the ServiceAccount,
  ClusterRole, binding and record. It does not fall back to the platform release
  path, does not require the provider source to remain live, and does not let one
  organization or provider probe another's records. Tests cover wrong provider,
  wrong organization, platform-vs-org separation, account teardown and the
  service-missing fail-closed case.
- **Organization owner release is scoped instead of silently succeeding.**
  `ReleaseForOrg` deletes every identity for one owner in exactly one
  organization, target workspace, provider and UID. The platform `Release` path
  now refuses an organization-scoped owner with `CodeInvalidRequest` rather
  than listing only platform records and returning success without deleting
  anything. Tests cover the scoped release and the refusal.
- **Organization identities now bind to an owner UID.** `owner.uid` is required
  for organization-scoped issuance. The sweep can therefore distinguish a
  deleted organization owner from a later same-name platform object even when
  both providers export the same GVR. Platform identities retain their legacy
  optional-UID behavior.
- **Organization identity authorization is bounded by the live APIExport
  surface.** Clause A no longer treats "the provider exports this group" as
  sufficient. The organization catalog requires every requested resource to be
  present in the provider's current APIExport, synthesizes the ordinary
  `<resource>/status` subresource, and refuses anything else with
  `resource_not_exported`. Dependency requirements are also checked against the
  live dependency export surface before applied claims and composition consent
  are considered. A long-lived identity therefore cannot silently acquire a
  resource added to the same group after issuance.
- **The identity sweep rechecks live organization authority before the
  owner/token fast path.** Labelled organization records re-resolve the current
  provider source, exact target workspace and binding, dependency surface and
  uncached composition Grant on every sweep. A definitive policy refusal
  deletes the record and its RBAC; a transient read failure leaves the record
  alone rather than revoking on a kcp hiccup. Tests cover revocation after
  source, binding and consent loss.
- **Heartbeat authentication resolves the exact organization identity
  workspace.** When a platform and organization provider share a catalog
  workspace, the authenticator now uses the selected organization record's
  runtime workspace instead of a same-name platform record's workspace. A
  platform provider token can no longer move an organization record's liveness
  state. An unresolved organization runtime workspace is a retryable `503`; it
  never falls back to the catalog workspace or a same-name platform provider.
  The remaining overlap is availability ambiguity during catalog cleanup.
- **Hub-only backend routes were reachable through the UI front door.** The UI
  proxy did not reserve `workload-identities`, so an asset-shaped path such as
  `/ui/providers/<name>/workload-identities/review.json` could reach a
  provider's hub-only endpoint. `NewUIProxy` now denies hub-only prefixes, the
  joined path is checked during grant redemption, and grant issuance refuses a
  provider whose UI URL itself resolves under a reserved prefix. Tests cover
  direct, cleaned, case-varied, and encoded dot-segment variants.
- **A UI URL with its own path prefix cannot be used to escape back to a
  hub-only backend route.** The reservation check runs on the request-relative
  path before the proxy joins `spec.serving.ui.url`'s prefix. A regression test
  serves a provider whose UI URL ends in `/ui` and requests
  `/ui/providers/<name>/../workload-identities/review.json`; it must return
  `404` without an upstream call.
- **Same-name self-hosting is tested for full API-group ownership, not just a
  renamed catalog card.** The bootstrap test now rewrites the CatalogEntry
  export, the supplied schemas, the APIExport resources and a private CRD to
  `edges.railgrid.ai` under the provider name `edges`. It is admitted even
  though a platform `edges` provider already serves that group; a differently
  named provider is still refused in each of those four places.
- **A same-name self-hosted Enable can no longer report success against the
  platform binding.** Existing bindings are verified by export path and name
  before their permission claims are updated. A mismatch returns `409` and
  leaves the old binding's claims untouched; the operator must Disable and
  re-Enable. A regression test covers the refusal and verifies the old claims
  were not mutated.
- **The binding-source conflict is now tested at the REST boundary.** A
  socket-free handler test injects a wrapped
  `kcp.ErrProviderBindingSourceConflict` and verifies `409 Conflict` rather
  than `500`, plus an ordinary bootstrapper error mapping to `500`. This closes
  the gap between the bootstrap regression test and the HTTP contract the
  portal consumes.

### Prioritized follow-ups

| Priority | Finding | Next step |
| --- | --- | --- |
| P0 | Permission-claim policy admits `kcp-admin` and the ambiguous plain provider ServiceAccount | Add an exact-subject or claim-audience mechanism upstream; keep hosted BYO enrollment closed until then |
| P1 | Ownership is not rechecked over installed organization exports | Reconcile ownership when the registry changes; mark conflicts not-ready and refuse Enable |
| P1 | Platform dependency export is not deep-compared to its CatalogEntry | Compare resources, verbs and actions; refuse the dependency on drift |
| P1 | Legacy organization identities stay outside org list/delete semantics | Add a label/migration and an operator cleanup keyed by organization provider |
| P1 | Consent loss does not terminate in-flight responses | Define the revocation contract; cancel streams and emit audit markers if required |
| P2 | Mixed-catalog heartbeat selection is ambiguous | Select from the verified token workspace and platform identity cluster; remove stale platform entries |
| P2 | Platform `warn` heartbeat accepts invalid bearers | Make `enforce` the default and keep an explicit compatibility exception, with a regression test for an anonymous invalid-token beat |
| P2 | UI assets other than `/main.js` are unpinned and the asset grammar is heuristic | Ship a per-file asset manifest or one integrity-pinned bundle |
| P2 | UI grants and relay tokens are replayable | Add single-use or request-bound redemption while keeping the short TTL |
| P2 | Secret selectors have no breadth bound | Limit selector keys/values and show the matched Secret count at Enable |
| P2 | Transient dependency reads look like policy denials | Return a distinct retryable authority-unavailable error |
| P2 | Same-name platform-to-self-hosted cutover is not atomic | Add a reference-migration path or distinct binding naming; plan a maintenance gap until then |
| P2 | The local quota bypass is process-wide, not BYO-scoped | Isolate it to a disposable test process/cluster or one explicit test organization, and never reuse this hub for normal traffic |
| P2 | Native quota admission is bypassed in this cluster | Pass the upstream quota diagnostic against an unmodified dependency before hosted enrollment |
| P3 | Sandbox paths treat every leading `namespaces` segment as namespace scope | Resolve resource scope from API metadata and add core-namespace plus custom-cluster-resource regression tests |

### Bottom line

The self-hosted BYO path is materially stronger than the platform path at
caller credentials, browser isolation, heartbeat verification, provider
workspace privilege and live identity authorization. That is a genuine
improvement in those boundaries, not a claim that arbitrary provider code is
safe. A self-hosted backend and UI are still attacker-controlled code able to
exercise every API the tenant enabled, and the replay, TOCTOU, UI-asset, quota
and claim-policy findings above remain open. Ownership is also only an
admission-time and mint-time check, not a guarantee that a conflicting export
was never installed, and platform dependency declarations can drift from the
live export. The mixed-catalog heartbeat ambiguity is one-directional and
low-severity, but it should not be mistaken for exact provider identity. Most
importantly, the ambiguous permission-claim subjects remain a hosted release
blocker. A passing local Tilt E2E would prove the organization-scoped plumbing
and isolation checks, not hosted readiness and not platform-provider safety.
