---
{"schema":1,"id":"design.components.form-select","title":"FormSelect","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"FormSelect provides the shared single-select combobox/listbox behavior and viewport-aware teleported panel."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/form-select.md#formselect","role":"design"},{"path":"provider-sdk/portalkit-vue/FormSelect.vue","role":"implementation"},{"path":"provider-sdk/portalkit/form-select.ts","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.controls","relation":"implements","path":"docs/design/patterns/controls.md"},{"id":"design.components.menu","relation":"see-also","path":"docs/design/components/menu.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.foundations.recipes","relation":"prerequisite","path":"docs/design/foundations/recipes.md"}]}
---

# FormSelect

## Purpose

The closed control is exactly `.k-input`: 4px, overlay background, focus ring
and glow, with a 3.5px `text-muted` chevron. Native `<select>` popups cannot be
styled; their operating-system popup is sanctioned and `accent-color` themes
what it can.

## Use when

Use `FormSelect.vue` in Vue portals or the framework-neutral
`faros-form-select` from `form-select.ts` in the Quickstart portal when a
product-consistent single-select popup is required. Both provide the shared
combobox/listbox behavior instead of relying on a browser popup.

## Avoid when

Use a native `<select>` when its operating-system popup is acceptable; native
select popups are sanctioned. Providers must not copy or restyle the shared
`.k-*` recipes.

## Anatomy and variants

The Vue and framework-neutral implementations compose the input and menu
recipes and provide single-select combobox/listbox semantics, keyboard
navigation, disabled options, and viewport-aware teleported listbox positioning.
Providers must not copy or restyle their `.k-*` recipes.

## Behavior

`FormSelect.vue` accepts its typed Vue props and `v-model`. The framework-neutral
element accepts `options`, `value`, and optional `placeholder`, `labelledby`, and
`describedby` properties, then emits one bubbling `change` event whose `detail`
is the selected string. The native `<select>` fallback retains its
operating-system popup behavior.

## Content

This component documents single-select behavior only. If search or multi-select
is needed later, compose a `.k-input` trigger, the shared dropdown panel, and
`.k-badge`-style selected tags; there is no third visual language.

## Layout and responsive behavior

The closed control is exactly `.k-input`: 4px, overlay background, focus ring
and glow, with a 3.5px `text-muted` chevron. Product-consistent listboxes are
teleported and positioned with viewport awareness.

## Accessibility

The shared popup provides single-select combobox/listbox semantics, keyboard
navigation, and disabled-option handling. Use the [accessible interaction
policy](../accessibility/interaction.md) for the cross-surface keyboard, focus,
contrast, touch, and reduced-motion contract.

## Code and evidence

The canonical implementations are [`FormSelect.vue`](../../../provider-sdk/portalkit-vue/FormSelect.vue)
and [`form-select.ts`](../../../provider-sdk/portalkit/form-select.ts), with
shared styling in [`faros-ui.css`](../../../provider-sdk/portalkit/faros-ui.css).
Distribution is checked by `make verify-portalkit`.

## Related guidance

See [control selection and input patterns](../patterns/controls.md), the
[dropdown and context menu](menu.md), [accessible interaction policy](../accessibility/interaction.md),
and [shared k-* recipes](../foundations/recipes.md).
