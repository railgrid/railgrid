# Linear provider

Linear is a standalone provider under `providers/linear`. It exposes cluster-scoped
Connection, Operation and Event APIs within each tenant workspace through `linear.providers.faros.sh`.
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
