# Linear provider

Linear exposes `Connection` and `Team` resources in each Faros tenant workspace.
Issue reads and writes use Team-bound Provider Actions. Issues stay in Linear;
Faros does not export Operation or Event resources, or accept Linear webhooks.

## Connect and register Teams

Enable Linear, accept its `secrets/get` permission claim, and add a Connection
in the portal. The API-key form validates the key before saving it in an owned
workspace Secret. An existing Secret reference can also be supplied in a
Connection manifest; see `examples/connection.yaml`. Credential namespaces
are explicit and default to `default`.

Register the Teams the workspace should use. Each Team pins its Connection UID
and upstream team UUID. Removing the registration stops authorizing new actions;
it does not delete the team or issues in Linear or cancel an in-flight write.

Consumers need `get` on the registered Team and `invoke` on its action-specific
subresource, for example `teams/issues` or `teams/add_comment`. The caller's
Kubernetes permissions are checked before provider credentials are resolved.
Use `resourceNames` to restrict a role to particular registrations. The example
in `examples/consumer-rbac.yaml` separates read and write permissions.

Administrative Team discovery requires permission to create Team registrations
and read the Connection. Ordinary consumers use registered Teams only.

## Provider Actions

The authenticated, tenant-scoped action route is:

```
POST /services/providers/linear/actions/clusters/{cluster}/teams/{team}/{action}/v1
```

`team` is the Faros Team resource name. The route supplies Connection, upstream
team identity and action; callers cannot override those identities in the input.
CatalogEntry publishes each action's schemas, bounds and retry semantics.

| Action | Input |
| --- | --- |
| `states` | `first`, `after` |
| `issues` | `first`, `after`, `query`, `since` |
| `issue` | `issueID` |
| `comments` | `issueID`, `first`, `after` |
| `replies` | `issueID`, `commentID`, `first`, `after` |
| `create_issue` | `title`, optional `description`, `stateID` |
| `update_issue` | `issueID`, at least one of `title`, `description`, `stateID` |
| `add_comment` | `issueID`, `body` |

Requests contain `input`. For writes, send a stable `Idempotency-Key` header
(the Actions SDK `idempotencyKey` option), or the legacy body `requestId`.
If both are supplied they must match. For example:

```json
{"requestId":"20260913T120000Z.550e8400-e29b-41d4-a716-446655440000","input":{"issueID":"issue-uuid","body":"Ready for review."}}
```

Timestamped keys use the current UTC time when preparing the intent;
persist the complete key before dispatch. Never select a new key because a
response was lost. Timestamped keys expire after 30 days for new dispatch.
Opaque keys without a timestamp are also supported; their receipts do not expire
and count toward the same 2,000-receipt tenant limit. A repeated key with different
input or a replaced Team/Connection is rejected. `X-Request-ID` is only a response
correlation ID.

Responses use the shared Provider Action envelope: `requestID`, provider/action
identity, `resourceRef`, and exactly one of `result` or `error`. The outer `result`
contains a `phase` and, on success, the upstream data in its own `result` field. `Running` and `Uncertain` are not success. Inspect a write using GET on
the same action route with `?requestId=...`; inspection never dispatches it.
Inspection checks current authorization and resource identities but does not
require the credential Secret to remain available.
After uncertainty, compare the current issue/comments in Linear before preparing
another intent. The portal keeps this recovery control beside the issue form.

Reads return current bounded results without persistence. Pages contain `nodes`
and `pageInfo`; page size is at most 50. Follow `endCursor` while `hasNextPage`
is true. `since` filters issue observations, not historical webhook deliveries.
The `replies` action checks that its parent comment belongs to the requested
issue; every issue and state is checked against the bound Team.

## Persistence and identity

Only writes have private recovery records, in Linear's own provider workspace
under `linear.internal.faros.sh`. These schemas are not exported or included in
the CatalogEntry. Consumers cannot list or modify receipts. They inspect outcomes
through the authenticated action boundary.

The dispatch fence is persisted before mutation. Unknown outcomes are retained
without automatic replay. Confirmed outcomes are eligible for cleanup after
30 days when their request keys have also expired. Admission is capped at 2,000
retained writes per tenant; pending and uncertain records are not evicted to make
room. Read operations consume no receipt capacity.

Initialization installs the private CRD and a provider-local runtime RBAC grant
using the bootstrap identity. The runtime identity is the standard `provider`
ServiceAccount. The Deployment requires one replica and Recreate rollout.

Factory uses a dedicated tenant ServiceAccount and the hub action route. The hub
verifies its token audience and current ServiceAccount UID in the selected tenant.
Provider actions then check the caller's resource permissions. This access does
not authorize other provider HTTP endpoints or expose Linear credentials.

## MCP and development

The MCP endpoint publishes `linear_states`, `linear_issues`, `linear_issue`,
`linear_comments`, `linear_replies`, `linear_create_issue`, `linear_update_issue`,
`linear_add_comment`, and `linear_inspect_write`. Domain tools use the same action
implementation and caller authorization as HTTP.

From the Faros repository root:

```sh
make codegen-linear-provider
make build-linear-provider
make test-linear-provider
make test-linear-portal
make lint-linear-provider
```

See `deploy/chart/README.md` for hosting values. Historical stage records describe
older implementations and do not establish acceptance of this action-based flow.

Connection readiness checks also observe `status.workspaceSlug` from the authenticated Linear organization. Consumers can resolve workspace URLs from the Team’s pinned Connection without requesting credentials or asking users to copy a URL key. Failed probes clear the observed slug; consumers must check readiness and observed generation.
