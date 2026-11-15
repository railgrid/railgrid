---
{"schema":1,"id":"design.components.checkbox","title":"Checkbox","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"The cited implementation is evidence for this review; product-wide adoption has not been audited."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/checkbox.md","role":"design"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"unverified","checks":[{"kind":"review","ref":"Checkbox design and product adoption audit","status":"pending"}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# Checkbox

## Purpose

Choose independent options or select rows.

## Use when

Each option can be enabled independently, or a collection supports selection. Use an indeterminate parent only to describe a partially selected group.

## Avoid when

Only one option can be chosen: use radio buttons or a select. Use a switch for an immediately applied on/off setting.

## Anatomy and variants

1. **Control:** a native checkbox with `.k-checkbox`; its 14px glyph uses the
   theme accent when selected.
2. **Label:** the option's name, associated with the input and clickable with
   it. Use `.k-checkbox-hit` on the label.
3. **Description or error:** optional supporting text beside the choice or its
   group; associate corrective text with the input when it applies.

Review unchecked, checked, indeterminate, focused, and disabled states. A
select-all control also needs a count or other context for the collection it
affects.

## Behavior

Each choice changes independently. For a select-all checkbox, derive checked
from all eligible children and set the native `indeterminate` property when
some, but not all, are selected. It is a visual and accessible summary, not a
third persisted value. Selecting the parent selects or clears the eligible
children; define whether selection covers a page or a whole filtered set.

## Content

Name what is selected, for example **Visualize data** or **All tools**, rather
than describing the checkbox itself. Use a fieldset and legend when choices
share a question. Put the consequence or unavailable reason beside the choice;
do not hide essential explanations in a tooltip.

## Layout and responsive behavior

Keep the input and label together, with descriptions aligned to their label.
Allow long text to wrap without shrinking the glyph. Give the label a block or
flex layout so `.k-checkbox-hit` can provide its 44px minimum target on coarse
pointers; the hook alone does not change an inline label's display. Review
both themes and the 390px frame.

## Accessibility

Use a native checkbox, associate its label, and use Space to toggle. The shared
`.k-checkbox` recipe supplies a visible violet keyboard-focus outline; use
`.k-checkbox-hit` on its label for a larger coarse-pointer target. Explain
unavailable options nearby.

## Code and evidence

The [shared recipe](../../../provider-sdk/portalkit/railgrid-ui.css) preserves
native checked and indeterminate behavior. The [checkbox review](http://127.0.0.1:5197/?entry=design.components.checkbox)
shows a partial select-all and disabled states. The [Agents toolset fixture](http://127.0.0.1:5198/?provider=agents&route=%23%2Fcreate%2Ftoolset)
shows independent choices with descriptions. These use sample data; product-wide
adoption and assistive-technology review remain pending.

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
