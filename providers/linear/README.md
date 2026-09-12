# Linear provider

A standalone Faros provider for explicit Linear issue operations and verified
issue/comment notifications. Linear remains the authority for issue content.
This provider has no dependency on Gru, provider-agents, or private company data.

The initial implementation uses personal API keys. OAuth, issue deletion,
project/cycle administration, and continuous bidirectional mirroring are outside
its scope. This is a local experimental contribution; live Linear acceptance
requires credentials and is tracked in [stage-4-status.md](stage-4-status.md).

## APIs and authorization

The `linear.providers.faros.sh` APIExport serves three namespaced resources:

| Resource | Purpose |
| --- | --- |
| `Connection` | API-key Secret reference, optional allowed team UUIDs, optional webhook subscription identity and separate signing Secret reference. |
| `Operation` | Immutable, explicit read/write intent with a durable result or uncertain outcome in status. |
| `Event` | Deduplicated notification, with connection UID and retention deadline. |

A Connection can only reference Secrets in its own namespace. The export claims
`get` on Secrets; no Secret listing or credential values are exposed in results.
Creating Connections is an administrative capability: it selects credentials and
team policy. Operation authors may use any Connection they can reference in their
namespace. Separate namespaces or RBAC roles when users need different access.
Consumers should receive read-only access to Events and Operation status; only
the provider should author Events and status. See [consumer-rbac.yaml](examples/consumer-rbac.yaml).

The controller uses its provider ServiceAccount and the discovered APIExport
virtual workspace. It does not contact another provider's backend. The portal
uses host-owned fetch to create/read KRM resources as the caller. MCP tools
`linear_submit_operation` and `linear_get_operation` are adapters over the same
KRM command path at `/mcp`; they require tenant bearer authentication and
`X-Faros-Cluster`. No privileged MCP mutation path exists.

## Use

1. Install and initialize the provider using the [chart](deploy/chart/README.md)
   or the local Make targets. Enable its export in a Faros workspace and accept
   its Secret read claim.
2. Create a Secret named `linear-api` with an `apiKey` entry in the chosen
   namespace, using your credential manager. Never put actual keys in Git.
3. Apply [connection.yaml](examples/connection.yaml), replacing the allowed team
   UUID. Use the portal's **Discover teams** command to find UUIDs accessible to
   the key; an empty team list allows every team the key can access.
4. Submit an Operation through the portal, MCP, or Kubernetes API. Inspect
   `status.phase` and `status.result`; creation alone does not prove completion.

Supported actions: `teams`, `states`, `issues`, `issue`, `comments`, `createIssue`,
`updateIssue`, `addComment`, and `reconcile`. `issues` searches titles using
`query`; list operations return `nodes` and `pageInfo`. Pass `after` from the
previous page into a new read Operation; page size is at most 50.

`createIssue` requires `teamID` and `title`. Existing-issue actions require
`issueID`; the provider resolves its actual team before applying connection policy.
`updateIssue` accepts explicit `title`, `description`, and `stateID`. Omitted
fields are preserved; the API accepts an empty description to clear it. A state
must belong to the issue's team. `addComment` requires `body`. Field names and
bounds are authoritative in [types.go](apis/v1alpha1/types.go).

```yaml
apiVersion: linear.providers.faros.sh/v1alpha1
kind: Operation
metadata:
  name: inspect-issue-001
  namespace: default
spec:
  connection: linear
  action: issue
  issueID: YOUR-123
```

## Uncertain writes and recovery

Operation status is persisted before dispatch with optimistic concurrency.
Only one controller can claim the initial operation. A timeout, partial GraphQL
error, malformed response, or crash after this fence leaves a write `Uncertain`.
It is never blindly retried, including after provider restart. HTTP 200 is not
assumed successful when GraphQL errors exist. Rate-limited reads can be submitted
again later; automatic write retries are disabled.

Inspect the original Operation and query Linear before deciding what happened.
A new Operation is a new authorization, not an idempotent retry. For an uncertain
create/comment, find the resulting issue/comment in Linear using independent
read operations. For an update, compare its current values with the intended
fields. Once an administrator has reconciled the outcome, they may record the
resolution on the existing operation status and retain that audit record.
There is no automatic semantic matching or exactly-once guarantee for Linear
side effects. In-flight/uncertain records have a finalizer to prevent ordinary
deletion from removing recovery evidence; do not remove it to retry a write.

## Webhooks and catch-up

Configure a Linear webhook for Issue and Comment resources, restricted to the
appropriate teams. Supply its ID, organization UUID and a separate signing
Secret reference on the Connection. The receiver is
`POST /webhooks/<logical-cluster>/<namespace>/<connection>` on this provider.
Expose **only that path** through your HTTPS ingress. The authenticated hub
provider proxy is not an anonymous webhook ingress; do not disable hub auth.

The receiver verifies HMAC-SHA256 over the raw body, signed payload timestamp
within one minute, subscription identity and team policy. Comment teams are
resolved through their issue. It acknowledges only after persisting a notification.
The deduplication key hashes signed content excluding the retry timestamp, scoped
to the Connection UID; changing an unsigned delivery header cannot bypass it.
Events contain identifiers, not copies of private issue/comment bodies.

Events are retained for seven days, with admission backpressure at 1,000 records
per namespace. Single-replica deployment is required. Watch/list the Event API
using Kubernetes resourceVersion/continue cursors. Resume watches from your last
resourceVersion, and relist after a `410 Gone`; retention is not an infinite log.
Repeated watch/list reads permit replay during retention. Expired notifications
are pruned; operation history is retained until an operator deletes terminal
records. Configure workspace resource quotas for additional storage limits.

After missed events, submit `reconcile` with a team UUID and a `since` RFC3339
value, then follow every returned page. This reads issues updated since that
point; it does not manufacture webhook events or prove all deleted entities were
recovered. Refresh comments explicitly for relevant issues. Removing an issue or
an inaccessible comment may require operator reconciliation. Automatic gap
reconciliation and webhook registration are not implemented in this increment.

## Development

From the Faros repository root:

```sh
make verify-linear-provider
make install-provider-linear
make init-provider-linear
make run-provider-linear
```

The dev runtime listens on port 8092. Tilt includes opt-in manual `linear-register`,
`linear-init`, and `linear` resources; stop any manually started provider before
starting Tilt's copy to avoid a port collision. The standard provider release workflow
supports `providers/linear/v*` tags; no release, push, or source mirror is created
by these local steps. The module uses the in-tree provider SDK replacement like
other Faros providers; the repository no longer requires a `go.work` file.

API behavior references: [Linear GraphQL](https://linear.app/developers/graphql)
and [webhook verification](https://linear.app/developers/webhooks).
