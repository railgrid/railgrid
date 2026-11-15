---
{"schema":1,"id":"design.components.tooltip","title":"Tooltip","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The shared data-k-tip manager positions visual tooltips; CSS provides a fallback before initialization."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/tooltip.md#tooltip","role":"design"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"},{"path":"provider-sdk/portalkit/tooltip.ts","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"The canonical tooltip manager and shared styles passed distribution parity after the latest shared-style update."},{"kind":"browser","ref":"Tooltip in native dialog fixture","status":"passing","evidence":"The layering fixture was checked at 390px: Escape dismissed the tooltip before the enclosing dialog, with focus retained. This does not cover all product contexts or screen readers."},{"kind":"review","ref":"Product-wide tooltip and assistive-technology audit","status":"pending"}]},"relatedDocuments":[{"id":"design.components.resource-table-actions","relation":"see-also","path":"docs/design/components/resource-table-actions.md"},{"id":"design.foundations.geometry","relation":"prerequisite","path":"docs/design/foundations/geometry.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"}]}
---

# Tooltip

## Purpose

`Tooltip` supplies a short visual label for a control without becoming a
second surface of product content. The `data-k-tip` recipe is managed by a
shared document-level controller, with a CSS fallback before initialization.

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

Show delay is 150ms. Hover and keyboard focus reveal it; the pointer can move
onto the visible bubble without dismissing it. Escape dismisses it
without moving focus until the pointer and focus leave the trigger. The tooltip
clamps to the viewport and moves below the trigger when there is no room above.
When the trigger is inside an open native dialog, the shared tooltip is hosted
inside that dialog. With focus on the trigger, Escape dismisses the tooltip
without also closing the dialog; a subsequent Escape can dismiss the dialog.

## Content

Type is 11px `text-primary`. Keep the explanation to two lines or fewer; move a
longer explanation into a popover.

## Layout and responsive behavior

Keep the 260px maximum width, 6px offset, 4px radius, and 4px 8px padding. The
tooltip has no arrow.

## Accessibility

Hover and focus both reveal the tooltip, while the triggering control retains
its own accessible label. The managed bubble is hidden from assistive
technology and touch does not reveal it. Do not put an explanation, disabled
reason, or other information available only in `data-k-tip`: provide it as
visible text or an accessible description that also works without hover.
Follow the [accessible interaction policy](../accessibility/interaction.md)
for naming and focus behavior.

## Code and evidence

The canonical implementation is the `[data-k-tip]` recipe in
`provider-sdk/portalkit/railgrid-ui.css` and `provider-sdk/portalkit/tooltip.ts`.
Verify distribution with `make verify-portalkit`.
Open the [tooltip review](http://127.0.0.1:5197/?entry=design.components.tooltip)
to compare an endpoint action and a resource table row action. The
[Edge Services fixture](http://127.0.0.1:5199/) uses the shared row
action implementation. Both fixtures use sample data.

## Related guidance

Use the [resource table actions](resource-table-actions.md) contract for
icon-only row actions, the [geometry foundation](../foundations/geometry.md)
for the surface geometry, the [accessible interaction policy](../accessibility/interaction.md),
and the
[review checklist](../quality/review-checklist.md).
