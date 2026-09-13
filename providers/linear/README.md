# Linear provider

A standalone Faros provider for explicit Linear issue operations and verified
issue/comment notifications. Linear remains the authority for issue content.
This provider has no dependency on Gru, provider-agents, or private company data.

The initial implementation uses personal API keys. OAuth, issue deletion,
project/cycle administration, and continuous bidirectional mirroring are outside
its scope. This is a local experimental contribution; live Linear acceptance
requires credentials and is tracked in [stage-4-status.md](stage-4-status.md).

## APIs and authorization

The `linear.providers.faros.sh` APIExport serves four cluster-scoped resources within each tenant workspace:

| Resource | Purpose |
| --- | --- |
| `Connection` | API-key Secret reference, Team access mode, optional webhook identity and signing Secret reference. |
| `Team` | Existing Linear team registered in Faros, pinned to a Connection name and UID. |
| `Operation` | Immutable, explicit read/write intent with a durable result or uncertain outcome in status. |
| `Event` | Deduplicated notification, with connection UID and retention deadline. |

A Connection references Secrets in the same tenant workspace. Each Secret reference
contains a namespace, defaulting to `default`; Secrets remain namespaced. The export claims
`get` on Secrets; no Secret listing or credential values are exposed in results.
Creating Connections and registering Teams are administrative capabilities: they select credentials and team access. Registered Teams define access; an empty registration set denies all teams. Registrations are read at dispatch, excluding deleting Teams and mismatched Connection UIDs. Linear credential permissions remain the outer access bound. Permission to create Operations authorizes using any Connection in that workspace.
Connection read permissions are not a separate invocation gate. Use separate Faros
workspaces when groups need isolated credentials or Linear access.
Consumers should receive read-only access to Events and Operation status; only
the provider should author Events and status. See [consumer-rbac.yaml](examples/consumer-rbac.yaml). When upgrading an existing installation, reapply this role (or add `get`, `list`, and `watch` on `teams` to your equivalent consumer role) so consumers can use the Teams UI. Team creation and deletion remain administrative.

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
2. In **Connections → Add connection**, enter a name and personal API key. Check
   the key, then save. Faros creates the Connection and its owned credential Secret.
3. In **Teams → Add teams**, choose the Connection and select existing Linear teams
   by name or key. This creates Faros registrations, never upstream teams.
4. Open a Team to browse, create and update its issues. Operations remain the
   underlying command API; inspect status to distinguish completion from uncertainty.

For declarative setup, create the credential Secret using your credential manager,
then a Connection and a Team with `spec.connection`,
`spec.connectionUID` and `spec.teamID`. These Team identity fields are immutable.
Use a Connection owner reference for registration garbage collection. Never put
actual keys in Git. Removing a Team only removes Faros access; it does not delete
the team or issues in Linear. Already dispatched operations are not cancelled.

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
side effects. In-flight/uncertain writes have a finalizer to prevent ordinary
deletion from removing recovery evidence; do not remove it to retry a write. If deletion races the completion update,
recovery marks the retained write Uncertain instead of leaving it Running.

## Webhooks and catch-up

Configure a Linear webhook for Issue and Comment resources, restricted to the
appropriate teams. Supply its ID, organization UUID and a separate signing
Secret reference on the Connection. The receiver is
`POST /webhooks/<logical-cluster>/<connection>` on this provider.
Expose **only that path** through your HTTPS ingress. The authenticated hub
provider proxy is not an anonymous webhook ingress; do not disable hub auth.

The receiver bounds ingress to 16 concurrent requests, at most two per workspace,
and a four-second processing deadline. Authentication and Linear lookups run
outside event admission; only event lookup/count/create is serialized per
workspace. Busy requests return 503 for Linear retry; persisted and deduplicated
deliveries return HTTP 200.

The receiver verifies HMAC-SHA256 over the raw body, signed payload timestamp
within one minute, subscription identity and team policy. Comment teams are
resolved through their issue. It acknowledges only after persisting a notification.
The deduplication key hashes signed content excluding the retry timestamp, scoped
to the Connection UID; changing an unsigned delivery header cannot bypass it.
Events contain identifiers, not copies of private issue/comment bodies.

Events are retained for seven days, with admission backpressure at 1,000 records
per tenant workspace. Single-replica deployment is required; the chart uses `Recreate` to prevent overlapping processes during upgrades. Upgrades therefore briefly interrupt service. Watch/list the Event API
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

## Controller scheduling and history cost

The controller scans bounded pages of resource metadata every five seconds,
keyed by logical cluster and name. Only new or changed resource versions enqueue
full resource reads. Terminal Operation bodies are not fetched on each scan;
an initial startup or a changed record still requires a full read. Metadata
scan/cache cost remains proportional to the resource count, so operators should
continue to delete unneeded terminal history and configure workspace quotas.

