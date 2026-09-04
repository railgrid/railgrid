---
{"schema":1,"id":"design.components.resource-stat-cards","title":"ResourceStatCards","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Providers supply meaningful facts and icons; the shared component controls list semantics and responsive density."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/resource-stat-cards.md#resourcestatcards","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceStatCards.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.components.resource-page","relation":"see-also","path":"docs/design/components/resource-page.md"},{"id":"design.foundations.geometry","relation":"prerequisite","path":"docs/design/foundations/geometry.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.content.ui-copy","relation":"see-also","path":"docs/design/content/ui-copy.md"}]}
---

# ResourceStatCards

## Purpose

`ResourceStatCards` presents provider-defined, meaningful facts in a shared
summary collection. Providers supply the facts and icons; the component owns
list semantics and responsive density.

## Use when

Use `ResourceStatCards` for meaningful provider-defined facts with
provider-chosen icons. Use `density="compact"` only for fact-heavy summaries;
default density preserves the existing card geometry.

## Avoid when

These facts are not a universal resource-field schema. Do not use the
component as a generic field renderer or add a second wrapper margin for
summary-slot spacing; `ResourcePage` owns that spacing.

## Anatomy and variants

The component renders a native list of stat cards. It supports the default
density and the opt-in `density="compact"` variant. Count-aware grid classes
balance one, two, four, and three-plus cards.

## Behavior

The component preserves native list semantics and selects the count-aware grid
classes for the supplied facts. Providers remain responsible for choosing
meaningful facts and their icons.

## Content

Each card contains a provider-defined fact and a provider-chosen icon. Keep the
facts meaningful to the resource rather than treating them as a universal
resource-field schema.

## Layout and responsive behavior

Both densities use a responsive three-column, then two-column, then one-column
grid. `ResourcePage` owns summary-slot spacing; callers do not add a second
wrapper margin.

## Accessibility

The component uses a native list for the stat collection. Fact labels and
values remain provider-owned and must be understandable in that list context;
the shared [accessible interaction policy](../accessibility/interaction.md)
supplies the cross-component naming and contrast contract.

## Code and evidence

The canonical implementation is
`provider-sdk/portalkit-vue/ResourceStatCards.vue`. Verify the shared
implementation with `make verify-portalkit`.

## Related guidance

Compose this summary with the [ResourcePage read shell](resource-page.md), use
the [geometry foundation](../foundations/geometry.md) for its grid, and follow
the [accessible interaction policy](../accessibility/interaction.md) and [UI
copy policy](../content/ui-copy.md) for labels and values.
