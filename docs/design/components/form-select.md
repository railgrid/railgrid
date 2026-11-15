---
{"schema":1,"id":"design.components.form-select","title":"FormSelect","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"FormSelect provides the shared single-select combobox/listbox behavior and viewport-aware teleported panel."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/form-select.md#formselect","role":"design"},{"path":"provider-sdk/portalkit-vue/FormSelect.vue","role":"implementation"},{"path":"provider-sdk/portalkit/form-select.ts","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"command","ref":"vitest run --config hack/design-review/vitest.config.mjs form-select-dialog.test.ts","status":"passing","evidence":"Mounted framework-neutral control keeps its listbox inside an open dialog, supports selection, and restores focus."},{"kind":"browser","ref":"Design review FormSelect dialog fixture","status":"passing","evidence":"Framework-neutral listbox opened in a native modal at available width and in the 390px fixture; Escape closed the listbox and retained dialog focus. This is a synthetic fixture, not a screen-reader or product-wide audit."}]},"relatedDocuments":[{"id":"design.patterns.controls","relation":"implements","path":"docs/design/patterns/controls.md"},{"id":"design.components.menu","relation":"see-also","path":"docs/design/components/menu.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.foundations.recipes","relation":"prerequisite","path":"docs/design/implementation/recipes.md"},{"id":"design.foundations.layering","relation":"see-also","path":"docs/design/implementation/layering.md"}]}
---

# FormSelect

## Purpose

The closed control uses the `.k-input` geometry with a quiet raised-surface
background, a visible focus outline, and a chevron. Native `<select>` popups cannot be
styled; their operating-system popup is sanctioned and `accent-color` themes
what it can.

## Use when

Use `FormSelect.vue` in Vue portals or the framework-neutral
`railgrid-form-select` from `form-select.ts` in the Quickstart portal when a
shared, viewport-aware single-select popup and consistent option presentation are required. Both provide the shared
combobox/listbox behavior instead of relying on a browser popup.

## Avoid when

Use a native `<select>` for short, stable choices when the operating-system popup supports the task, especially for platform-owned touch interaction. Use a searchable picker for long or frequently changing choices after testing the task; search is not a shipped FormSelect capability. Providers must not copy or restyle the shared
`.k-*` recipes.

## Anatomy and variants

1. **Field label and help:** identify the value and any requirement or error.
2. **Trigger:** a combobox button shows the selected value or placeholder and
   a chevron.
3. **Listbox:** a positioned panel contains labeled options; the selected
   option has a checkmark and disabled options remain unavailable.
4. **Validation:** an associated corrective message appears beside an invalid
   control without discarding the draft.

The Vue and framework-neutral implementations compose the shared input and
menu recipes. Providers must not copy or restyle their `.k-*` recipes.

## Behavior

`FormSelect.vue` accepts its typed Vue props and `v-model`. The framework-neutral
element accepts `options`, `value`, and optional `placeholder`, `labelledby`, and
`describedby` properties, then emits one bubbling `change` event whose `detail`
is the selected string. The native `<select>` fallback retains its
operating-system popup behavior.

The Vue component is controlled: selection emits `update:modelValue`, and the
owner must update `modelValue` to display and submit the choice. The
framework-neutral element updates its own `value` before dispatching `change`;
assigning its `value` property programmatically does not dispatch that event.
Synchronize any application state explicitly when setting it in code.

Neither shared control resets its selection when its enclosing form is reset.
The optional named hidden input supplies a submission value, not form-associated
reset behavior. A form with a reset action must restore the Vue model or the
element's `value` itself, and reset its validation state at the same time.

The form owner must validate required FormSelect values, prevent submission
when invalid, set `invalid`, and focus the first invalid control or an error
summary. Pass `errorMessage` for the shared danger border and concise corrective
message; when `invalid` is set, the component associates that message with the
trigger automatically. Use `describedby` for separate help or an externally
rendered error. The `required` prop
announces the requirement; the button and optional hidden input do not provide
native required-field validation. Retain the draft and clear the error after
a valid selection.

Give each option a unique string value. Both implementations use
the value to identify the selected option, and the Vue implementation also
uses it as the rendering key. Duplicate values can mark more than one option
selected while the trigger displays the first matching label.

An empty value may be an explicit option, such as “none” or “pick a machine”;
both implementations display its label and mark it selected when the current
value is empty. They also apply placeholder styling to that value, and the
form receives the same empty string whether it represents an explicit choice
or an unfilled draft. The form owner must interpret and validate that sentinel
according to the task. If those states must be distinguished, use a separate
non-empty value for the choice and translate it at the form boundary.

Options can change while a draft is open. Neither implementation clears or
validates an existing value when its option disappears or becomes disabled.
An unmatched non-empty value is displayed as its raw string and remains in the
optional hidden input; it is not a selected option in the list. A disabled
matching option can still be the retained value. When the task requires a
currently available choice, validate membership and availability against the
current authoritative options before submission, including after a refresh.
Explain an unavailable saved choice and let the user choose a replacement;
do not silently replace the draft with the first option. The first enabled
option is only the initial keyboard target when the retained value is not
selectable, not a new value until the user selects it.

While the list is open, both implementations preserve the active keyboard
option by its unique value across an options update. If it disappears or becomes
disabled, the target falls back to the selected option if enabled, otherwise
the first enabled option. With no enabled option there is no active target.
These updates do not change the saved value. Make live changes clear so the
user can review the available choices before selecting one.

When disabled, both implementations exclude their optional hidden value from
native form submission. Re-enabling restores the retained value. Disabled is
not a substitute for form-owner validation when the control is available.

Give every select a persistent field label. Pass its ID through `labelledby`
when the label is separate; both implementations also support a native
wrapping label. Without `labelledby`, the trigger does not replace the native
label with its selected value. When opened, the listbox refers to the wrapping
label; a generated ID is added to that label if it has none. Provide an
explicit label ID for dynamically composed or complex labels.
Client-rendered Vue app roots use distinct ID prefixes so independent roots
do not point their comboboxes at each other's listboxes. Callers supplying an explicit `id` must keep it unique in the document.

## Content

Name the field and each option in the user's domain, and make a placeholder
describe the expected choice rather than acting as the only label. Explain an
empty list or disabled control beside it. The current component is single
select and does not provide type-ahead or search; validate a separate picker
before standardizing search or multiple selection.

## Layout and responsive behavior

The closed control uses a 4px input, a neutral surface, and a visible focus
outline. A native `select.k-input` uses the same closed surface and focus
treatment while retaining its platform popup. The selected option uses a neutral highlight and checkmark. Listboxes are
teleported and positioned with viewport awareness. Inside an open native dialog, the listbox is hosted inside that dialog so it remains in the browser top layer.

## Accessibility

The shared popup provides single-select combobox/listbox semantics, keyboard
navigation, and disabled-option handling. Enter, Space, or an arrow opens the
list; arrows move among enabled options and wrap, Home and End move to the
first and last enabled options, and Enter or Space selects the active option.
Tab closes the list and continues normal focus navigation. Escape closes an
open listbox and returns focus to its trigger without dismissing an enclosing surface; a later
Escape can reach that surface. Alt+Down opens the list and Alt+Up closes it;
other modified shortcuts are left to the browser. Composition keystrokes are left to the input method rather than opening, changing, or dismissing the control. Use the [accessible interaction
policy](../accessibility/interaction.md) for the cross-surface keyboard, focus,
contrast, touch, and reduced-motion contract.

## Code and evidence

The canonical implementations are [`FormSelect.vue`](../../../provider-sdk/portalkit-vue/FormSelect.vue)
and [`form-select.ts`](../../../provider-sdk/portalkit/form-select.ts), with
shared styling in [`railgrid-ui.css`](../../../provider-sdk/portalkit/railgrid-ui.css).
Distribution is checked by `make verify-portalkit`.

The [select review](http://127.0.0.1:5197/?entry=design.components.form-select)
compares shared and native closed controls, including validation and a disabled
form value. It does not exercise form reset. The [Edge service detail fixture](http://127.0.0.1:5199/service-detail.html)
mounts native selects in context with simulated data; neither fixture verifies
the behavior of every platform's native popup.
Its [locked-scheme scenario](http://127.0.0.1:5199/service-detail.html?scenario=locked-scheme)
shows why a service type fixes the disabled scheme. The service creation
fixture shows the same explanation after selecting UniFi Network.

## Related guidance

See [control selection and input patterns](../patterns/controls.md), the
[dropdown and context menu](menu.md), [accessible interaction policy](../accessibility/interaction.md),
and [shared k-* recipes](../implementation/recipes.md).
