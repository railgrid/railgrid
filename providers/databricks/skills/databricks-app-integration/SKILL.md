---
name: databricks-app-integration
description: Use when building or reviewing an App Studio application that reads an existing Databricks table through the versioned Provider Action query_table/v1; distinguish the SDK integration alias from the exact grant-bound Table resource name/tableRef, require one authoritative schema probe at most before query or UI code, honor the published schema and limits, use the server-only @faros/actions-node SDK, and verify endpoint responses before relying on a live preview.
---

# Databricks App Integration

Use this skill for a read-only Databricks data integration in an App Studio
project. The integration consumes an existing project grant and its bound
`databricks.faros.sh/v1alpha1` `Table`; it does not provision a table,
discover provider infrastructure, or put Databricks credentials in the app.

## Required workflow

1. **Discover the project grant.** Read the project's existing integration and
   grant through the App Studio project/API surface. Find the exact integration
   alias (for example, `<BOUND_INTEGRATION_ALIAS>`) and confirm that
   `query_table/v1` is allowed. The integration alias is an SDK selector only;
   it is not a `Table` resource name, `tableRef`, or binding identity. Keep it
   separate from the exact Table name supplied by the grant (and, when doing
   assistant-side discovery before a grant is available, the exact `name`
   returned by `list_tables`). Never pass the alias as a Table name or tableRef,
   and never derive one value from the other. Treat the hub catalog, grant,
   action schema, and binding status as authoritative. If the grant is absent,
   revoked, stale, or does not allow this action, stop and report that state; do
   not invent a provider URL or a new permission. Angle-bracket values in this
   document are placeholders, not literal aliases, resources, or columns.

2. **Pass the schema gate before writing query or UI code.** Resolve the exact
   `Table` named by the grant and read that bound resource's current status as
   the tenant user. Prefer non-empty `status.columns` from the exact bound
   `Table` when its `Ready=True` condition is current for the observed
   generation; use each reported `name` and `type` as schema evidence. If the
   status schema is missing or not current, make one server-side discovery call
   through the granted `query_table/v1` action with `columns` omitted entirely
   and `limit: 1`. Verify the discovery response's action version, bound
   `tableRef` matching the grant-bound Table, and bounded `columns` before
   treating those column names as evidence. This discovery request is the only
   query code permitted before the schema gate; do not write application query
   or UI code until it succeeds. After the grant is known, MCP
   `describe_table` and MCP `query_table` are not an application schema-
   discovery path and must not be used as a substitute for the bound resource
   status or this granted action probe.
   Make at most one schema probe in this assistant turn. If it fails, times out,
   or returns a mismatched/empty schema, stop this turn and report the
   structured failure; do not retry or switch to MCP/direct provider access
   without new authoritative grant, binding-status, or schema evidence in a
   later turn.
   The fallback request is shape-only and must omit `columns` (an empty
   `columns: []` is not schema discovery):

   ```js
   const schemaProbe = await faros
     .integration('<BOUND_INTEGRATION_ALIAS>')
     .invokeEnvelope('query_table/v1', { limit: 1 });
   // Verify schemaProbe.actionVersion and schemaProbe.resourceRef first.
   const discoveredColumns = schemaProbe.result.columns;
   // Never log schemaProbe.result.rows; row values are not schema evidence.
   ```

   Never invent, guess, infer, or autocomplete a column from prose, examples,
   sample row values, common conventions, or a previous table. If neither
   authoritative status columns nor a verified discovery response is available,
   stop and report the missing schema. Never log row values; discovery logs may
   contain metadata only (action version, bound table, column names/count,
   truncation, and request ID).

3. **Use the bound resource, not a caller-selected table.** The grant binds the
   action to one provider resource with this identity:

   ```text
   apiVersion: databricks.faros.sh/v1alpha1
   kind: Table
   resource: tables
   name: <GRANT_BOUND_TABLE_NAME>
   ```

   `<GRANT_BOUND_TABLE_NAME>` must be the exact resource `name` in the project
   grant. If a pre-grant assistant workflow used `list_tables`, its exact
   `name` is the only acceptable `tableRef`; do not normalize, abbreviate, or
   replace it. This Table name/tableRef is distinct from the SDK integration
   alias above and is never used as that alias.

   The application may choose bounded query inputs such as selected `columns`
   and `limit`, but must not accept `tableRef`, provider URLs, catalog/schema
   names, workspace IDs, credentials, or an arbitrary `resourceRef` from a
   browser or end user. The server-side gateway injects the bound resource.