Connections, Teams, Operations, and Events have separate worker pools (four workers per
resource per export endpoint). Each pool processes at most one job per workspace
at a time and rotates workspaces after each job. Jobs have a 45-second budget;
Connection and Team checks are scheduled every minute and Events at expiration. Slow
Linear calls do not hold up metadata discovery, other workspace workers, or the
separate expiration pool. Readiness reports access to the export resource APIs;
individual credential failures remain on Connection status. This is bounded
concurrency, not an upstream quota guarantee for credentials reused across workspaces.

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

## Brand asset

`portal/public/icon.svg` is the unmodified `linear-icon.svg` from [Linear's official brand assets](https://linear.app/brand), downloaded 2026-09-12. It identifies the Linear integration; Linear owns the mark.

## Portal resource pages

The Linear portal uses shared Vue PortalKit resource layouts with Connections,
Teams, Operations, and Events sections. Connection creation checks and saves the
credential; separate Team registration selects access. Team details contain the
issue list, search, pagination and Create issue action. Connection and Team lists
use the shared row deletion action and detail overflow menu with confirmation.

Collection routes are `connections`, `teams`, `operations`, and `events`.
Resource details use `<collection>/detail/<name>`. Creation routes include
`connections/create` and `teams/create`; issue routes remain `issues/create` and
`issues/detail/<connection>/<id>`. Legacy `issues` URLs remain available for
bookmarks and in-memory write recovery, but Issues is no longer a top-level tab.
Empty Connections and Teams use the shared first-run guide.

`GET /api/connections/<name>/teams` provides paginated administrative discovery.
It requires caller permission to create Teams and read the Connection before
resolving the credential through the provider. The visible and resolved Connection
UIDs must match. Discovery lists credential-accessible teams so administrators can add new registrations.
Discovery writes no resources, has bounded concurrency and disables caching.

`POST /api/onboarding/teams` checks the caller's permission to create Connections
through a caller-scoped SelfSubjectAccessReview before contacting Linear. Each
request retrieves at most 50 teams, has a 25-second deadline, and shares a
16-request admission limit. The submitted key is transient and responses disable
caching; discovery creates no Connection, Secret, or Operation. The portal saves
the Connection first, then creates its owned credential Secret in `default`, using
the caller's Kubernetes permissions. The provider's Secret-get claim is unchanged.
Partial saves link to the Connection and support retrying the same settings;
recovery never overwrites an existing Secret. The key is kept only in the creation
form's memory and cleared on success or exit. Reloading loses unsaved credentials;
inspect any partially created Connection before starting again.

Editing a search keeps the last applied results visible until **Search** is
submitted; **Clear search** returns to the first unfiltered page. Refreshing an
issue preserves unsaved field edits, with **Discard changes** restoring the
latest fetched values. Leaving the detail page discards those edits, as the
form explains. Comment progress and recovery appear beside the comment form.

Before submitting a write, the active workspace session retains its operation
name and original Connection. Navigating within the provider preserves that
recovery identity. Returning shows **Check outcome** and a link to the submitted
operation; ordinary submission stays disabled. Checking an outcome never posts a
new command. **Prepare a separate write** requires an explicit acknowledgment
that the user checked Linear and accepts the possibility of duplication. It does
not replay or delete the original Operation. This UI state is in memory and
clears when the provider session is destroyed or the workspace/user changes;
the KRM Operation remains the durable recovery record across browser reloads.

Build with `make build-linear-provider-portal` and verify behavior and types with
`make test-linear-portal`. The portal still registers `faros-provider-linear` and
ships the classic-script `main.js` bundle. Connections, Teams, Operations, and Events use workspace-wide API endpoints.
Credential namespace references remain supported by the API; portal setup uses `default`.

## Scope and breaking-change policy

The workspace is the user-facing and authorization scope. Resource names are
unique across that workspace. The experimental namespaced API is replaced
without a compatibility layer or data migration. Development installations
using the previous schemas must recreate their disposable bindings/resources;
never resubmit old Operation records as fresh commands. Credential Secrets
retain their namespace and are referenced explicitly by the new Connection.

## Factory integration in the portal

Team issue boards, issue lists, and issue details discover whether Factory is
bound to the current workspace before reading its published Task resources.
When Factory is not enabled, a dismissible banner links to the workspace's
provider catalog. Dismissal lasts for the browser session and is scoped to the
workspace and user. Enablement and permissions remain owned by the host catalog.

When enabled, the portal reads tasks in Factory's `default` namespace through
the caller-scoped Kubernetes proxy. Links match the immutable Linear issue UUID,
Connection name and UID, and team identity; a replaced Connection cannot inherit
old task links. Task status links open Factory, while issue links continue to
open Linear issue details. The detail section includes clarification, attempt
count, repository, and a validated GitHub implementation PR link when available.

The visible surface refreshes Factory state every 30 seconds and on explicit
refresh, and cancels reads when leaving the view or changing workspace. Failed
reads display an unavailable state rather than an enablement prompt. An empty
task list is shown as no linked work, not proof of missing intake configuration:
Factory does not currently publish an intake-configuration discovery API.
