---
{"schema":1,"id":"design.components.tokenized-list-input","title":"Tokenized list input","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"Agents owns this template-specific component; it is not yet a distributed PortalKit primitive."},"appliesTo":["agents"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/tokenized-list-input.md#tokenized-list-input","role":"design"},{"path":"providers/agents/portal/src/components/AgentTemplateListInput.vue","role":"implementation"},{"path":"providers/agents/portal/src/style.css","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentCreate.vue","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"News reporter product fixture","status":"passing","evidence":"A 320px touch-emulated fixture checked long-token wrapping, remove focus and the empty required-list error. It did not create an agent or verify assistive technology."},{"kind":"review","ref":"Cross-product and assistive-technology review","status":"pending"}]},"relatedDocuments":[{"id":"design.components.field-group","relation":"related"},{"id":"design.components.text-input","relation":"related"},{"id":"design.patterns.resource-creation","relation":"related"}]}
---

# Tokenized list input

## Purpose

Collect a short list of values while making each entry visible and removable.

## Use when

A form asks for multiple independent items, such as topics for an agent or repositories to review, and users may paste several lines at once.

## Avoid when

The value is prose, a single scalar, or a large dataset better edited in a table. Use a textarea for multiline prose; do not imply the component validates or deduplicates entries.

## Anatomy and variants

The enclosing input surface contains zero or more tokens, each with a named remove button, followed by a labeled text input. Help and an optional error sit outside the surface. The form owns required, disabled, and invalid states. The enabled surface is transparent; the disabled surface uses the overlay and secondary-text roles, consistent with text inputs.

## Behavior

Typing adds a pending value to the form. Enter or blur commits it as a token; pasting multiple lines creates one token per nonempty line. Removing a token returns focus to the input. The form validates submitted values and clears a field's prior error when it changes. The component does not save or submit data itself.

The `modelValue` contract is a newline-delimited string, not an array. Each
emission joins committed entries and the trimmed pending text with `\n`, so a
form receives the current draft before Enter or blur. Incoming values are split
on `\n`, trimmed, and empty lines discarded; duplicates remain. A multiline
paste first commits any pending text, then appends each trimmed, nonempty pasted
line as a separate entry, regardless of the input's caret position. A single-line
paste follows normal text-input behavior. Form owners should parse and validate
the emitted string as a list and must not depend on preserving whitespace or
blank entries.

## Content

Name the items in the label and provide an example in the placeholder. Explain Enter and multiline paste near the field. Remove buttons name the item they remove; error text states the correction needed.

## Layout and responsive behavior

Tokens wrap within the input surface and long unbroken values wrap without pushing the page horizontally. On coarse pointers, remove actions need at least 44-by-44-pixel targets. Keep the input available after the tokens so another value can be added.

## Accessibility

Associate the label, help, required state, and error with the text input. When the input is focused, outline the enclosing surface with the same 2px violet ring and offset as other text inputs. Apply the invalid border to the enclosing surface and include a text error; do not rely on color alone. Preserve keyboard entry and focus after removal. Screen-reader and physical-device behavior remain to be reviewed.

## Code and evidence

The product implementation is [`AgentTemplateListInput.vue`](../../../providers/agents/portal/src/components/AgentTemplateListInput.vue), composed by [`AgentCreate.vue`](../../../providers/agents/portal/src/views/AgentCreate.vue). The [News reporter fixture](http://127.0.0.1:5198/?provider=agents&scenario=news-template&theme=light&route=%23%2Fcreate%2Fagent%2Fnews-reporter) uses sample data and does not create an agent. The [design-book specimen](http://127.0.0.1:5197/?entry=design.components.tokenized-list-input) mounts the same component with local state.

## Related guidance

See [field groups](field-group.md), [text input](text-input.md), and [resource creation](../patterns/resource-creation.md).
