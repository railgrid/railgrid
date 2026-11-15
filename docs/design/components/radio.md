---
{"schema":1,"id":"design.components.radio","title":"Radio","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"The cited implementation is evidence for this review; product-wide adoption has not been audited."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/radio.md","role":"design"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"unverified","checks":[{"kind":"review","ref":"Radio design and product adoption audit","status":"pending"}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# Radio

## Purpose

Choose exactly one option in a visible group.

## Use when

A short set of visible options represents one mutually exclusive choice, such as an environment or connection method.

## Avoid when

Several options may be chosen independently: use checkboxes. For a long list consider a select.

## Anatomy and variants

1. **Group:** a fieldset and legend state the single decision.
2. **Option:** a native radio and its clickable label; each input in the group
   shares a `name` and has a distinct value.
3. **Help or validation:** optional explanation beside the group or choice,
   associated with the relevant control.

Review selected, unselected, focused, required, and disabled states, including
a selected option that later becomes unavailable.

## Behavior

Selecting one option clears the previous choice. For a required group, require
a selection before submission and show a corrective message when validation
fails. Do not silently substitute an enabled choice for an unavailable saved
value; explain the restriction and require an intentional change.

## Content

Phrase the legend as the decision, such as **Connection method**, and label each
option with the value it selects. Describe material differences next to the
options; explain why a choice is unavailable where it appears.

## Layout and responsive behavior

Keep each control and label together and let descriptions wrap. Use a block or
flex label with `.k-checkbox-hit` so the 44px coarse-pointer minimum applies;
do not enlarge the 14px native glyph to create the target. Stack choices when
the available width cannot accommodate them comfortably. Review both themes
and the 390px frame.

## Accessibility

Use native radio inputs in a fieldset with a legend; support keyboard arrow navigation.
The shared `.k-checkbox` recipe also applies to radios and supplies a visible
violet keyboard-focus outline. Use `.k-checkbox-hit` on the associated label
for a larger coarse-pointer target.

## Code and evidence

The [shared recipe](../../../provider-sdk/portalkit/railgrid-ui.css) preserves
native radio behavior. The [radio review](http://127.0.0.1:5197/?entry=design.components.radio)
shows a required group and disabled choices. The [Agents configuration fixture](http://127.0.0.1:5198/?provider=agents&scenario=layout&route=%23%2Fagents%2Fresearch-assistant%2Fconfig)
shows the choice in context. Product-wide adoption and assistive-technology
review remain pending.

The [researcher creation fixture](http://127.0.0.1:5198/?provider=agents&scenario=researcher&route=%23%2Fcreate%2Fagent%2Fresearcher) shows the shared focus outline on a radio card. Arrow-key selection changes the research choice and reveals the specialist field. This fixture uses simulated data.

The [Edge service detail fixture](http://127.0.0.1:5199/service-detail.html)
shows the same native focus treatment in a target selection. Its host-only
sample disables the Kubernetes Service choice and explains why. Its
[host-required scenario](http://127.0.0.1:5199/service-detail.html?scenario=locked-scheme)
also demonstrates the service-type restriction and required host validation;
neither fixture contacts a hub.

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
