---
{"schema":1,"id":"design.components.menu","title":"Dropdown and context menu","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The k-menu recipe is canonical; ActionMenu owns a complete Vue overflow-menu behavior and App Studio pickers mirror the geometry locally."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/menu.md#dropdown-and-context-menu","role":"design"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ActionMenu.vue","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.components.action-menu","relation":"see-also","path":"docs/design/components/action-menu.md"},{"id":"design.foundations.recipes","relation":"implements","path":"docs/design/foundations/recipes.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.patterns.navigation-and-feedback","relation":"see-also","path":"docs/design/patterns/navigation-and-feedback.md"}]}
---

# Dropdown and context menu

## Purpose

The `.k-menu` panel is a 6px `surface-raised` surface with `border-subtle`,
heavy `shadow-2xl`-class elevation, and 4px padding. Items use 4px radius,
6px 8px padding, and 12px secondary text. Hover is `surface-overlay` plus
primary text; active/selected is `accent-subtle` plus accent text and never
glows. Focus is a crisp 2px inset accent outline with no glow. Disabled items
remain visible at reduced opacity and do not retain a hover surface.

## Use when

Use this contract for dropdown and context menus. `ActionMenu` owns a complete
Vue overflow-menu behavior, while App Studio's PreviewActionsMenu,
ResponseModePicker, and ApprovalModePicker follow the same geometry with local
Tailwind classes.

## Avoid when

Do not introduce a provider-local menu visual language. Use the shared `.k-menu`
and `.k-menu-item` vocabulary; active and selected states never glow.

## Anatomy and variants

The panel is a 6px `surface-raised` surface with `border-subtle`, heavy
`shadow-2xl`-class elevation, and 4px padding. Items use 4px radius, 6px 8px
padding, and 12px secondary text. Disabled items remain visible at reduced
opacity and do not retain a hover surface.

## Behavior

Destructive items use danger text, danger-subtle hover, and a hairline divider.
Keyboard behavior is arrows plus Home/End, Escape closes, and focus returns to
the trigger. `ActionMenu` adds roving focus, busy item exclusion, outside
pointer/focus dismissal, and native Tab exit; App Studio's PreviewActionsMenu,
ResponseModePicker, and ApprovalModePicker follow the same geometry with local
Tailwind classes.

## Content

Destructive items use danger text, danger-subtle hover, and a hairline divider.
Selected and active items use accent-subtle plus accent text; they never glow.

## Layout and responsive behavior

The menu panel uses 6px radius, 4px padding, and heavy elevation. Its item
geometry is 4px radius with 6px 8px padding. Local App Studio pickers mirror
this geometry.

## Accessibility

Keyboard behavior is arrows plus Home/End; Escape closes and focus returns to
the trigger. `ActionMenu` additionally owns roving focus, busy-item exclusion,
outside pointer/focus dismissal, and native Tab exit. Preserve these semantics
when using the shared menu contract.

## Code and evidence

The canonical visual recipe is in [`faros-ui.css`](../../../provider-sdk/portalkit/faros-ui.css).
[`ActionMenu.vue`](../../../provider-sdk/portalkit-vue/ActionMenu.vue) provides
the complete shared overflow-menu behavior. Distribution is checked by
`make verify-portalkit`.

## Related guidance

See [ActionMenu](action-menu.md), [shared k-* recipes](../foundations/recipes.md),
[accessible interaction policy](../accessibility/interaction.md), and
[navigation and feedback](../patterns/navigation-and-feedback.md).
