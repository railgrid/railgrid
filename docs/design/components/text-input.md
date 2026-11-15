---
{"schema":1,"id":"design.components.text-input","title":"Text input","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"The cited implementation is evidence for this review; product-wide adoption has not been audited."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/text-input.md","role":"design"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"unverified","checks":[{"kind":"review","ref":"Text input design and product adoption audit","status":"pending"}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# Text input

## Purpose

Enter a short value.

## Use when

The answer is a short, editable value such as a connection name. Keep a persistent label and provide validation close to the field.

## Avoid when

The answer is multiple lines, a fixed choice, or a search query: use a textarea, select or radio group, or search input respectively.

## Anatomy and variants

1. **Label:** a persistent name, with a required indicator when applicable.
2. **Control:** a native single-line input using `.k-input`; its transparent
   surface, subtle border, and 4px radius fit the surrounding material.
3. **Help or error:** optional field-specific guidance below the control,
   associated with it through `aria-describedby`.

Review default, focused, disabled, read-only, and error states. Disabled inputs
use the overlay surface; read-only values remain selectable.

## Behavior

Retain user input on validation failure. A focused text input uses a 2px violet
outline with a 2px offset. An invalid input uses a danger border and keeps the
violet focus outline when focused. Mark an invalid value with `aria-invalid`
and associate the visible error. The form owns validation timing and, on an
invalid submission, should bring the first actionable error into focus. Use
`readonly` when a value must remain selectable; use `disabled` when the control
cannot be operated.

## Content

Name the value, such as **Connection name**, rather than the interaction.
Placeholders may illustrate a format but never replace the label. Put format
requirements beside the field before submission; an error should say what to
change without clearing the entered value. Mark required fields consistently
and avoid repeating group-level guidance.

## Layout and responsive behavior

Keep the label, input, help, and error in reading order. Let the input fill its
available column without forcing the page wider; wrap help and errors. The
shared recipe uses 8px vertical and 12px horizontal padding, with a 44px
minimum input target on coarse pointers. Review both themes and the 390px frame.

## Accessibility

Associate the label and help; expose required and invalid state to assistive
technology. Use an appropriate native input type and autocomplete or input
mode where known. Do not rely on border color alone to convey an error, and
preserve the visible violet keyboard-focus outline.

## Code and evidence

The [shared stylesheet](../../../provider-sdk/portalkit/railgrid-ui.css) owns the visual recipe. The [Agents connection form](../../../providers/agents/portal/src/views/Connections.vue) uses it for names and credentials, with associated help and disabled states. This is an observed consumer, not a product-wide adoption audit. Open the [Agents connection product fixture](http://127.0.0.1:5198/?provider=agents&route=%23%2Fcreate%2Fconnection%2Fgithub) for context; it uses fixture data and is not a live backend.

The [text-input specimen](http://127.0.0.1:5197/?entry=design.components.text-input)
shows the shared states and field anatomy alongside a deployment map editor and
a required enum with no default. The enum starts with an explicit prompt and
preserves the schema value's type when selected. The map remove action's
fallback tooltip stays within the narrow fixture on focus and hover.

The [Edge service creation fixture](http://127.0.0.1:5199/product.html?view=create) mounts the product form with synthetic Edge data. Required labels and native required state follow the selected target and service type. An invalid port retains its value, focuses the field on submit, and does not produce a fabricated endpoint preview.

For a service type with a fixed scheme, a pasted URL with a conflicting scheme
stays in the host field with an associated correction message; creation is
blocked until corrected. A malformed pasted URL cannot silently become the
agent-loopback endpoint. The fixture shows the corrected host and port in the
endpoint preview. It does not create a live service.
Service proxies use the root endpoint, so a URL containing a path, query,
fragment, or embedded credentials stays in the field with a correction message
instead of silently dropping those parts. Configure credentials separately.
The [service detail fixture](http://127.0.0.1:5199/service-detail.html?scenario=locked-scheme)
uses the same URL parsing and scheme validation for edits. On an invalid save,
the host retains its value and receives focus; a valid URL fills the separate
host, scheme, and port fields. Both fixtures use simulated data.

The [Edge workload creation fixture](http://127.0.0.1:5199/product.html?view=workload-create) shows required and optional inputs with a synthetic Kubernetes edge. Invalid replica counts receive associated inline guidance and block creation without silently replacing them with a default.
The replica control keeps a stable accessible name when its error appears;
the corrective message is its description rather than part of the label.

The [Edge connection fixture](http://127.0.0.1:5199/wizard.html) retains
malformed or duplicate label pairs and shows an associated correction instead
of silently dropping or overwriting them. Valid Kubernetes label keys and
values, including empty values, remain supported. It uses simulated data.
The Edge name field also explains its allowed format and blocks an invalid name
before creation, while retaining the entered value for correction.
The workload selector uses the same label key and value rules; unlike an Edge
label, a selector pair requires a nonempty value. An invalid selector leaves
the placement preview unavailable until corrected.

The [text-input review](http://127.0.0.1:5197/?entry=design.components.text-input) also mounts the Infrastructure deployment form's map editor with synthetic values. Map validation identifies the first offending key or value, associates its error with that control, and focuses it on submission without marking valid neighbors invalid; the fixture does not contact a backend.

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
