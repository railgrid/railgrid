---
{"schema":1,"id":"design.components.field-group","title":"Field group","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"Uses native grouping and existing product styles; there is no shared FieldGroup component."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/field-group.md#field-group","role":"design"},{"path":"providers/agents/portal/src/views/Toolsets.vue","role":"implementation"},{"path":"providers/agents/portal/src/style.css","role":"implementation"}],"verification":{"state":"unverified","checks":[{"kind":"review","ref":"Field group product adoption and accessible descriptions","status":"pending"}]},"relatedDocuments":[{"id":"design.patterns.controls","relation":"implements"},{"id":"design.components.checkbox","relation":"related"},{"id":"design.components.radio","relation":"related"},{"id":"design.accessibility.interaction","relation":"see-also"}]}
---

# Field group

## Purpose

Make related choices understandable as one question while keeping the explanation of each choice beside it.

## Use when

Checkboxes or radio buttons answer a shared question, such as which tools belong to a toolset. Use a native fieldset and legend to give the choices a shared name.

## Avoid when

Fields are unrelated or merely happen to appear on the same page. A layout container or section heading does not replace a fieldset for a semantic group of choices.

## Anatomy and variants

1. **Legend:** names the shared question or decision.
2. **Choices:** each native control has a clickable label and may have a short
   description specific to that option.
3. **Group help or error:** optional guidance applies to the whole set and is
   associated with the fieldset.

Radio groups allow one selection; checkbox groups allow independent selections.
There is no distributed `FieldGroup` component: the product owns composition
using native grouping and existing control recipes.

## Behavior

Keep a choice's entire label clickable. Preserve the natural keyboard behavior of native checkboxes and radios. The form owns validation, state changes, and disabled reasons; a fieldset does not itself perform validation or persist a selection.

## Content

Write a concise legend as a field label, not an uppercase section heading. Explain the group once; avoid repeating the same help under each choice. Put details specific to a choice beside that choice. If the available choices are unavailable or failed to load, describe the state and recovery separately.

## Layout and responsive behavior

Align controls with the first line of the choice label. Let descriptions wrap without separating them from their controls; keep the legend, choices, and group help together at narrow widths. Use local spacing instead of nesting a decorative card around every choice.

## Accessibility

Use a native `fieldset` and `legend`. Associate group help or a group error with `aria-describedby`, and associate choice-specific help with its control or label. Do not use color alone for an error or unavailable state. Review keyboard operation and assistive-technology announcements in the actual form.

## Code and evidence

The [Agents toolset form](../../../providers/agents/portal/src/views/Toolsets.vue) provides a product example. Its styles are provider-local; this page does not claim a distributed FieldGroup component. The [field-group specimen](http://127.0.0.1:5197/?entry=design.components.field-group) uses the same shared checkbox recipe, and the [toolset product fixture](http://127.0.0.1:5198/?provider=agents&route=%23%2Fcreate%2Ftoolset) uses sample data and no live backend.

In the 390px specimen with a simulated coarse pointer, the checkbox glyph
measured 14px and its label target 44px high. This is fixture evidence, not a
real-device or product-wide accessibility audit.

The Agents configuration form also associates its harness-specific autonomy
explanation with the radio fieldset when those choices are restricted. The
association is conditional: a normal model-backed agent does not reference a
hint that is absent.

In the toolset editor, existing assignments missing from the connection list
remain visible. Saving waits until the list is available and the user removes
any assignment that no longer resolves. This prevents an incomplete list from
silently changing the derived tool families.
If a refresh removes the focused connection choice, focus returns to the Tools
group so the newly unavailable assignment can be reviewed. The [refresh
fixture](http://127.0.0.1:5198/toolset-refresh.html) simulates this transition
in the actual editor. A browser check confirmed the focused group, visible
assignment, and disabled save after refresh; assistive-technology and live
backend behavior remain unverified.
The [unavailable-assignment fixture](http://127.0.0.1:5198/?provider=agents&scenario=toolset-missing-connection&route=%23%2Ftoolsets%2Fresearch%2Fedit)
uses simulated data to show that recovery path.

In Agents Tools & toolsets, a shared explanation is associated with the
toolset, built-in capability, and direct-tool groups: granting background
access first requires chat access. An existing background-only grant remains
available to remove. Explain both the prerequisite and the recovery path
where a control can be disabled for new selections but still enabled to undo
an existing one.

For tokenized template lists, put the invalid border on the enclosing field
and associate the inline error with the input. The [News reporter fixture](http://127.0.0.1:5198/?provider=agents&scenario=news-template&theme=light&route=%23%2Fcreate%2Fagent%2Fnews-reporter)
uses sample data: select the sample channel and submit without topics to
review the error, then type a topic to clear it. A 320px touch-emulated review
also checked long-token wrapping, removal focus, and 44px action targets. It
does not create an agent or verify a live channel.

## Related guidance

See [control patterns](../patterns/controls.md), [checkbox](checkbox.md), [radio](radio.md), and the [interaction policy](../accessibility/interaction.md).
