---
{"schema":1,"id":"design.components.confirm-dialog","title":"ConfirmDialog and modal support","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Vue and vanilla confirmation helpers mount the shared in-page dialog and replace native dialogs."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/confirm-dialog.md#confirmdialog-and-modal-support","role":"design"},{"path":"provider-sdk/portalkit-vue/ConfirmDialog.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/confirm.ts","role":"implementation"},{"path":"provider-sdk/portalkit/modal.ts","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.navigation-and-feedback","relation":"see-also","path":"docs/design/patterns/navigation-and-feedback.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.foundations.recipes","relation":"prerequisite","path":"docs/design/foundations/recipes.md"},{"id":"design.components.resource-table-actions","relation":"see-also","path":"docs/design/components/resource-table-actions.md"}]}
---

# ConfirmDialog and modal support

## Purpose

Use the promise-based `confirmDialog()` plus one root `<ConfirmDialog />` in Vue,
or `confirmModal()`/`alertModal()` from vanilla PortalKit. Native
`window.confirm` and `window.alert` are forbidden. The caller owns the
destructive action and awaits a boolean result; row delete actions pass
`danger: true`.

## Use when

Use the shared promise-based helpers whenever an in-page confirmation or alert
is needed. Vue consumers mount one root `<ConfirmDialog />`; vanilla consumers
use `confirmModal()` or `alertModal()` from PortalKit.

## Avoid when

Never use native `window.confirm` or `window.alert`. Do not move the destructive
mutation into the dialog: the caller owns the action and awaits the boolean
result.

## Anatomy and variants

Vue uses `confirmDialog()` with one root `<ConfirmDialog />`; vanilla PortalKit
uses `confirmModal()`/`alertModal()`. Solid danger is allowed inside a
confirmation dialog, but it never glows.

## Behavior

The caller awaits a boolean result and owns the destructive action. Row delete
actions pass `danger: true`.

## Content

Use confirmation or alert copy appropriate to the caller-owned action. The
dialog's danger treatment is reserved for the destructive confirmation action;
the solid danger treatment never glows.

## Layout and responsive behavior

Dialogs and modals are 6px, `surface-raised`, and hairlined with heavy elevation.
Their scrim derives from `surface` (`bg-surface/60` or
`color-mix(in srgb, var(--color-surface) 60%, transparent)`), never from text or
`bg-black/*`, so dark and light themes remain dark-on-dark and light-on-light.

## Accessibility

Use the shared in-page dialog so the interaction has the common dialog
semantics and focus behavior; native browser dialogs are not part of this
contract. The caller's action remains available through the promise result.

## Code and evidence

Canonical implementations are [`ConfirmDialog.vue`](../../../provider-sdk/portalkit-vue/ConfirmDialog.vue),
[`confirm.ts`](../../../provider-sdk/portalkit-vue/confirm.ts), and
[`modal.ts`](../../../provider-sdk/portalkit/modal.ts). Distribution is checked
by `make verify-portalkit`.

## Related guidance

See [navigation and feedback](../patterns/navigation-and-feedback.md), the
[accessible interaction policy](../accessibility/interaction.md), [shared
k-* recipes](../foundations/recipes.md), and [resource table actions](resource-table-actions.md).
