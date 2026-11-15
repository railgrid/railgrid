---
{"schema":1,"id":"design.components.layout-selector","title":"LayoutSelector","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"LayoutSelector is a controlled two-mode segmented selector with storage as a non-fatal preference."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/layout-selector.md#layoutselector","role":"design"},{"path":"provider-sdk/portalkit-vue/LayoutSelector.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/layoutPreference.ts","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Current byte-for-byte PortalKit and AgentKit copy and manifest parity passed."},{"kind":"browser","ref":"Core and provider rendered fixture matrices","status":"passing","evidence":"Six core and six provider dark/light desktop/mobile/hybrid cases passed with actual fonts loaded; this is shared fixture evidence, not a dedicated LayoutSelector interaction case."}]},"relatedDocuments":[{"id":"design.components.menu","relation":"see-also","path":"docs/design/components/menu.md"},{"id":"design.patterns.controls","relation":"see-also","path":"docs/design/patterns/controls.md"},{"id":"design.components.resource-table","relation":"see-also","path":"docs/design/components/resource-table.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"}]}
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

Both options remain visible in a compact segmented radio group. Each option
has an icon and a short label; the selected option has a quiet surface and
border. Focus uses an accent outline without a glow.

## Behavior

Click or Space selects an option. Arrow keys move focus and select the next
option; Home and End select the first and last. Tab enters the selected option
and moves out of the group normally. `layoutPreference.ts` validates stored
values and treats unavailable storage as a non-fatal preference miss.

Keyboard events from an active input-method composition do not change the
layout or move focus. Modified navigation keys remain available to the browser
and assistive technology.

## Content

The defaults are Grid and List. Callers can set `gridLabel` (such as Cards or
Board) and `gridIcon`; emitted values remain `grid` and `list`.

## Layout and responsive behavior

Keep both labeled choices visible. On coarse pointers each option has a minimum
44-by-44px target. Toolbars may wrap the group to preserve readable controls.

## Accessibility

The labeled `radiogroup` contains two `radio` buttons with `aria-checked` and a
single Tab stop. Arrow keys, Home, and End move selection and focus; labels are
visible and icons are decorative.

## Code and evidence

Canonical implementations are [`LayoutSelector.vue`](../../../provider-sdk/portalkit-vue/LayoutSelector.vue),
[`layoutPreference.ts`](../../../provider-sdk/portalkit-vue/layoutPreference.ts).
Review the segmented control in the [design book](http://127.0.0.1:5197/?entry=design.components.layout-selector)
and in the [Agents collection fixture](http://127.0.0.1:5198/?provider=agents&scenario=layout&route=%23%2Fagents).
Both use sample data. In the design-book fixture, Arrow Right selected List and
moved focus to it; Shift+Arrow Left left the selection and focus unchanged.
This keyboard check does not cover assistive technology or a real touch device.
Distribution is checked by `make verify-portalkit`.

## Related guidance

See the [dropdown and context menu](menu.md), [control selection and input
patterns](../patterns/controls.md), [resource table](resource-table.md), and
[accessible interaction policy](../accessibility/interaction.md).
