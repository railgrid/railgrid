---
{"schema":1,"id":"design.components.resource-stat-cards","title":"ResourceStatCards","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Providers supply meaningful facts and icons; the shared component controls list semantics and responsive density."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/resource-stat-cards.md#resourcestatcards","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceStatCards.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"Deployment fixture summary","status":"passing","evidence":"At 390px, the sample deployment summary fit a 354px content area without document overflow in light and dark. Revision and readiness remained visible during a simulated failed refresh. This does not cover all counts, providers, assistive technology, or a live backend."},{"kind":"review","ref":"Cross-product rendered and assistive-technology audit","status":"pending"}]},"relatedDocuments":[{"id":"design.components.resource-page","relation":"see-also","path":"docs/design/components/resource-page.md"},{"id":"design.foundations.geometry","relation":"prerequisite","path":"docs/design/foundations/geometry.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.content.ui-copy","relation":"see-also","path":"docs/design/content/ui-copy.md"}]}
---

# ResourceStatCards

## Purpose

`ResourceStatCards` presents provider-defined, meaningful facts in a shared
summary collection. Providers supply the facts and icons; the component owns
list semantics and responsive density.

## Use when

Use `ResourceStatCards` for meaningful provider-defined facts with optional
provider-chosen icons. Use `density="compact"` only for fact-heavy summaries;
default density preserves the existing card geometry.

## Avoid when

These facts are not a universal resource-field schema. Do not use the
component as a generic field renderer or add a second wrapper margin for
summary-slot spacing; `ResourcePage` owns that spacing.

## Anatomy and variants

1. **Label:** identifies the fact.
2. **Value:** receives the strongest emphasis and may use mono for a technical
   value.
3. **Detail:** optional context explains the value or a condition.
4. **Icon:** optional and decorative; tone colors may communicate meaningful
   state, but the text must convey it too. The current component retains its
   icon slot even when no icon is supplied.

The cards form a named native list. Default and opt-in `density="compact"`
variants use count-aware grids for one, two, four, and other card counts.

## Behavior

The component preserves native list semantics and selects the count-aware grid
classes for the supplied facts. Providers remain responsible for choosing
meaningful facts and their icons.

## Content

Each card contains a provider-defined label and value, with optional detail and
icon. Keep facts meaningful to the resource rather than treating them as a
universal field schema. Use detail to explain a meaningful state, not to repeat
the label.

## Layout and responsive behavior

At wide widths one card uses one column, two and four use two columns, and
other counts use three. Both densities reduce to two and then one column as
the resource container narrows. `ResourcePage` owns summary-slot spacing;
callers do not add a second wrapper margin.

## Accessibility

The component uses a native list for the stat collection. Fact labels and
values remain provider-owned and must be understandable in that list context;
the shared [accessible interaction policy](../accessibility/interaction.md)
supplies the cross-component naming and contrast contract.

## Code and evidence

The canonical implementation is
`provider-sdk/portalkit-vue/ResourceStatCards.vue`. The [stat-card review](http://127.0.0.1:5197/?entry=design.components.resource-stat-cards)
compares counts and densities; the [deployment fixture](http://127.0.0.1:5201/)
shows a resource summary with synthetic data. Verify distribution with
`make verify-portalkit`.

## Related guidance

Compose this summary with the [ResourcePage read shell](resource-page.md), use
the [geometry foundation](../foundations/geometry.md) for its grid, and follow
the [accessible interaction policy](../accessibility/interaction.md) and [UI
copy policy](../content/ui-copy.md) for labels and values.
