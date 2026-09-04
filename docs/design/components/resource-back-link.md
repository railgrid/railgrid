---
{"schema":1,"id":"design.components.resource-back-link","title":"ResourceBackLink","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"ResourceBackLink provides a real browser hyperlink with narrowly scoped SPA interception."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/resource-back-link.md#resourcebacklink","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceBackLink.vue","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.components.resource-page","relation":"see-also","path":"docs/design/components/resource-page.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.foundations.recipes","relation":"implements","path":"docs/design/foundations/recipes.md"},{"id":"design.patterns.resource-creation","relation":"see-also","path":"docs/design/patterns/resource-creation.md"}]}
---

# ResourceBackLink

## Purpose

Use `ResourceBackLink` before and outside a `ResourcePage` shell on Vue detail
routes. It always renders a real `href`. Only an unmodified primary activation
may be intercepted for caller-owned SPA `back` routing; modified and non-primary
clicks retain native browser behavior. A disabled link prevents navigation and
`back` emission, exposes `aria-disabled="true"`, and remains out of keyboard
tab order with `tabindex="-1"`. Its arrow flips in RTL.

## Use when

Use it before and outside a `ResourcePage` shell on Vue detail routes. Use the
`.k-back-action` recipe directly for create flows and other non-detail controls.

## Avoid when

Do not replace the detail route's single return affordance with provider-level
collection tabs. Do not use a JavaScript-only action in place of the real
`href`; modified and non-primary clicks must retain native browser behavior.

## Anatomy and variants

The component always renders a real `href` and uses the `.k-back-action` recipe:
intrinsic-width, start-aligned, borderless, 12px/500 accent text with a 6px icon
gap, accent-hover underline, and no control surface. Its arrow flips in RTL.

## Behavior

Only an unmodified primary activation may be intercepted for caller-owned SPA
`back` routing. Modified and non-primary clicks retain native browser behavior.
A disabled link prevents navigation and `back` emission, exposes
`aria-disabled="true"`, and remains out of keyboard tab order with
`tabindex="-1"`.

## Content

The component is the single return affordance on a detail route. Route-specific
destination and visible wording remain caller-owned; use the recipe directly
for create flows and other non-detail controls.

## Layout and responsive behavior

The `.k-back-action` recipe is intrinsic-width, start-aligned, borderless, and
has a 6px icon gap. Coarse and hybrid any-pointer targets are at least 44×44px.

## Accessibility

The real `href` preserves native link behavior. A disabled link exposes
`aria-disabled="true"` and `tabindex="-1"`; modified and non-primary clicks are
not intercepted. The target-size rule applies to coarse and hybrid pointers.

## Code and evidence

The canonical implementation is [`ResourceBackLink.vue`](../../../provider-sdk/portalkit-vue/ResourceBackLink.vue),
with shared styling in [`faros-ui.css`](../../../provider-sdk/portalkit/faros-ui.css).
Distribution is checked by `make verify-portalkit`.

## Related guidance

See [ResourcePage](resource-page.md), [shared k-* recipes](../foundations/recipes.md),
[resource creation journeys](../patterns/resource-creation.md), and the
[accessible interaction policy](../accessibility/interaction.md).
