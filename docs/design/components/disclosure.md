---
{"schema":1,"id":"design.components.disclosure","title":"Disclosure","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"The cited implementation is evidence for this review; product-wide adoption has not been audited."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/disclosure.md","role":"design"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"unverified","checks":[{"kind":"review","ref":"Disclosure design and product adoption audit","status":"pending"}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# Disclosure

## Purpose

Reveal optional supporting details in place.

## Use when

Supporting technical or advanced details are optional to the primary task and can be revealed in place.

## Avoid when

A user needs the information to understand the current failure or decide the next action: leave that information visible.

## Anatomy and variants

1. **Summary:** a concise label naming the hidden content.
2. **Context and chevron:** optional closed-state context helps decide whether
   to open it; a chevron indicates expansion.
3. **Body:** supporting content appears in place when opened.

The shared recipe has a bordered contextual variant and a compact, borderless
diagnostic variant. Independent disclosures may be open together.

## Behavior

Keep essential errors and recovery visible while collapsed. Open and close in
place without navigating or changing the underlying resource. A chevron turns
with the state; reduced motion removes its transition.

## Content

Name the content, such as **Arguments and result** or **Technical details**,
rather than a vague “More.” Use a short preview when it helps scanning, but
keep the actual failure and next action outside the collapsed region.

## Layout and responsive behavior

Let the summary and context wrap while keeping the chevron visible. Long
technical content should wrap or scroll within its own region rather than
widening the page. Shared summaries grow to a 44px minimum height on coarse
pointers. Review both themes and the 390px frame.

## Accessibility

Use native `details` and `summary` where possible so the browser exposes the
expanded state and keyboard activation. The label names what opens, the
chevron is decorative, and keyboard focus remains visible. Do not put
independent interactive controls inside the summary.

## Code and evidence

The [shared recipe](../../../provider-sdk/portalkit/railgrid-ui.css) provides
both variants. The [disclosure specimen](http://127.0.0.1:5197/?entry=design.components.disclosure)
compares them; the [Agent run fixture](http://127.0.0.1:5200/) shows disclosures
in context with sample data. Product-wide adoption and assistive-technology
review remain pending.

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
