---
{"schema":1,"id":"design.components.resource-table-actions","title":"ResourceTable row actions","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Shared row action buttons provide visibility, touch targets, event isolation, and caller-owned destructive confirmation."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/resource-table-actions.md#resourcetable-row-actions","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceTableActionButton.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTableDeleteButton.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTableEditButton.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTableExpandButton.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceDeleteFailure.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.components.resource-table","relation":"implements","path":"docs/design/components/resource-table.md"},{"id":"design.components.confirm-dialog","relation":"prerequisite","path":"docs/design/components/confirm-dialog.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.content.ui-copy","relation":"see-also","path":"docs/design/content/ui-copy.md"}]}
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
Lucide icon and label. Use `ResourceTableExpandButton` as the toggle for a row
that discloses a detail row through the table's `after-row` slot.

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
event-isolation contract. The expand toggle is the one exception to idle
hiding: it stays visible, because a collapsed row gives no other hint that a
detail row exists, and its chevron turns to point at the open detail. Destructive confirmation is caller-owned through
`confirmDialog({ danger: true })`.

### Row deletion

Use the Code Repositories table as the reference for a destructive row action.

- Give the action and its pending label the resource name. Use the shared delete
  button when the action fits with the other row controls. A row with a larger
  action set can keep Delete in the shared menu.
- Use the shared danger confirmation to name the target and describe its
  consequences. Keep typed-name verification for irreversible teardown where
  the resource requires it. Use **Remove registration** when the remote
  repository is retained.
- Keep the affected action busy through the write and its immediate refresh.
  Show **Deleting** when the server reports ongoing teardown. Remove the row
  when the provider has evidence the resource is gone or an accepted deletion
  has already made it inaccessible.
- Render rejected writes using `ResourceDeleteFailure` beside the table.
  Keep the row actionable. The row action opens confirmation again. A
  provider-owned Retry action may also reopen confirmation after validating
  that the same resource and workspace are still selected.
- On cancellation, return focus to the initiating control. When that control
  disappears, use the table or collection heading as the focus target.

## Content

Every action has a resource-specific accessible label. The primary resource name
is a text-level `.k-table-resource-link` (accent, regular weight, transparent
at rest and hover). Cross-resource references are ordinary cell text. External
URLs use a concise linked action plus `ExternalLink`. Resource IDs and fully
qualified names remain ordinary text. Finite non-status enum values are muted
square `.k-badge.k-badge--label` tags; secondary counts are muted text. Operational and
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
`provider-sdk/portalkit-vue/ResourceTableDeleteButton.vue`,
`provider-sdk/portalkit-vue/ResourceTableEditButton.vue`,
`provider-sdk/portalkit-vue/ResourceTableExpandButton.vue`, and
`provider-sdk/portalkit-vue/ResourceDeleteFailure.vue`. Verify them with
`make verify-portalkit`.

## Related guidance

Use these actions with the [ResourceTable contract](resource-table.md), the
[confirmation dialog](confirm-dialog.md), the [accessible interaction
policy](../accessibility/interaction.md), and the [UI copy policy](../content/ui-copy.md).
