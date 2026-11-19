---
{"schema":1,"id":"design.patterns.resource-reads","title":"Resource reads and background refresh","kind":"pattern","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"PortalKit ResourcePage, ResourceTable, and page-state.ts own the shared read lifecycle; callers own fetching."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/patterns/resource-reads.md#resource-reads-and-background-refresh","role":"design"},{"path":"provider-sdk/portalkit/refresh.ts","role":"implementation"},{"path":"provider-sdk/portalkit/page-state.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourcePage.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTable.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."},{"kind":"browser","ref":"Edge Service and App Studio project detail refresh fixtures","status":"passing","evidence":"Chromium at 1280px light and 390px dark verified that each detail recovers automatically from a failed background read and keeps its last snapshot. Edge Service also retained an unsaved port change. These fixtures use mocked APIs."}]},"relatedDocuments":[{"id":"design.components.resource-page","relation":"see-also"},{"id":"design.components.resource-table","relation":"see-also"},{"id":"design.components.resource-table-actions","relation":"related"},{"id":"design.components.status-badge","relation":"related"},{"id":"design.components.portalkit-support","relation":"see-also"},{"id":"design.foundations.recipes","relation":"prerequisite"},{"id":"design.quality.ui-conformance","relation":"see-also"}]}
---

# Resource reads and background refresh

Resource pages, tables, and dashboard summaries share the `ResourceReadState`
contract from PortalKit. `refreshMode` is `foreground` or `background`. A first
read may show a skeleton or pending state, but once a populated snapshot—or an
authoritative empty snapshot—exists, keep it visible through every later
background read and transient failure. Background work must not replace an
empty/no-match body, spin or disable header actions, or otherwise disturb the
useful surface. An out-of-flow `aria-busy` indicator or live status is
appropriate when it communicates refresh without changing geometry.

User Refresh, Retry, query, filter, and page actions are foreground reads and
show immediate feedback even when queued behind another read. Reads serialize.
A timer request never invalidates a useful active read; at most one follow-up is
coalesced, with foreground priority. Explicit authority or resource/tenant/user
identity invalidation fences stale results. Token rotation alone is not an
identity change. Reset snapshots only when tenant, user, or resource identity
changes. Stop or unmount cancels the timer and queued work.

## Provider refresh contract

Import the framework-neutral helpers from `src/portalkit/refresh.ts`. PortalKit
distributes them to every provider. Give each independently loaded collection
or instance one owner for its requests and its last successful snapshot.
An instance backed by an already refreshed store uses that store's snapshot.

- Route initial loads, Refresh, Retry, and change notifications through
  `createLatestRefreshController`. Check `isCurrent(requestID)` before
  committing rows, errors, and loading state.
- After a successful write, call `invalidate()`, discard any cached complete
  list and pending navigation cursor, and request an immediate foreground read.
  A DELETE can be accepted while a finalizer still owns the object. Show the
  terminating state until a fresh read confirms removal. A rejected delete
  leaves the row available and shows the failure.
- Schedule background reconciliation with `createAdaptiveRefreshTimer`.
  The stable default is 30 seconds. Use a shorter delay while a resource is
  changing. Schedule again after a read settles, including after a failure.
  An armed timer also wakes when the tab becomes visible or the browser comes
  online. Cancel when a route is deactivated and call `stop()` on teardown.
  Event streams can request earlier reads. Detail refreshes keep unsaved
  form values and update the observations around them. A same-name resource
  with a different UID requires a fresh review before writes can resume.
  Bind writes to the reviewed UID at the API boundary.
- For paginated Kubernetes lists, keep one `createResourcePageReader()` per
  table. Call its `navigate(cursor)` method when the user chooses a page. The reader consumes the
  supplied cursor for navigation. A refresh starts a fresh list and walks to
  the displayed page, stopping earlier if the collection shrank. Reusing a
  continuation token for refresh would return the pre-mutation snapshot.
  Beyond page three, the reader allows a background walk after 30 seconds per
  three pages, rounded up. Manual and post-mutation refreshes run immediately.
- Limit automatic requests to resource reads. Running a saved query, starting
  a job, and repeating a mutation require their own user intent. Workflow
  streams can retain their own progress and reconnect behavior.

