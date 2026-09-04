---
{"schema":1,"id":"design.components.resource-table-actions","title":"ResourceTable row actions","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Shared row action buttons provide visibility, touch targets, event isolation, and caller-owned destructive confirmation."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/resource-table-actions.md#resourcetable-row-actions","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceTableActionButton.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTableDeleteButton.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTableEditButton.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.components.resource-table","relation":"implements","path":"docs/design/components/resource-table.md"},{"id":"design.components.confirm-dialog","relation":"prerequisite","path":"docs/design/components/confirm-dialog.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.content.ui-copy","relation":"see-also","path":"docs/design/content/ui-copy.md"}]}
---

# ResourceTable row actions

## Purpose

Shared row action buttons provide compact edit, delete, and other row operations
with consistent visibility, touch targets, event isolation, and
caller-owned destructive confirmation.

## Use when

Vue resource tables use `ResourceTableEditButton` and
`ResourceTableDeleteButton` for compact edit/delete shortcuts. Use
`ResourceTableActionButton` for other compact actions; the caller supplies the
Lucide icon and label.

## Avoid when

Do not replace the shared helpers with provider-local row-action markup or use a
second visible status line for verbose provider feedback. Danger actions never
glow.

## Anatomy and variants

`ResourceTableActionButton` accepts a caller-supplied Lucide icon and label,
optional busy label/state, disabled state, and one of the sanctioned `neutral`,
`accent`, `warning`, or `danger` tones. Edit and delete helpers are the
preferred semantic shortcuts.

## Behavior

Actions reveal on row hover or keyboard focus and remain visible on touch. All
row action buttons inherit the same hover/focus/touch visibility and
event-isolation contract. Destructive confirmation is caller-owned through
`confirmDialog({ danger: true })`.

## Content

Every action has a resource-specific accessible label. The primary resource name
is a text-level `.k-table-resource-link` (accent, regular weight, transparent
at rest and hover). Cross-resource references are ordinary cell text. External
URLs use a concise linked action plus `ExternalLink`. Resource IDs and fully
qualified names remain ordinary text. Finite non-status enum values are muted
square `.k-badge` tags; secondary counts are muted text. Operational and
lifecycle state uses `StatusBadge`; verbose provider feedback belongs in the
title and accessible name rather than a second visible status line. Mono is
reserved for genuinely technical content such as schema column names and
types; providers do not restyle these properties locally.

## Layout and responsive behavior

Keep row actions compact while preserving their shared touch-target behavior;
the table's primary column owns the row-action edge. Touch surfaces remain
reachable through the shared visibility contract.

## Accessibility

Every action has a resource-specific accessible label. Keyboard focus reveals
the actions, nested action events remain isolated from row activation, and
destructive confirmation is explicit and caller-owned. Follow the [accessible
interaction policy](../accessibility/interaction.md) for the complete keyboard,
focus, naming, and target contract.

## Code and evidence

The canonical implementations are
`provider-sdk/portalkit-vue/ResourceTableActionButton.vue`,
`provider-sdk/portalkit-vue/ResourceTableDeleteButton.vue`, and
`provider-sdk/portalkit-vue/ResourceTableEditButton.vue`. Verify them with
`make verify-portalkit`.

## Related guidance

Use these actions with the [ResourceTable contract](resource-table.md), the
[confirmation dialog](confirm-dialog.md), the [accessible interaction
policy](../accessibility/interaction.md), and the [UI copy policy](../content/ui-copy.md).
