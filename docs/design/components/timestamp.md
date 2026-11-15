---
{"schema":1,"id":"design.components.timestamp","title":"Timestamp","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The shared resource and conversation timestamp implementations ship; adoption across all product surfaces is not yet audited."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/timestamp.md#timestamp","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceTimestamp.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/timestamp.ts","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AITimestamp.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"test","ref":"provider-sdk/portalkit-vue/ResourceTimestamp.behavior.test.mjs","status":"passing","evidence":"Formatter tests cover relative labels, invalid instants, daylight-saving ambiguity, and shared clock lifecycle."},{"kind":"test","ref":"hack/design-review/deployment-timestamps.test.ts","status":"passing","evidence":"The mounted synthetic deployment verifies semantic timestamps and exact-time disclosure."},{"kind":"browser","ref":"Agents delivery tester fixture","status":"passing","evidence":"The synthetic tester was reviewed at desktop and 390px in light and dark; keyboard activation revealed the exact time and changing delivery reset the disclosure."},{"kind":"review","ref":"Product-wide timestamp adoption and assistive-technology review","status":"pending"}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# Timestamp

## Purpose

Show when an event happened while retaining its exact time.

## Use when

A record or event has a meaningful time and people benefit from a quick relative label with access to the exact timestamp.

## Avoid when

The value is a duration, a future schedule needing a timezone decision, or missing: use the appropriate dedicated representation.

For consequential future deadlines, such as token expiry or the end of a
deletion recovery window, show the full date, time, and timezone immediately. Use a semantic
`time` element for a valid source instant; state that the deadline is
unavailable when it is invalid rather than displaying an inferred time.

## Anatomy and variants

1. **Relative label:** quiet text gives a quick sense of elapsed time.
2. **Exact time:** hover or focus reveals date, time, and timezone; activation
   keeps it visible inline until toggled again.
3. **Source label:** an accessible name identifies which event the time belongs
   to, such as **Run started**.

`ResourceTimestamp` renders an accessible unavailable marker for either
missing or invalid values; `AITimestamp` omits them. Neither presentation
distinguishes those two cases visually.
Both accept valid absolute RFC3339 timestamps with an explicit offset; an
impossible date or a value without a timezone is unavailable rather than
being interpreted in the viewer's local timezone.

## Behavior

Use the shared formatter and exact-time disclosure rather than presenting an ambiguous relative label alone. Hover and focus reveal the exact time; activation toggles it inline. Exact time uses the browser’s locale and time zone unless the resource component receives an explicit supported locale or time zone. A changed source value closes the inline disclosure.

## Content

Provide a caller-specific label such as “Run started” so assistive technology can identify the event. Do not imply precision or a time zone that the source does not provide.

## Layout and responsive behavior

Keep the relative label compact in rows and metadata, while allowing an
expanded exact value to wrap without widening the page. Avoid overlapping
adjacent actions or clipping the exact-time tooltip near viewport edges. Review
both themes and the 390px frame.

## Accessibility

Expose exact date and timezone on keyboard focus as well as pointer interaction. The accessible button name includes both relative and exact time, and `aria-pressed` reflects whether the exact value is shown inline. Valid resource timestamps retain a semantic `time` element.

## Code and evidence

The [resource component](../../../provider-sdk/portalkit-vue/ResourceTimestamp.vue)
and [conversation component](../../../provider-sdk/agentkit-vue/AITimestamp.vue)
share the exact-time formatter. The [Agents run fixture](http://127.0.0.1:5200/)
includes start, finish, and step timestamps. The
[deployment fixture](http://127.0.0.1:5201/) includes summary, condition, and
event timestamps. Both use synthetic data.

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
