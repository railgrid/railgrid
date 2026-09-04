---
{"schema":1,"id":"design.components.create-guidance","title":"CreateGuidance","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"CreateGuidance is the shared guidance rail for route-owned forms; provider copy and value derivation remain local."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/create-guidance.md#createguidance","role":"design"},{"path":"provider-sdk/portalkit-vue/CreateGuidance.vue","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.resource-creation","relation":"implements","path":"docs/design/patterns/resource-creation.md"},{"id":"design.components.first-run-guide","relation":"see-also","path":"docs/design/components/first-run-guide.md"},{"id":"design.content.ui-copy","relation":"prerequisite","path":"docs/design/content/ui-copy.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"}]}
---

# CreateGuidance

## Purpose

When a route-owned form benefits from domain help, place `CreateGuidance.vue`
beside a `k-create-fields` region inside `k-create-surface--guided`. The rail
contains only timely prerequisites, a live and non-secret summary of what Faros
will create, and controller-owned next steps. Provider copy and value derivation
remain local.

## Use when

Use this guidance rail when a route-owned form benefits from domain help. Place
it beside the `k-create-fields` region inside `k-create-surface--guided`.

## Avoid when

Do not turn the rail into a general documentation surface or move provider copy
and value derivation into the shared component. Its content is limited to timely
prerequisites, a live non-secret summary, and controller-owned next steps.

## Anatomy and variants

The guided surface has a `k-create-fields` region beside the
`CreateGuidance.vue` rail. Provider copy and value derivation remain local;
the shared component supplies the guidance-rail structure.

## Behavior

The rail presents a live, non-secret summary of what Faros will create and
controller-owned next steps. It contains timely prerequisites rather than stale
or unrelated help.

## Content

Include only timely prerequisites, a live and non-secret summary of what Faros
will create, and controller-owned next steps. Provider copy and value derivation
remain local.

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
with shared styling in [`faros-ui.css`](../../../provider-sdk/portalkit/faros-ui.css).
Distribution is checked by `make verify-portalkit`.

## Related guidance

See the [resource creation journey](../patterns/resource-creation.md),
[FirstRunGuide](first-run-guide.md), [UI copy policy](../content/ui-copy.md),
and [review checklist](../quality/review-checklist.md).