Edit the canonical helpers under `provider-sdk/portalkit/`, then run
`make sync-portalkit`. The shared table and page render the read state.

Adoption is lossless. Before moving a resource, inventory every legacy field,
custom workflow/action/editor/table, read or mutation state (including stale and
deleting), and sensitive-data boundary. Shared layout must not discard
provider-specific content. Providers choose meaningful stat cards, sections,
editors, tables, and actions. Secrets and credential values never render,
including a credential reference or edit workflow.

Examples of resource-read adoption include Code Repository and Connection;
Edge and Edge Service; Databricks Connection, Warehouse, and Table;
Infrastructure Application Template Instance; and MCP Access. These examples
inherit the canonical title hierarchy. They are not a complete inventory of
`ResourcePage` consumers or a universal field schema.

## Edge Services example

Run the isolated fixture from the repository root:

```sh
./portal/node_modules/.bin/vite --config hack/edge-services-visual/vite.config.mjs
```

Open http://127.0.0.1:5199. The fixture supplies synthetic services and uses
the canonical ResourceTable and StatusBadge. Select **Simulate failed refresh**
to inspect the stale snapshot and **Retry** to restore the fixture. The theme
control exercises both portal themes. Search filters the example rows.

The layout makes three decisions:

1. The page title and creation action establish the task before the collection.
   Search sits with the table because it changes that collection.
2. Service names lead each row; edge, type, target, and status support a quick
   scan. The table retains horizontal scrolling at narrow widths.
3. A failed background refresh leaves the previous rows visible and places the
   failure and retry next to the collection. This preserves the operator's
   context while making stale data explicit.

Vercel's [request list and inspector](https://vercel.com/changelog/ai-gateway-logs)
informed the emphasis on a scannable collection and retained context.
Cloudflare's [Kumo Resource List](https://kumo-ui.com/blocks/resource-list/)
informed the grouping of title, action, and collection controls. The fixture
demonstrates composition and read-state behavior with synthetic data; it does
not exercise the live Edges backend or service creation.

Mobile fixture review covered 320px and 390px browser viewports. The table
keeps overflow inside its scroll area, and Retry returns focus to the fixture's
refresh control. Service creation is disabled in this example.

## Deployment detail example

Run the isolated fixture from the repository root:

```sh
./portal/node_modules/.bin/vite --config hack/deployment-detail-visual/vite.config.mjs
```

Open http://127.0.0.1:5201. The example composes the canonical ResourcePage,
ResourceStatCards, ResourceSectionCard, ConditionsPanel, ResourceTable, and
StatusBadge with synthetic deployment data. The fixture depicts a rollout
where revision v42 has two of three replicas ready. Select **Review conditions**
to reach the controller diagnostic. **Simulate failed refresh** preserves the
snapshot and exposes a retry; the theme control switches portal themes.

The composition makes these decisions:

1. The deployment name, environment, cluster, and status establish which
   resource needs attention. Revision, replica count, and update time provide
   the immediate context for a rollout decision.
2. The rollout summary names the incomplete revision and points to conditions.
   The condition message identifies the failed readiness check. Recent events
   order that diagnostic with the rollout and previous revision.
3. Health signals state their environment and time window. The adjacent log
   excerpt identifies the replica and timestamp, so its scope can be compared
   with the condition and events.
4. A failed background refresh keeps those observations visible and labels the
   snapshot stale. The shared ResourcePage owns the retry presentation.

[Baseten's scoped metrics](https://docs.baseten.co/observability/metrics) informed
the explicit environment, deployment, and time context.
[Railway's observability dashboard](https://docs.railway.com/observability)
informed grouping operational signals with logs. This is a synthetic composition
study. It does not establish that the Infrastructure provider currently exposes
deployment metrics, logs, rollout controls, or these specific data fields.

Mobile fixture review covered 320px and 390px browser viewports. Retry returns
focus to the fixture's refresh control, and Review conditions moves focus to
the conditions section. Tables keep their overflow inside their scroll areas.

The Infrastructure instance detail applies the conditions navigation pattern
when a failed instance has reported conditions. Its Review conditions action
moves focus to the existing conditions section. The instance page presents
controller-reported data and does not imply the fixture's metrics or logs exist.
