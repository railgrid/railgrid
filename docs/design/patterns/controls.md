---
{"schema":1,"id":"design.patterns.controls","title":"Control selection and input patterns","kind":"pattern","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"partial","notes":"Native controls and PortalKit FormSelect implement current input contracts; slider, date/time, and command-palette specs remain planned."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/patterns/controls.md#control-selection-and-input-patterns","role":"design"},{"path":"provider-sdk/portalkit-vue/FormSelect.vue","role":"implementation"},{"path":"provider-sdk/portalkit/form-select.ts","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Current byte-for-byte PortalKit and AgentKit copy and manifest parity passed."},{"kind":"browser","ref":"Core and provider rendered fixture matrices","status":"passing","evidence":"Six core and six provider dark/light desktop/mobile/hybrid cases passed with actual fonts loaded; input sizing, checkbox glyph, and coarse label hit area were exercised in the provider fixtures."}]},"relatedDocuments":[{"id":"design.components.form-select","relation":"see-also"},{"id":"design.components.layout-selector","relation":"related"},{"id":"design.components.visual-primitives","relation":"related"},{"id":"design.foundations.geometry","relation":"prerequisite"},{"id":"design.foundations.recipes","relation":"prerequisite"},{"id":"design.quality.review-checklist","relation":"see-also"}]}
---

# Control selection and input patterns

The Vue and framework-neutral implementations in the
[FormSelect component](../components/form-select.md) own the product-consistent
single-select popup. For a short, stable set of simple options, prefer a native
select when the platform popup meets the task, especially on touch. Use
FormSelect when a shared, viewport-aware popup and consistent option presentation
matter across providers. Keep native and shared selects visually aligned when
they appear together. Native checkboxes
and radios are the baseline; custom styling is reserved for dense composite
rows and shared toggle recipes.

The shared `.k-input` recipe uses `box-sizing: border-box` and full available
width, keeping borders and padding inside the declared width. Single-line
controls in `.k-create-fields` have a 36px minimum height; dense filters and
textareas retain their own sizing. On coarse-pointer input, `.k-input` supplies
a minimum 44×44px target. A native checkbox keeps its
14×14px glyph through `.k-checkbox`; when a larger touch target is needed,
apply `.k-checkbox-hit` to the associated checkbox or radio label so the label
supplies the minimum 44×44px hit area while the native glyph remains compact.

Fields use a consistent reading order: label, control, then help or validation.
Keep a short required or optional marker with the label; put explanatory copy
below the control and connect it with `aria-describedby`. Use a short label as
the accessible name so a screen reader does not read the explanation twice.
An error may replace the hint or precede it, but both stay below the control.
Adjacent fields align at the top; a longer hint must not move a neighboring
control down. Form-wide instructions can precede a group of fields.

## Validation and recovery

The form owner validates a required FormSelect on submit. Its `required` prop
announces the requirement but does not block submission: the Vue trigger is a
button and its optional hidden value input does not participate in native
constraint validation. Validate both Vue and framework-neutral consumers
explicitly, prevent the operation when invalid, set `invalid` and associate
the corrective message with `errorMessage` or associate an existing message with `describedby`. The shared error message uses a danger border and an icon alongside corrective text. On submission failure, focus an
error summary or the first invalid control. Keep entered values. Clear the
error after a valid selection, and announce the result without relying on
color alone. Native required controls retain browser constraint validation.

For no available options, explain why and offer the next action outside the
popup where possible. Distinguish loading, no matches, and a failed option
read. An unavailable field must say when and how it becomes available.

## Choosing and finding options

Choose the simplest control that supports the task. A native select is a good
baseline for short, stable choices and platform-owned touch interaction.
FormSelect is suitable when the shared popup's presentation or viewport-aware
placement is needed. The current FormSelect supports arrows,
Home, End, Enter, Space, and Escape; it does not implement type-ahead or search.
For a long or frequently changing set, offer a searchable picker with a
keyboard-accessible results list, loading, no-match, and failure states.
The cutoff is task-dependent; test finding a known and an unfamiliar item
rather than enforcing an arbitrary item count. Searchable selection is a
proposal, not a shipped FormSelect capability.

## Grouping choices

Use a native `fieldset` and `legend` for related radio or checkbox choices.
The legend is a field label: match the size, weight, color, and sentence case
of neighboring labels, rather than the uppercase treatment of a section
heading. Put group help after the choices and associate it with the fieldset.
An individual choice can carry a description beneath its title within the
clickable label. Keep the native control aligned with that title.

## Range and date/time input

A native range input with accent color is the current fallback. A custom
slider is a proposal requiring review in both themes and across keyboard and
touch input. Give it an accessible name, visible value, bounds and units,
and a sufficiently large hit target. Do not assume that its thumb must match
the toggle knob; its geometry and focus treatment need a rendered review.

No custom date/time picker is shipped. Use native `date` or `datetime-local`
with the shared input styling when appropriate. A local datetime has no
inherent timezone: show the intended timezone with the field and define how
it is selected and stored. For scheduled instants, validate nonexistent and
ambiguous daylight-saving times and offer a clear correction or offset choice.
Show the timezone and resulting instant in the confirmation. Use two labeled
inputs for a range and validate their order. These scheduling requirements
are proposed guidance to validate with an actual scheduling journey.

## Command discovery

The contextual AgentKit slash palette is shipped for Agents and App Studio;
see the [shared conversation contract](../components/ai-conversation.md#rich-input-ownership).
It is anchored to its composer and selects enabled skills and resource context.

A global application command palette is an unshipped concept. Before choosing
its geometry, define the tasks and scope it covers, an observable way to open
it, platform-appropriate shortcut labels, keyboard focus and dismissal,
empty and error results, and a narrow-screen layout. Review the experience
with keyboard and assistive technology in both themes. Earlier 560px and
`⌘K` values are visual studies rather than a shipped contract.
