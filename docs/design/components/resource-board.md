---
{"schema":1,"id":"design.components.resource-board","title":"ResourceBoard","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Shared board layout collapses empty columns into discoverable controls; consumers own data and cards."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/resource-board.md#resourceboard","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceBoard.vue","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"passing","evidence":"Factory and Linear synthetic browser checks exercised reveal and collapse across desktop and mobile in light and dark themes; mounted tests cover counts, focus and live population."}]},"relatedDocuments":[{"id":"design.patterns.resource-reads","relation":"see-also","path":"docs/design/patterns/resource-reads.md"},{"id":"design.foundations.geometry","relation":"prerequisite","path":"docs/design/foundations/geometry.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.content.ui-copy","relation":"see-also","path":"docs/design/content/ui-copy.md"}]}
---

# ResourceBoard

## Purpose

Display grouped resource cards while keeping empty workflow phases discoverable.

## Use when

Use for phase or workflow boards with stable column IDs and current item counts.

## Avoid when

Use ResourceTable for tabular comparisons. This component does not support drag-to-transition workflows.

## Anatomy and variants

Pass `columns` containing `id`, `label`, and `count`. The `icon` and `column` slots receive that descriptor. Consumers own cards and fetching. `ariaLabel` names the board and `emptyText` names the empty-lane message.

## Behavior

Populated columns always appear. Empty columns start as compact buttons in Hidden columns. Users may reveal an empty column and hide it again. Expansion is local to the mounted board; key by data scope to reset it. Unknown observed phases must be supplied, never dropped.

## Content

Counts represent the supplied filtered collection, not workspace totals. Consumers supply precise workflow names, card links, and product-specific details.

## Layout and responsive behavior

Horizontal overflow preserves readable cards on small screens. Each visible lane has independently scrollable content. Shared canonical CSS owns spacing and geometry.

## Accessibility

Named regions support keyboard scrolling. Native buttons reveal and collapse empty lanes. Focus moves to the revealed lane and returns to its button when collapsed.

## Code and evidence

Canonical implementation: `provider-sdk/portalkit-vue/ResourceBoard.vue`. Render only after an authoritative first read and retain content on refresh failures. Consumers remain responsible for read-state feedback. Verification status is recorded in this entry's metadata.

## Related guidance

See [resource reads](../patterns/resource-reads.md), [geometry](../foundations/geometry.md), and [interaction](../accessibility/interaction.md).
