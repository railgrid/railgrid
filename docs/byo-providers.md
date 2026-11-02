# Bring your own provider

An organization administrator can register an authored provider, deploy it on
one of the organization's connected Kubernetes Edges, and enable it in selected
workspaces. A provider need not exist in the platform catalog. Self-hosting a
platform provider uses the same organization boundary and its published chart.

This describes the implementation in this checkout. It does not assert that a
hosted deployment has been upgraded or configured for this flow.

**Dependency release blocker:** the currently pinned kcp apiserver quota
accessor selects an annotated admin quota twice for cluster-scoped requests.
It can charge a failed creation repeatedly until the quota is full. A
source-controlled gate currently denies hosted BYO enrollment even when all
operator limits and Railgrid native guards are ready. The gate has no runtime
configuration or environment-variable bypass. Upstream requirements and the
explicit diagnostic are recorded in
[KCP upstream requirements](upstream-kcp-requirements.md). This checkout does
not patch KCP or substitute a modified dependency.

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
requires a separate operator audit. The hosted enrollment gate described above
remains unchanged.

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

Even with those limits ready, hosted BYO enrollment remains disabled until the
supported KCP quota-selection diagnostic passes. Only a source release update
may open this gate after validation against the unmodified supported dependency;
there is no Helm value, command-line option or environment override. The gate
blocks new registration, bootstrap, provider credential issuance/rotation, and
consumer Enable preparation. It does not revoke credentials or bindings that
already exist.

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
pass against a supported, unmodified dependency before the source-controlled
enrollment gate is opened.

The diagnostic intentionally fails on the affected pin; do not weaken the quota
or report full enrollment readiness from tests using a modified dependency.
Railgrid-owned admission and request guards use existing KCP extension points;
they do not modify KCP source or establish that the upstream quota defect is fixed.
