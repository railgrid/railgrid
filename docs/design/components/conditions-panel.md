---
{"schema":1,"id":"design.components.conditions-panel","title":"ConditionsPanel","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"ConditionsPanel provides shared status detail without turning technical state into an always-visible second status line."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/conditions-panel.md#conditionspanel","role":"design"},{"path":"provider-sdk/portalkit-vue/ConditionsPanel.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.components.resource-section-card","relation":"see-also","path":"docs/design/components/resource-section-card.md"},{"id":"design.components.status-badge","relation":"see-also","path":"docs/design/components/status-badge.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"}]}
---

# ConditionsPanel

## Purpose

Conditions and health may be promoted into an always-visible product-facing
section when they help a user recover. Raw configuration, metadata, YAML, and
other technical detail are optional and stay closed by default. When exposed,
the panel is limited to sanitized configuration, health, metadata, or a
read-only object snapshot. Credentials and tokens never render.

## Use when

Use the panel when conditions or health help a user understand recovery or the
current resource state. Promote that information into an always-visible,
product-facing section only when it helps recovery.

## Avoid when

Do not use it as an always-visible second status line for technical detail. Raw
configuration, metadata, YAML, and other technical detail stay closed by
default; credentials and tokens never render.

## Anatomy and variants

When exposed, the panel contains only sanitized configuration, health,
metadata, or a read-only object snapshot. There is no separate visual variant
documented here: use the shared panel inside a `ResourceSectionCard` rather
than inventing a second technical container.

## Behavior

Technical detail remains optional and closed by default. Lifecycle status stays
in a `StatusBadge`; verbose provider feedback belongs in accessible names and
titles.

## Content

Show only sanitized configuration, health, metadata, or a read-only object
snapshot. Never render credentials or tokens.

## Layout and responsive behavior

Place the shared panel inside a `ResourceSectionCard` rather than introducing
a second technical container. The panel's surrounding card owns its layout.

## Accessibility

Lifecycle status remains a `StatusBadge`, while verbose provider feedback is
carried by accessible names and titles. Keep technical detail readable without
making credentials or tokens available to assistive technology or the page.

## Code and evidence

The canonical implementation is [`ConditionsPanel.vue`](../../../provider-sdk/portalkit-vue/ConditionsPanel.vue).
The shared distribution contract is checked by `make verify-portalkit`.

## Related guidance

See the [ResourceSectionCard](resource-section-card.md), [StatusBadge](status-badge.md),
[accessible interaction policy](../accessibility/interaction.md), and [review
checklist](../quality/review-checklist.md).
