---
{"schema":1,"id":"design.components.confirm-dialog","title":"ConfirmDialog and modal support","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Vue and vanilla helpers mount Railgrid confirmations in the browser's modal top layer."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/confirm-dialog.md#confirmdialog-and-modal-support","role":"design"},{"path":"provider-sdk/portalkit-vue/ConfirmDialog.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/confirm.ts","role":"implementation"},{"path":"provider-sdk/portalkit/modal.ts","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Current byte-for-byte PortalKit and AgentKit copy and manifest parity passed."},{"kind":"browser","ref":"ConfirmDialog scoped interaction fixture","status":"passing","evidence":"Current core and provider fixture cases exercised safe Cancel initial focus, scoped/native Enter, focus trapping and restoration, bounded long-dialog layout, and 44px coarse targets across dark/light desktop/mobile/hybrid cases. Evidence: /tmp/railgrid-design-audit-fixture/current.json and /tmp/railgrid-verify-core-providers-browser-iteration3.log. Focus trapping and restoration were additionally checked in eight Vue/vanilla desktop/mobile light/dark cases: /tmp/design-audit-confirm-focus-final.log."},{"kind":"browser","ref":"Current rendered fixture boundary","status":"passing","evidence":"Six core and six provider dark/light desktop/mobile/hybrid cases passed with actual fonts loaded; this evidence does not establish all routes, live Tilt, or assistive-technology verification."}]},"relatedDocuments":[{"id":"design.patterns.navigation-and-feedback","relation":"see-also","path":"docs/design/patterns/navigation-and-feedback.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.foundations.recipes","relation":"prerequisite","path":"docs/design/implementation/recipes.md"},{"id":"design.components.resource-table-actions","relation":"see-also","path":"docs/design/components/resource-table-actions.md"},{"id":"design.foundations.layering","relation":"see-also","path":"docs/design/implementation/layering.md"}]}
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
confirmation dialog, but it never glows. Both helpers use the shared bounded,
wrapping confirmation geometry.

## Behavior

Use a plain confirmation for most destructive actions. Initial focus is Cancel
for a plain destructive confirmation and Confirm for an informational one.
Use an exact resource name only when an action has particularly consequential,
irreversible effects, such as deleting an Infrastructure instance and the
resources it provisioned. Put focus in the verification field for those
requests. Confirm is unavailable until the name matches exactly.

Cancel, Escape, and backdrop dismissal settle the request and return focus to
the invoking control when it remains available. Replacing a request clears
typed verification and settles the earlier request as cancelled.

## Content

Name the action in both the heading and confirmation button. In the description,
name the resource and what will be removed. For irreversible actions, state
that consequence in a distinct warning before the verification field.

## Layout and responsive behavior

The modal uses the raised surface, a neutral Cancel button and a distinct
destructive action. The scrollable content keeps long names, warnings and
controls within the viewport. Footer actions wrap at narrow widths.

## Accessibility

Native modal dialogs place Vue and vanilla confirmations in the browser's
top layer. Label the explicit Confirm and Cancel actions and associate the
verification field with its instructions. Respect composition when processing
Enter or Escape.

## Code and evidence

Open the [confirmation review](http://127.0.0.1:5197/?entry=design.components.confirm-dialog)
for a plain destructive action, Infrastructure instance deletion, and a vanilla
confirmation. The fixture simulates each outcome locally.

## Related guidance

See [navigation and feedback](../patterns/navigation-and-feedback.md), the
[accessible interaction policy](../accessibility/interaction.md), [shared
k-* recipes](../implementation/recipes.md), and [resource table actions](resource-table-actions.md).
