---
{"schema":1,"id":"design.components.digest-schedule","title":"Digest schedule","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"shipped","notes":"Agents owns this news reporter template editor; it is not a general PortalKit date or recurrence picker."},"appliesTo":["agents"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/digest-schedule.md#digest-schedule","role":"design"},{"path":"providers/agents/portal/src/components/AgentDigestSchedule.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentCreate.vue","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"News reporter and design-book fixtures","status":"passing","evidence":"The real component was checked in the design-book specimen and News reporter fixture. Both editor switches moved focus to the revealed input; the 390px dark product fixture fit without horizontal page overflow. The last selected weekday remained keyboard-focusable, and weekday targets were at least 44 by 44 pixels at 390px with touch emulation. No agent was created."},{"kind":"review","ref":"Screen-reader, physical device, and backend scheduling behavior","status":"pending"}]},"relatedDocuments":[{"id":"design.patterns.controls","relation":"related"},{"id":"design.components.field-group","relation":"related"},{"id":"design.patterns.resource-creation","relation":"related"}]}
---

# Digest schedule

## Purpose

Set when a news reporter posts its recurring digest without requiring a cron expression for the common case.

## Use when

The Agents news reporter template asks for a time and one or more weekdays. This component is specific to that template and its cron parameter.

## Avoid when

The task needs a one-time date, a general recurrence editor, or local-time scheduling. Those require their own timezone and daylight-saving decisions; this component does not supply a general calendar or timezone picker.

## Anatomy and variants

The common editor contains a native time input labeled UTC, a weekday group, and an Edit cron action. The advanced editor offers a cron input, timezone and daylight-saving guidance, and a way back when its expression can be represented by the common editor. Unsupported expressions remain editable as cron.

## Behavior

The common editor converts its selected time and weekdays into a cron expression, retaining at least one selected day. The final selected day remains focusable and announces why it cannot be removed; selecting another day makes it removable again. Clearing the time temporarily prevents weekday changes. Switching editors moves focus to the newly shown field and preserves the expression. The parent owns validation, submission, and persistence; the specimen keeps edits local.

## Content

Name UTC beside the time. Explain that cron defaults to UTC unless its timezone prefix specifies another zone, and that daylight-saving transitions may skip or repeat a local time. Show actionable validation next to the field.

## Layout and responsive behavior

The time and weekday group wrap as space narrows; weekday buttons also wrap rather than overflow. Native time input presentation varies by browser and device. Keep related labels and help visible when the layout wraps.

## Accessibility

The time input has a visible label and required state; weekday choices are named toggle buttons in a fieldset with a legend and explanatory text. Errors are associated with the relevant input. When switching editors, focus the newly revealed input so keyboard users can continue. Assistive-technology and physical-device behavior still need review.

## Code and evidence

[`AgentDigestSchedule.vue`](../../../providers/agents/portal/src/components/AgentDigestSchedule.vue) is used by [`AgentCreate.vue`](../../../providers/agents/portal/src/views/AgentCreate.vue). The [design-book specimen](http://127.0.0.1:5197/?entry=design.components.digest-schedule) and [News reporter fixture](http://127.0.0.1:5198/?provider=agents&scenario=news-template&theme=light&route=%23%2Fcreate%2Fagent%2Fnews-reporter) use sample values and do not create an agent.

## Related guidance

See [control selection](../patterns/controls.md), [field groups](field-group.md), and [resource creation](../patterns/resource-creation.md).
