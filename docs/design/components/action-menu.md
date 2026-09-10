---
{"schema":1,"id":"design.components.action-menu","title":"ActionMenu","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The current Vue implementation owns the overflow trigger, roving menu focus, dismissal, and typed selection event; callers own mutations."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/action-menu.md#actionmenu","role":"design"},{"path":"provider-sdk/portalkit-vue/ActionMenu.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ActionMenu.conformance.test.mjs","role":"reference"}],"verification":{"state":"verified","checks":[{"kind":"test","ref":"provider-sdk/portalkit-vue/ActionMenu.conformance.test.mjs","status":"passing"},{"kind":"browser","ref":"Core and provider rendered fixture matrices","status":"passing","evidence":"Six core and six provider dark/light desktop/mobile/hybrid cases passed; ActionMenu viewport placement, focus exit, and coarse targets were exercised in the shared fixtures."}]},"relatedDocuments":[{"id":"design.components.menu","relation":"see-also","path":"docs/design/components/menu.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.foundations.recipes","relation":"prerequisite","path":"docs/design/foundations/recipes.md"}]}
---

# ActionMenu

## Purpose

`ActionMenu` is the compact overflow trigger for a caller-owned set of actions.
It emits `select(id)`; it does not perform mutations, confirmation, routing, or
resource deletion. Callers own the resulting action.

## Use when

Use it for a compact set of caller-owned overflow actions. The caller supplies
a non-empty accessible `label` and readonly items with `id`, `label`, optional
tone (`neutral`, `accent`, `warning`, or `danger`), `disabled`, and `busy` state.

## Avoid when

Do not make `ActionMenu` responsible for a mutation, confirmation, route
transition, or resource deletion. Use the [shared menu contract](menu.md) for
the visual and interaction vocabulary when a different menu owner is needed.

## Anatomy and variants

The trigger is a 32px `.k-icon-action` with a Lucide `Ellipsis` icon, a
`data-k-tip` label, and linked `aria-controls`, `aria-haspopup="menu"`, and
`aria-expanded` state. `showLabel` adds the visible trigger label while keeping
the same action semantics. The menu is a `.k-menu` with `role="menu"`; each
item is a `role="menuitem"` button with a stable roving `tabindex`,
`aria-disabled`, and `aria-busy`. Busy items show `Loader2`, are disabled, and
are not selectable. A divider separates the first contiguous danger group from
preceding non-danger items.

## Behavior

Keyboard behavior is complete and deterministic: closed `ArrowDown`, Enter, or
Space opens on the first selectable item; closed `ArrowUp` opens on the last.
Open ArrowUp/ArrowDown wrap over selectable items; Home/End jump to the first or
last; Enter, Space, and Spacebar select. Escape closes and restores trigger
focus. When Tab is pressed, the teleported menu closes and synchronously returns
focus to its own trigger before the browser performs native adjacent focus
movement. Pointer-down and focus-in outside the root close without stealing
focus. A disabled trigger closes the menu and unmount cleanup removes document
listeners.

Selection ignores disabled, busy, or missing IDs, closes first, restores trigger
focus, and then emits the ID. Changes to the item list repair an invalid active
index while the menu is open.

## Content

The caller supplies the non-empty accessible trigger label and item IDs and
labels. Item tone is limited to `neutral`, `accent`, `warning`, or `danger`;
busy items use the loading state rather than becoming selectable actions.

## Layout and responsive behavior

The menu is teleported to `body` and positioned by the shared
`useAnchoredPopover` composable. Geometry stays within viewport margins, flips
above the trigger when needed, updates on resize/scroll and panel measurement,
and retains a minimum width of 180px; its tooltip is right-anchored and
suppressed while open.
The menu uses the [shared menu recipe](../foundations/recipes.md) and never
glows.

## Accessibility

The trigger exposes linked `aria-controls`, `aria-haspopup="menu"`, and
`aria-expanded` state. Menu items use `role="menuitem"`, stable roving
`tabindex`, `aria-disabled`, and `aria-busy`. The keyboard, dismissal, and focus
return rules above are part of the component contract; native Tab movement is
not trapped after the component synchronously restores the trigger focus.

## Code and evidence

The canonical implementation is [`ActionMenu.vue`](../../../provider-sdk/portalkit-vue/ActionMenu.vue),
using [`useAnchoredPopover.ts`](../../../provider-sdk/portalkit-vue/useAnchoredPopover.ts),
with behavior checked by
[`ActionMenu.conformance.test.mjs`](../../../provider-sdk/portalkit-vue/ActionMenu.conformance.test.mjs).

## Related guidance

See the [dropdown and context menu](menu.md), [accessible interaction
policy](../accessibility/interaction.md), and [shared k-* recipes](../foundations/recipes.md).