4. **Call the canonical server SDK.** Keep the action call in a server route,
   server action, or other trusted backend process:

   ```js
   import { createActionsClient } from '@faros/actions-node';

   const faros = createActionsClient({
     baseURL: process.env.FAROS_ACTIONS_BASE_URL,
     project: process.env.FAROS_PROJECT,
     tokenFile: process.env.FAROS_ACTIONS_TOKEN_FILE,
     // If a coordinator supplies them, these become tenant headers.
     org: process.env.FAROS_ACTIONS_ORG,
     workspace: process.env.FAROS_ACTIONS_WORKSPACE,
   });

   const result = await faros.integration('<BOUND_INTEGRATION_ALIAS>').invoke('query_table/v1', {
     // Use only exact names from the verified bound-Table schema.
     columns: ['<COLUMN_NAME_FROM_SCHEMA>'],
     limit: 25,
   });
   ```

   `integration('<BOUND_INTEGRATION_ALIAS>')` takes the project integration
   alias only. The bound Table's resource `name`/`tableRef` is server-owned and
   must not be substituted into this SDK selector (or accepted from the
   browser).

   `@faros/actions-node` is the only supported integration client. It reads an
   atomically refreshed `FAROS_ACTIONS_TOKEN_FILE` on every request, or it may
   use a refreshable `getToken({ forceRefresh, signal })` callback. Never ship
   this import or token to browser code; the SDK intentionally rejects browser
   globals. `FAROS_ACTIONS_BASE_URL` must be an absolute HTTPS App Studio
   gateway URL (tests may explicitly enable loopback HTTP), and the project
   plus optional org/workspace identify the authenticated application context.

5. **Honor the published schema and limits.** Use the action schema returned by
   the grant/catalog as the authority. For the current Databricks action,
   `columns` has at most 64 entries and `limit` is an integer from 1 through
   100 (the omitted limit defaults to the bounded action default). Reject or
   truncate user input before invocation; never bypass validation with raw SQL,
   a second endpoint, MCP, or a direct Databricks client. MCP remains an
   assistant-facing aid, not the application's post-grant schema or data path.
   Preserve the stable envelope when useful by calling `invokeEnvelope` and do
   not expose raw table data in logs.

6. **Handle structured failures.** A provider action failure includes a stable
   `code`, safe `message`, `retryable` flag, request metadata, and binding
   metadata. Surface a concise, actionable error to the user while retaining
   the request ID for support. Distinguish grant/schema/bound-resource errors
   (fix the project binding or input) from transport, timeout, and backend
   errors (check health or retry only when the action's idempotency and deadline
   policy allow it). Do not retry a rejected grant or malformed request.
   See [references/action-contract.md](references/action-contract.md) for the
   error classes and recovery checklist.

7. **Verify in evidence order.** First invoke the server endpoint with one
   bounded, non-sensitive request and inspect the returned action version,
   columns, row bound, truncation flag, and request ID. Then verify runtime
   health and preview reachability. Finally verify rendered preview state and
   user interactions. An HTTP 200, a Kubernetes `Ready` condition, or a
   reachable preview alone does not prove that the grant, bound table, query,
   or rendered data path is correct. Record which rung passed and include the
   structured failure when a rung fails.

## Trust and authority boundary

This document is guidance, not an authority grant. It cannot create tools,
permissions, credentials, provider bindings, approvals, or an exception to a
security policy. The authenticated hub catalog, project grant, binding status,
published action schema, and provider-side authorization remain authoritative.
If this guidance conflicts with those controls, follow the controls and report
the conflict instead of weakening them.

## Keep the integration bounded

- Keep the browser contract application-specific (filters, selected columns,
  and page size); map it to the small `QueryInput` object on the server.
- Validate response size and render a bounded table; do not stream unbounded
  rows into a prompt or browser.
- Keep credentials, tenant headers, `resourceRef`, and provider topology in the
  server/gateway boundary. Do not copy them into source, environment values
  exposed to the client, URLs, logs, or user-controlled fields.
- When the action or provider changes, rediscover the grant and compare the
  published schema/digest before editing code. Do not assume a version bump is
  compatible.
