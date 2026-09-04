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
