---
{"schema":1,"id":"design.patterns.resource-reads","title":"Resource reads and background refresh","kind":"pattern","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"PortalKit ResourcePage, ResourceTable, and page-state.ts own the shared read lifecycle; callers own fetching."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/patterns/resource-reads.md#resource-reads-and-background-refresh","role":"design"},{"path":"provider-sdk/portalkit/page-state.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourcePage.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTable.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.components.resource-page","relation":"see-also"},{"id":"design.components.resource-table","relation":"see-also"},{"id":"design.components.resource-table-actions","relation":"related"},{"id":"design.components.status-badge","relation":"related"},{"id":"design.components.portalkit-support","relation":"see-also"},{"id":"design.foundations.recipes","relation":"prerequisite"},{"id":"design.quality.ui-conformance","relation":"see-also"}]}
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

Use a slower cadence for stable resources (current providers use about 30s) and
a faster provider-appropriate cadence for unsettled or error states. Keep state
ownership in canonical `ResourcePage`, `ResourceTable`, and `page-state.ts`;
edit canonical sources first, then run `make sync-portalkit`.

Adoption is lossless. Before moving a resource, inventory every legacy field,
custom workflow/action/editor/table, read or mutation state (including stale and
deleting), and sensitive-data boundary. Shared layout must not discard
provider-specific content. Providers choose meaningful stat cards, sections,
editors, tables, and actions. Secrets and credential values never render,
including a credential reference or edit workflow.

Current consumers are Code Repository and Connection; Edge and Edge Service;
Databricks Connection, Warehouse, and Table; Infrastructure Application
Template Instance; and MCP Access. These nine consumers inherit the canonical
title hierarchy, but are adoption examples rather than a universal field
schema.

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
