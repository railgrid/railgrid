# Linear provider

Linear is a standalone provider under `providers/linear`. It exposes cluster-scoped
Connection, Team, Operation and Event APIs within each tenant workspace through `linear.providers.faros.sh`.
Human and machine callers author the same immutable Operation resources; the
provider reconciles them against Linear and stores observed outcomes in status.
Uncertain writes remain explicit and are never automatically replayed.

The Operation API supports the read action `replies`. It takes an `issueID` and
`commentID`, validates that the parent comment belongs to the requested issue
and an allowed team, and returns the existing bounded comment `nodes` and
`pageInfo` shape. The implementation reads Linear's native `Comment.children`
connection, with stable comment IDs, parent IDs, issue IDs, author metadata,
and timestamps. It does not treat an unfiltered issue comment list as a
threaded-reply fallback. Read pagination remains explicit through `first` and
`after`; operation results are durable and uncertain writes stay
operator-reconcilable.

The provider discovers tenant APIs through its APIExportEndpointSlice. It reads
referenced Secrets through its declared Secret-get claim (each reference carries
a namespace defaulting to `default`), with Connection team
policy applied to issue operations and webhook events. Portal and MCP command
submission use caller credentials against the tenant API. Bootstrap and runtime
identities are separate in the Helm deployment. No Gru implementation or private
company configuration belongs in this provider.

See the [provider contract and usage](../providers/linear/README.md),
[typed API](../providers/linear/apis/v1alpha1/types.go),
[chart reference](../providers/linear/deploy/chart/README.md), and
[current acceptance evidence](../providers/linear/stage-4-status.md).

The tenant workspace is the access boundary: callers permitted to create
Operations can invoke any Connection in that workspace. Connection configuration
remains administrative; separate workspaces provide separate credential and
team-policy authority. Webhooks address `/webhooks/<cluster>/<connection>` and
event admission is bounded per tenant workspace. Namespaced resource routes and
schemas have been replaced without a compatibility layer.

Controller work is discovered through metadata-only, paginated version scans.
Each export endpoint has separate bounded pools for Connections, Teams, Operations, and
Events; each pool schedules one job per workspace per turn. Full terminal results
are fetched only on initial discovery or metadata changes, not every scan.
Event expiration and Connection probing are scheduled independently. Historical
record counts still determine metadata scan/cache cost; terminal history cleanup
remains operator-owned.

Webhook preparation authenticates and resolves team policy outside the
workspace admission section. Ingress has global/per-workspace concurrency bounds
and a four-second deadline; count/create admission is per workspace, with
contention returned for retry. HTTP 200 acknowledges persistence or an existing
deduplicated Event. The chart uses Recreate so local admission and dispatch
recovery do not overlap during a normal deployment upgrade.

The portal retains pending mutation identity and its Connection in workspace
session state before submission. Views can stop polling without forgetting the
intent, and resumed checks read that original Operation. Preparing a separate
write requires explicit acknowledgment; changing workspace/user clears UI state.


Teams register existing Linear teams within a Faros workspace and pin their
Connection name, Connection UID and upstream team ID in immutable spec fields.
The portal journey is Connections → Teams → Issues. Only current, nondeleting registrations for that
Connection identity authorize issue operations and webhook admission. Empty
registration sets deny all teams. Team readiness records observed metadata and
credential reachability; access uses current registration intent, not cached status.

Removing a Team removes
its Faros registration only and does not cancel previously dispatched work or
modify upstream teams/issues. Operations and Events retain their existing schemas
and user-facing pages.
