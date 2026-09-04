---
{"schema":1,"id":"design.components.resource-section-card","title":"ResourceSectionCard","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The shared card owns border-box containment and optional headerless content while providers retain content ownership."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/resource-section-card.md#resourcesectioncard","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceSectionCard.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.resource-reads","relation":"see-also","path":"docs/design/patterns/resource-reads.md"},{"id":"design.foundations.geometry","relation":"prerequisite","path":"docs/design/foundations/geometry.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.content.ui-copy","relation":"see-also","path":"docs/design/content/ui-copy.md"}]}
---

# ResourceSectionCard

## Purpose

`ResourceSectionCard` provides the shared card boundary for vertically stacked
resource sections. Providers own each card's product content and optional
actions; the card owns border-box containment.

## Use when

Put product-facing content first in vertically stacked
`ResourceSectionCard` cards. Conditions or health may be promoted into an
always-visible product section when they help a user recover. A headerless body
supports legacy or template-driven groups without inventing a second container.

## Avoid when

Technical details are optional rather than a mandatory last card. Raw
configuration, metadata, or YAML may be omitted when they add noise. Do not
render credentials or tokens, even when a provider exposes a credential
reference or edit workflow.

## Anatomy and variants

The card has a product-facing content region, optional provider-owned section
actions, and an optional technical-details region. Section action buttons may
use a leading Lucide icon with a visible label; icon-only actions are not the
default. The headerless body is the supported variant for groups that do not
need a card header.

## Behavior

When technical details are shown, keep them closed by default and limit them to
sanitized configuration, health, metadata, or a read-only object snapshot.
Providers retain ownership of the content and optional actions.

## Content

Lead with product-facing content. Conditions or health may be promoted into an
always-visible product section; raw configuration, metadata, or YAML remains
optional and can be omitted when it adds noise. Section actions should use a
visible label when possible.

## Layout and responsive behavior

The card owns `width: 100%` and `min-width: 0`, so wide tables keep controls and
card geometry stable while their canvas scrolls internally.

## Accessibility

Section action buttons follow the shared accessible interaction and naming
contract; a leading icon supplements a visible label rather than replacing it
by default. Technical content remains read-only and sanitized when exposed.
See the [accessible interaction policy](../accessibility/interaction.md).

## Code and evidence

The canonical implementation is
`provider-sdk/portalkit-vue/ResourceSectionCard.vue`. Verify the shared
implementation with `make verify-portalkit`.

## Related guidance

Use the [resource reads pattern](../patterns/resource-reads.md) for the read
surface around the card, the [geometry foundation](../foundations/geometry.md)
for containment, the [accessible interaction policy](../accessibility/interaction.md)
for actions, and the [UI copy policy](../content/ui-copy.md) for product-first
labels and state copy.
