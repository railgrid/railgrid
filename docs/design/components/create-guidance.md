---
{"schema":1,"id":"design.components.create-guidance","title":"CreateGuidance","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"CreateGuidance is the shared guidance rail for route-owned forms; provider copy and value derivation remain local."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/create-guidance.md#createguidance","role":"design"},{"path":"provider-sdk/portalkit-vue/CreateGuidance.vue","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.resource-creation","relation":"implements","path":"docs/design/patterns/resource-creation.md"},{"id":"design.components.first-run-guide","relation":"see-also","path":"docs/design/components/first-run-guide.md"},{"id":"design.content.ui-copy","relation":"prerequisite","path":"docs/design/content/ui-copy.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"}]}
---

# CreateGuidance

## Purpose

When a route-owned form benefits from domain help, place `CreateGuidance.vue`
beside a `k-create-fields` region inside `k-create-surface--guided`. The rail
contains only information that helps the user decide or prepare before submitting,
such as an essential prerequisite or a consequential outcome. Provider copy and
value derivation remain local.

When a displayed value previews a transformation of the draft, calculate it
from the value that will actually be submitted. For example, the [Edge service
creation fixture](http://127.0.0.1:5199/product.html?view=create) previews a
pasted URL's scheme and port before the host field loses focus; invalid URLs
show a correction prompt instead of a misleading endpoint.

## Use when

Use this guidance rail when a route-owned form benefits from domain help. Place
it beside the `k-create-fields` region inside `k-create-surface--guided`.

## Avoid when

Do not turn the rail into a general documentation surface or move provider copy
and value derivation into the shared component. Do not repeat form values or list
routine post-creation steps merely to fill the rail. Put field-specific format,
reachability, and validation help beside the relevant control.

## Anatomy and variants

The guided surface has a `k-create-fields` region beside the
`CreateGuidance.vue` rail. Provider copy and value derivation remain local;
the shared component supplies the guidance-rail structure.

## Behavior

Optional prerequisites, non-secret values, and next steps appear only when they
help a decision in the current flow. Recovery flows may use next steps when those
steps are the action available now.

## Content

Explain a prerequisite or consequence once, using concise provider-owned copy.
Never echo secrets. Keep field-specific help with its field and associate it
with the control for assistive technology.

## Layout and responsive behavior

The shared container query places fields before guidance on narrow surfaces and
keeps both regions fluid at desktop and 4K widths. See the [resource creation
pattern](../patterns/resource-creation.md) for choosing this surface and the
canonical footer.

## Accessibility

No standalone keyboard model applies: `CreateGuidance` is a guidance rail, not
an interactive composite control. Keep its content within the owning form's
accessible structure and preserve the form's control labels.

## Code and evidence

The canonical component is [`CreateGuidance.vue`](../../../provider-sdk/portalkit-vue/CreateGuidance.vue),
with shared styling in [`railgrid-ui.css`](../../../provider-sdk/portalkit/railgrid-ui.css).
Distribution is checked by `make verify-portalkit`.

The Agents [toolset](http://127.0.0.1:5198/?provider=agents&route=%23%2Fcreate%2Ftoolset)
and [webhook](http://127.0.0.1:5198/?provider=agents&route=%23%2Fcreate%2Fwebhook)
fixtures keep the rail to the access or setup consequence. Field-specific help
stays with the controls, without repeating the entered values. Both use
simulated data.

The [Edge connection fixture](http://127.0.0.1:5199/wizard.html) keeps the
installation prerequisite in the rail and places name and label guidance
beside the fields. The later installation step retains its command and masked
token flow. The fixture uses simulated data.

The [Edge workload fixture](http://127.0.0.1:5199/product.html?view=workload-create)
keeps the derived placement estimate while leaving field values and validation
beside their controls. Singleton distinguishes one possible placement from the
number of matching edges; the preview is based on the currently loaded edges.

## Related guidance

See the [resource creation journey](../patterns/resource-creation.md),
[FirstRunGuide](first-run-guide.md), [UI copy policy](../content/ui-copy.md),
and [review checklist](../quality/review-checklist.md).
