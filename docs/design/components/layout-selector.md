---
{"schema":1,"id":"design.components.layout-selector","title":"LayoutSelector","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"LayoutSelector is a controlled two-mode menu with storage as a non-fatal preference."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/layout-selector.md#layoutselector","role":"design"},{"path":"provider-sdk/portalkit-vue/LayoutSelector.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/layoutPreference.ts","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.components.menu","relation":"see-also","path":"docs/design/components/menu.md"},{"id":"design.patterns.controls","relation":"see-also","path":"docs/design/patterns/controls.md"},{"id":"design.components.resource-table","relation":"see-also","path":"docs/design/components/resource-table.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"}]}
---

# LayoutSelector

## Purpose

Use `LayoutSelector` when one resource collection has grid and list
presentations. It is controlled through `modelValue` and
`update:modelValue`, with exactly two stable values: `grid` and `list`.
`layoutPreference.ts` validates stored values, defaults to `grid`, and treats
unavailable or failing browser storage as a non-fatal preference miss.

## Use when

Use it when one resource collection offers grid and list presentations. Keep the
control controlled through `modelValue` and `update:modelValue`.

## Avoid when

Do not add a third layout mode: the stable values are exactly `grid` and `list`.
Storage failure or an unavailable browser storage API is a non-fatal preference
miss, not a reason to block the collection.

## Anatomy and variants

The trigger is a compact current-layout icon plus chevron with
`aria-haspopup`, `aria-expanded`, `aria-controls`, and an accessible name that
includes the current mode. Focus is a crisp accent outline and never a glow.
The menu has a visible mono-uppercase Layout label and `role="menu"`; Grid and
List are `role="menuitemradio"` with `aria-checked`. Selection uses the
accent-subtle menu state and no glow.

## Behavior

Click, Enter, and Space select. Closed ArrowDown/ArrowUp opens on the first or
last item; open arrows wrap; Home/End jump; Escape closes and restores trigger
focus. Tab closes after ordinary focus movement without trapping. Pointer or
focus movement outside closes the menu. `layoutPreference.ts` validates stored
values, defaults to `grid`, and treats unavailable or failing browser storage as
a non-fatal preference miss.

## Content

The menu has a visible mono-uppercase `Layout` label. Grid and List are the only
documented choices, and the trigger's accessible name includes the current mode.

## Layout and responsive behavior

The trigger is compact and shows the current-layout icon plus chevron. Its menu
uses the shared layout-selector/menu geometry; selection uses
`accent-subtle` and never glows.

## Accessibility

The trigger exposes `aria-haspopup`, `aria-expanded`, and `aria-controls`. The
menu uses `role="menu"`; Grid and List use `role="menuitemradio"` with
`aria-checked`. Focus is a crisp accent outline and never a glow. The keyboard,
focus-return, and native Tab-exit behavior above is part of the contract.

## Code and evidence

Canonical implementations are [`LayoutSelector.vue`](../../../provider-sdk/portalkit-vue/LayoutSelector.vue)
and [`layoutPreference.ts`](../../../provider-sdk/portalkit-vue/layoutPreference.ts).
Distribution is checked by `make verify-portalkit`.

## Related guidance

See the [dropdown and context menu](menu.md), [control selection and input
patterns](../patterns/controls.md), [resource table](resource-table.md), and
[accessible interaction policy](../accessibility/interaction.md).
