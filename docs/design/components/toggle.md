---
{"schema":1,"id":"design.components.toggle","title":"Toggle","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"The cited implementation is evidence for this review; product-wide adoption has not been audited."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/toggle.md","role":"design"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"unverified","checks":[{"kind":"review","ref":"Toggle design and product adoption audit","status":"pending"}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# Toggle

## Purpose

Change an independent setting between on and off.

## Use when

A setting has exactly two states and the interface applies the change as a setting. Place the setting label and any disabled explanation beside it.

## Avoid when

The choice is one item in a form that is submitted later: prefer a checkbox, unless the surrounding form already uses an established setting pattern.

## Anatomy and variants

1. **Label:** a stable name for the setting, independent of its current state.
2. **Track and knob:** `.k-toggle` is 36 × 20px with a 14px knob; the accent
   track and knob position indicate on. Focus uses a violet outline.
3. **Context:** optional description, unavailable reason, or save feedback
   remains beside the setting.

Review on, off, focused, disabled, pending, and failed-save states.

## Behavior

Make the current setting evident; explain disabled settings next to the control. The consumer owns saving and failure recovery. If a switch stays focusable while a save is pending, expose its busy and unavailable state, block repeat activation, and keep the last confirmed value until the save succeeds. On failure, retain that value and show an actionable error.

## Content

Use a stable noun or action phrase such as **Notifications**; avoid changing
the label from “Enable” to “Disable” as the state changes. Explain what the
setting controls and why it cannot be changed when that is not obvious. A save
error should say what failed and how to retry.

## Layout and responsive behavior

Keep the label and explanation adjacent to the switch; let the text wrap
without shrinking the track. Provide a 44px hit area around the compact visual
track and avoid overlapping neighboring controls. Stack or wrap the row on
narrow screens. Review both themes and the 390px frame.

## Accessibility

Use a native checkbox with `role="switch"` or a button with `role="switch"`
and `aria-checked`; associate the visible label and any explanatory text. If
the native input uses a separate `.k-toggle` track, mark the track decorative
and synchronize its `aria-checked` styling with the input. A button switch must
support Space and Enter. A focusable busy control needs `aria-disabled` and a
guard in its handler; `aria-disabled` alone does not stop activation. Respect
reduced motion and do not rely on color alone to show the current value.

## Code and evidence

The [shared stylesheet](../../../provider-sdk/portalkit/railgrid-ui.css) defines the switch track. The [Workspace assistant fixture](http://127.0.0.1:5198/?provider=agents&scenario=assistant&route=%23%2Fassistant-settings) shows a disabled setting with an explanation; the [Edge detail fixture](http://127.0.0.1:5199/detail.html) shows the native harness switch using the same track. Its [failed-save scenario](http://127.0.0.1:5199/detail.html?scenario=harness-failure) exercises recovery. Both use simulated data and do not contact a live backend. Product-wide adoption and assistive-technology review remain pending.

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
