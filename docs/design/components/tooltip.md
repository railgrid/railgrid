---
{"schema":1,"id":"design.components.tooltip","title":"Tooltip","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The CSS-only data-k-tip tooltip is implemented in the canonical stylesheet; native title remains acceptable for plain icon labels."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/tooltip.md#tooltip","role":"design"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.components.resource-table-actions","relation":"see-also","path":"docs/design/components/resource-table-actions.md"},{"id":"design.foundations.geometry","relation":"prerequisite","path":"docs/design/foundations/geometry.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"}]}
---

# Tooltip

## Purpose

`Tooltip` supplies short supplemental help for a control without becoming a
second surface of product content. The CSS-only `data-k-tip` variant is
implemented in the canonical stylesheet.

## Use when

Use `[data-k-tip="…"]` for shared icon-only actions. Native `title` remains
acceptable for a plain icon label.

## Avoid when

Content never exceeds two lines; a longer explanation is a popover. Tooltips
never glow and should not carry a full interaction or content surface.

## Anatomy and variants

The styled variant has a 4px radius, 4px 8px padding, 260px maximum width, a
6px offset, and no arrow. Its surface is `surface-overlay` with a
`border-subtle` hairline and `0 4px 12px rgba(0,0,0,.35)` elevation (`.10` in
light theme).

## Behavior

Show delay is 300ms, hide is immediate, and hover and focus both show it.

## Content

Type is 11px `text-primary`. Keep the explanation to two lines or fewer; move a
longer explanation into a popover.

## Layout and responsive behavior

Keep the 260px maximum width, 6px offset, 4px radius, and 4px 8px padding. The
tooltip has no arrow.

## Accessibility

Hover and focus both reveal the tooltip, while the triggering control retains
its own accessible label. Follow the [accessible interaction policy](../accessibility/interaction.md)
for naming and focus behavior.

## Code and evidence

The canonical implementation is the `[data-k-tip]` recipe in
`provider-sdk/portalkit/faros-ui.css`. Verify with `make verify-portalkit`.

## Related guidance

Use the [resource table actions](resource-table-actions.md) contract for
icon-only row actions, the [geometry foundation](../foundations/geometry.md)
for the surface geometry, the [accessible interaction policy](../accessibility/interaction.md),
and the [review checklist](../quality/review-checklist.md).
