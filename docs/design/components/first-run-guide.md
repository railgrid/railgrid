---
{"schema":1,"id":"design.components.first-run-guide","title":"FirstRunGuide","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"FirstRunGuide is the shared first-use surface; vanilla portals use the matching k-first-run semantic classes."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/first-run-guide.md#firstrunguide","role":"design"},{"path":"provider-sdk/portalkit-vue/FirstRunGuide.vue","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.resource-creation","relation":"implements","path":"docs/design/patterns/resource-creation.md"},{"id":"design.content.ui-copy","relation":"prerequisite","path":"docs/design/content/ui-copy.md"},{"id":"design.patterns.navigation-and-feedback","relation":"see-also","path":"docs/design/patterns/navigation-and-feedback.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"}]}
---

# FirstRunGuide

## Purpose

For an authoritative first-use empty collection, use `FirstRunGuide.vue` or
matching `k-first-run*` semantic markup in a vanilla portal. It explains the
value, offers the immediate primary action, and shows the shortest meaningful
resource journey. Missing prerequisites change the current step and action;
they do not leave a user at an inert empty table.

## Use when

Use it for an authoritative first-use empty collection. Use the Vue component
or matching `k-first-run*` semantic markup in a vanilla portal.

## Avoid when

Do not leave a first-use collection at an inert empty table. Missing
prerequisites change the current step and action rather than removing the next
meaningful step.

## Anatomy and variants

The guide explains the value, offers the immediate primary action, and shows
the shortest meaningful resource journey. The supported implementations are
`FirstRunGuide.vue` and matching vanilla `k-first-run*` semantic markup.

## Behavior

Missing prerequisites change the current step and action. The guide continues
to offer a meaningful next step rather than leaving the user at an inert empty
table.

## Content

Use an eyebrow, a one-line explanation, and one primary action. The copy
explains the value and shortest meaningful resource journey; the primary action
is the immediate next step.

## Layout and responsive behavior

The empty state is an invitation: use a restrained `.contour-grid` texture, an
eyebrow, a one-line explanation, and one primary action. The primary action is
the only solid accent action in the view and follows the shared glow rule.

## Accessibility

The supported vanilla markup is semantic `k-first-run*` markup. Keep the
immediate primary action reachable and identifiable in the empty collection's
accessible structure; this component is not a replacement for the shared
interaction policy.

## Code and evidence

The canonical Vue component is [`FirstRunGuide.vue`](../../../provider-sdk/portalkit-vue/FirstRunGuide.vue),
and shared styling lives in [`faros-ui.css`](../../../provider-sdk/portalkit/faros-ui.css).
Distribution is checked by `make verify-portalkit`.

## Related guidance

See the [resource creation journey](../patterns/resource-creation.md), [UI copy
policy](../content/ui-copy.md), [navigation and feedback](../patterns/navigation-and-feedback.md),
and [accessible interaction policy](../accessibility/interaction.md).
