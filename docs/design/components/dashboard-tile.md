---
{"schema":1,"id":"design.components.dashboard-tile","title":"Provider dashboard summary","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"Providers own their summary content; PortalKit supplies shared tile conventions. Recovery behavior is not yet consistent across all providers."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/dashboard-tile.md#provider-dashboard-summary","role":"design"},{"path":"provider-sdk/portalkit/dashboardtile.ts","role":"implementation"},{"path":"providers/agents/portal/src/views/DashboardTile.vue","role":"implementation"},{"path":"providers/edges/portal/src/DashboardTile.vue","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"test","ref":"providers/agents/portal/src/test/dashboard-tile.test.ts","status":"passing","evidence":"Mounted Agents and Edges product tests cover initial failure, pending retry and focus recovery; Edges also checks a queued foreground retry. Agents tests cover stale populated and empty snapshots."},{"kind":"test","ref":"hack/design-review/edges-dashboard-tile.test.ts","status":"passing","evidence":"Mounted Edges tile checks initial retry and a foreground retry queued behind an active refresh."},{"kind":"test","ref":"hack/design-review/code-dashboard-tile.test.ts","status":"passing","evidence":"Mounted Code tile checks a foreground retry queued behind an active refresh and keyboard focus recovery."},{"kind":"test","ref":"providers/infrastructure/portal/src/dashboardTileMounted.test.ts","status":"passing","evidence":"Mounted Infrastructure tile checks queued retry, focus recovery, and retention of a snapshot after a binding error."},{"kind":"test","ref":"hack/design-review/app-studio-dashboard-tile.test.ts","status":"passing","evidence":"Mounted App Studio tile checks retry focus, queued retries and same-workspace caller changes."},{"kind":"test","ref":"hack/design-review/quickstart-dashboard-tile.test.ts","status":"passing","evidence":"Mounted Quickstart retry keeps focus while pending and returns it to the restored summary."},{"kind":"test","ref":"hack/design-review/kuery-dashboard-tile.test.ts","status":"passing","evidence":"Mounted Kuery retry restores focus after a failed retry and retains a stale snapshot on binding loss."},{"kind":"review","ref":"Other provider dashboard tile recovery","status":"pending","evidence":"All seven shipped provider tile types have simulated examples; live backend and broader accessibility review remain pending."}]},"relatedDocuments":[{"id":"design.patterns.resource-reads","relation":"see-also"},{"id":"design.components.dashboard-usage","relation":"see-also"},{"id":"design.components.portalkit-support","relation":"see-also"}]}
---

# Provider dashboard summary

## Purpose

Summarize a provider's current workspace resources at a glance and let a user open a relevant item.

## Use when

The workspace dashboard needs a concise provider summary with a route to further detail.

## Avoid when

The task requires a full collection or a usage quota meter.

## Anatomy and variants

The host supplies the titled card. The provider supplies concise counts, a short list of meaningful recent items, and an empty or read-state message.

## Behavior

A first read shows loading, an actionable failure, or an authoritative empty or populated result. Keep a populated or empty snapshot visible during a later refresh or failure and identify stale data, including when a previously available provider binding disappears. A missing binding on the first read may instead be an ordinary empty state. During an initial retry keep the failure and focused action visible, announce pending state, and ignore repeated activation; if success removes the focused control, move focus to the restored summary without interrupting a user who moved elsewhere. A workspace or caller change fences the previous snapshot. The provider owns its requests, permissions, data and navigation. When the
host reports a provider is not ready, its tile explains that the summary will
appear when the provider becomes available; it does not show a blank body. The [not-ready fixture](http://127.0.0.1:5204/ui/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222?preview=provider-not-ready) shows this state with a synthetic Edges provider; it does not contact a live provider. In dashboard
arrangement mode, the host hides navigation and recovery controls even when a
provider bundle failed to load; those actions return when arrangement ends.

An explicit authorization, rate-limit, or service failure takes precedence over wording that resembles a missing workspace or binding; it must remain visible as a failure. A typed `400 TenantMissing` on the first read is an expected absence; unrelated `400` responses are failures.

## Content

Keep navigation labels specific and use status text alongside color. Failures name the affected summary and offer retry; stale states say the last result is shown. When a recognized status or reason is available, use the shared fixed recovery hint for access, missing provider, rate limit, or service failure. Provider messages can contain arbitrary or sensitive text and are not displayed. A richer diagnostic disclosure remains an open design and implementation task. See [dashboard usage](dashboard-usage.md) for quota-specific presentation.

## Layout and responsive behavior

Tiles share compact spacing, semantic colors, and a row cap. They must fit narrow cards without hiding recovery actions. The [mounted Agents fixture](http://127.0.0.1:5197/?entry=design.components.dashboard-tile) uses a simulated service. Review light and dark themes and narrow width. It does not establish backend or product-wide behavior.

## Accessibility

Use native buttons and descriptive names, keyboard focus, and textual status. Preserve focus during recovery.

## Code and evidence

The [shared tile helper](../../../provider-sdk/portalkit/dashboardtile.ts) defines polling and visual slots. The mounted review includes the Agents, Edges, Code, Infrastructure, App Studio, [Quickstart](../../../providers/quickstart/portal/src/tile.ts), and [Kuery](../../../providers/kuery/portal/src/dashboard-tile.ts) tiles. Their simulated services cover retry. The [dashboard fixture](http://127.0.0.1:5204/ui/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222?preview=usage-thresholds) also shows host-level unavailable summaries: Customize hides their recovery and navigation actions, and Done restores them. These are synthetic provider failures; product-wide accessibility and live backend behavior remain unverified. See [resource reads](../patterns/resource-reads.md) for the cross-product state contract.

## Related guidance

See [resource reads](../patterns/resource-reads.md), [dashboard usage](dashboard-usage.md), and [PortalKit support](portalkit-support.md).
