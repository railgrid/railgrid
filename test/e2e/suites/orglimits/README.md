Run `make test-byo-availability` from the repository root. It runs the guard
unit tests, then starts one isolated embedded kcp with the unmodified pinned
dependencies, random loopback ports and temporary data. No Docker, existing
cluster, deployed hub or external credentials are used. The integration test
skips unless `RAILGRID_E2E_ORG_LIMITS=true`; the Make target sets that opt-in.

The test provisions two organizations through native Workspaces and verifies
full-object CREATE/PATCH size enforcement, cleanup, actual APIExport virtual
workspace request-size enforcement, and a real organization-owned export's
wildcard watch cap. Four real watch streams stay open while eight further
wildcard-watch requests are rejected and the other organization writes under a
10-second test deadline. This is a bounded local regression workload, not a
production latency guarantee or a general denial-of-service benchmark. The
numeric limits are test fixtures, not production defaults. Server shutdown and
data cleanup run even on failure.

The suite marks one consumer LogicalCluster and installs a minimal native
ResourceQuota with initialized `count/configmaps` status. It creates the quota's
`admin` namespace after observing the marker, then verifies covered ConfigMap
writes, denial of an uncovered Secret family, and that an unmarked workspace
remains unaffected. An unmarked exporter child workspace under the same
organization owns the APIExport used for wildcard-watch tests. This focused
fixture does not prove full BYO enrollment safety or the complete consumer
baseline quota. See
[upstream KCP requirements](../../../../docs/upstream-kcp-requirements.md).

The guard starts after kcp authentication and authorization. It does not bound
pre-authentication work, trusted platform-wide requests outside organization
scope, or every source of shared control-plane load. It applies to all resolved
organization workspaces when explicitly enabled, including existing core and
platform-provider data. It relies on kcp's verified identity, impersonation and
authorization boundaries; caller headers do not choose an organization.

Limits are shared only inside one embedded process. Multiple replicas/shards
do not share counters and are not a supported enforcement topology. Restarting
the process resets traffic bursts and active-account allocations. Ordinary
requests and streams share one total concurrency cap; recognized streams also
have an additional cap. Classification uses kcp's parsed request verb and
subresource, including accepted forms such as `watch=TRUE`; an arbitrary Upgrade
header cannot move an ordinary write into the stream budget. Forwarding through
a second HTTP hop can charge the same organization again; no caller-controlled
header suppresses accounting.

`MaxOrganizations` (`--org-max-active-organizations`) bounds lazily allocated
in-memory limiter accounts. It does not count persisted organizations, certify
capacity for a new enrollment, or bound aggregate etcd storage. An existing
inventory larger than this setting can still pass guard readiness: the first
organizations to use the guard consume the slots, and requests needing another
account are rejected until an idle account has no requests or streams and its
token bucket has fully refilled. Reclaiming only a fully refilled bucket
preserves its rate and burst limits. Deployment sizing should still account for
the organization inventory; native object quotas and admission before new
authority remain separate requirements.

For a marked consumer workspace, the native write guard requires the exact
cluster-scoped consumer quota and an initialized matching hard/used counter.
During quota installation it permits only CREATE of the structural `admin`
namespace. With missing or over-limit coverage, an UPDATE is permitted only
when the complete serialized object becomes strictly smaller; equal-size and
growing writes still require quota coverage. This narrow cleanup path lets
finalizer removal proceed without exempting administrator identities.

The serialized-object cap also does not establish an initial storage baseline.
An already oversized object may receive a non-growing update (including a small
PATCH removing a finalizer), allowing cleanup; further growth is rejected.
Aggregate live objects, etcd encoding/metadata overhead, MVCC revisions,
compaction, defragmentation and operational headroom need their own enforced
capacity policy and validation. Passing this suite neither fixes the pinned
upstream quota issue nor makes hosted BYO enrollment ready.
