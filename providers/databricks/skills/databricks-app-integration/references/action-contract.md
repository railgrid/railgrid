# `query_table/v1` contract and troubleshooting

This reference is for implementation and diagnosis after the project grant has
been discovered. It does not replace the authenticated catalog or grant. Keep
the project integration alias (used only by the SDK) separate from the exact
grant-bound Table resource `name`/`tableRef` (the exact `name` from the grant,
or from `list_tables` in a pre-grant assistant workflow). Never pass one in
place of the other.

Schema discovery is a hard prerequisite for application query and UI code:
prefer current `status.columns` from the exact grant-bound `Table`; otherwise
make one server-side `query_table/v1` discovery call with `columns` omitted and
`limit: 1`. The discovery request is `{ "limit": 1 }`; do not send
`"columns": []` and do not send any guessed column. Never invent or guess
columns, and never treat examples below as real names. Every angle-bracket value
is an explicit placeholder. After the grant is known, MCP `describe_table` and
MCP `query_table` are not an application schema-discovery path and cannot
replace the bound status or granted action probe. Allow at most one schema probe
per assistant turn; if it fails, times out, or returns a mismatch, stop and
report its structured failure. Continue only when a later turn has new
authoritative grant, binding-status, or schema evidence. Do not log row values
during discovery or normal querying.

Use `invokeEnvelope` for this probe so the server can verify the complete
envelope before reading `schemaProbe.result.columns`:

```js
const schemaProbe = await faros
  .integration('<BOUND_INTEGRATION_ALIAS>')
  .invokeEnvelope('query_table/v1', { limit: 1 });
// Verify schemaProbe.actionVersion and schemaProbe.resourceRef first.
const discoveredColumns = schemaProbe.result.columns;
// Never log schemaProbe.result.rows; row values are not schema evidence.
```

## Request shape

The server SDK sends the integration alias and action ID through the App Studio
gateway. The alias is only the SDK selector; the gateway resolves the project's
grant and injects the exact bound resource reference. The only caller-controlled
action input has this shape. The following is illustrative only;
`<COLUMN_NAME_FROM_SCHEMA>` is a placeholder that must be replaced only after
schema evidence:

```json
{
  "columns": ["<COLUMN_NAME_FROM_SCHEMA>"],
  "limit": 25
}
```

The provider-side resource identity is fixed to the exact grant-bound Table
`name` (the `tableRef` returned by `list_tables` in a pre-grant assistant
workflow):

```json
{
  "apiVersion": "databricks.faros.sh/v1alpha1",
  "kind": "Table",
  "resource": "tables",
  "name": "<GRANT_BOUND_TABLE_NAME>"
}
```

The action schema permits at most 64 column names and a limit from 1 to 100.
The provider applies the same bounds and rejects unknown input fields. The
action is synchronous, read-only, and idempotent for a fixed bound and input;
respect the published timeout and output limits rather than adding an
unbounded client retry loop.

## Response checks

Before rendering, check that the response has `actionVersion: "v1"`, the
server-owned `tableRef`, a bounded `columns` array, a bounded `rows` array, and
the `truncated` flag. Treat a mismatch between the expected grant-bound table
and the response identity as a failed verification, not as a reason to let the
caller choose another table. Keep `requestID` from the SDK envelope for
diagnosis and log metadata only; never log row values, including from a
discovery response.

## Failure classes

| Signal | Meaning | Safe next step |
| --- | --- | --- |
| `unauthenticated`, `tenant_required` | The gateway/provider did not receive the authenticated app context. | Check server token-file refresh and project/org/workspace configuration; never ask the browser for a provider token. |
| `grant_not_found`, `action_not_allowed`, `binding_revoked` | The project grant is absent, revoked, or does not expose `query_table/v1`. | Rediscover or repair the project grant through App Studio; do not bypass it with a direct provider request. |
| `invalid_request`, `schema_validation_failed` | Input is outside the published schema, including too many columns or an invalid limit. | Validate against the catalog schema and return a user-facing input error. |
| `schema_projection_invalid` | A requested column is not present in the exact bound `Table` schema. This is an HTTP 400, non-retryable failure with a safe rejected-column message (for example, `requested column "<REJECTED_COLUMN_FROM_ERROR>" is not present in the imported table schema`); the hub preserves the request ID and bound resource identity. | Rediscover the exact bound-Table schema, repair the projection to use only verified column names, and submit a corrected request. Do not retry unchanged columns, invent a replacement, or switch the bound table. |
| `resource_not_found`, `resource_forbidden`, `resource_not_ready` | The grant-bound `Table` cannot be resolved or the caller is not authorized for it. | Inspect binding status and provider resource readiness as the tenant user; do not accept a replacement table name. |
| `timeout`, `aborted`, `network_error` | The gateway or provider call did not complete. | Use the request deadline and one bounded retry only when the published idempotency policy permits it; preserve the request ID. |
| `action_failed`, `backend_failure` | The provider resolved the binding but its Databricks dependency failed. | Show a safe dependency error, inspect provider health/logs by request ID, and avoid exposing credentials or backend URLs. |

The SDK represents provider failures as `ProviderActionError` with a stable
`code`, safe `message`, `retryable` flag, request metadata, and binding
metadata. Transport/configuration failures are `ActionsClientError` with a
machine-readable code. Do not infer retryability from HTTP status alone.

## Verification ladder

1. **Endpoint:** make one bounded server call and inspect the complete envelope
   and structured error, without printing row contents.
2. **Runtime:** confirm the server process is healthy, the token file is
   readable by that process, and the preview route is reachable.
3. **Rendered state:** confirm the UI renders the returned columns/rows and
   truncation state rather than a hard-coded success placeholder.
4. **Interaction:** exercise one supported filter/page-size interaction and
   verify that the server revalidates the bounded input. A `Ready` condition or
   HTTP 200 only proves transport/runtime health, not these later rungs.

If a rung fails, stop at that rung, preserve the structured evidence, and fix
the owning boundary before moving on.
