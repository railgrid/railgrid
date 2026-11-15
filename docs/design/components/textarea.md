---
{"schema":1,"id":"design.components.textarea","title":"Textarea","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"The cited implementation is evidence for this review; product-wide adoption has not been audited."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/textarea.md","role":"design"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"browser","ref":"Agents researcher setup fixture","status":"passing","evidence":"In the synthetic fixture, selecting Specialist and submitting an empty area of expertise displays its error and focuses the textarea."},{"kind":"review","ref":"Product-wide textarea and assistive-technology audit","status":"pending"}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# Textarea

## Purpose

Enter or edit longer multi-line text.

## Use when

People need room to write or edit multiple lines, such as agent instructions. Size the initial field for the likely content.

## Avoid when

The answer is a short identifier or a constrained choice: use a text input or selection control.

## Anatomy and variants

1. **Label:** a persistent name for the text and required indicator when needed.
2. **Control:** a native multiline textarea using `.k-input`; its transparent
   surface and border match text inputs, with a vertical resize affordance.
3. **Help or error:** task-specific guidance or a corrective message below
   the control, associated with it.

Review default, focused, disabled, read-only, and error states. An invalid
field keeps its danger border and violet focus outline; a disabled field uses
the overlay surface. Read-only values remain focusable and selectable.

## Behavior

Provide enough initial rows for the task and allow vertical resizing without
overflowing the container. Use native `readonly` for selectable values and
`disabled` only when the field cannot be operated. For custom validation,
associate the error with the field and retain the user's text for correction.

## Content

Name the content, for example **Agent instructions**, and explain the level of
detail or format needed before submission. A placeholder can illustrate the
task but cannot carry its only instructions. State any length or format limit
near the field and make an error explain how to correct the draft.

## Layout and responsive behavior

Choose an initial number of rows appropriate to the task; instructions need
more room than a short description. Keep the label and feedback with the
field, allow vertical resizing, and cap its inline size to the container so it
does not widen the page. The shared control has a 44px minimum target on coarse
pointers. Review both themes and the 390px frame.

## Accessibility

Associate labels and errors, expose required and invalid state, retain visible focus, and distinguish read-only from disabled. When a form uses custom validation, expose the required state without relying on the visual asterisk alone.

## Code and evidence

The [shared stylesheet](../../../provider-sdk/portalkit/railgrid-ui.css) defines
the textarea recipe. Agents uses it for [agent instructions](../../../providers/agents/portal/src/views/AgentConfig.vue)
and [researcher setup](../../../providers/agents/portal/src/views/AgentCreate.vue).
The [textarea specimen](http://127.0.0.1:5197/?entry=design.components.textarea)
shows the editable, read-only, disabled, and error states.
The [Agents researcher fixture](http://127.0.0.1:5198/?provider=agents&scenario=researcher&route=%23%2Fcreate%2Fagent%2Fresearcher)
shows a conditionally required textarea: choose Specialist, then submit an
empty field to review its error and focus. It uses fixture data and is not a
live backend. Product-wide adoption and accessibility review remain pending.

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
