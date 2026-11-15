---
{"schema":1,"id":"design.components.progress","title":"Task progress","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"The cited implementation is evidence for this review; product-wide adoption has not been audited."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/progress.md","role":"design"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"unverified","checks":[{"kind":"review","ref":"Task progress design and product adoption audit","status":"pending"}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# Task progress

## Purpose

Show measurable progress toward completing a task.

## Use when

The work has a meaningful known total, such as files uploaded out of files selected. Show the units and actual count.

## Avoid when

The duration or total is unknown: use an indeterminate busy indicator. A quota is a usage meter, not task completion.

## Anatomy and variants

1. **Task label:** name the work being measured, such as **Uploading project
   files**.
2. **Value:** show completed and total units, such as **3 of 8 files**.
3. **Track:** a 6px track and proportional fill, using the shared task-progress
   recipe. Use a native `progress` element when practical.

Compare the in-progress and finished states with quota usage separately.

## Behavior

Use known values and units; update the value as work completes. Replace the
active indicator with a completion or failure state that describes the outcome
and any recovery. Do not invent a percentage or treat completed requests as
proof that downstream work has finished.

## Content

Use a verb and object for the task, and identify what the count measures. For
example, **8 of 10 requests finished** can include failures; report the
successes and failures separately. Use the same units in visible text and the
accessible value.

## Layout and responsive behavior

Keep label and value together above the track. The shared header wraps them
when needed, and the track fills the available width without forcing the page
wider. Review both themes and the 390px frame.

## Accessibility

Expose a named native progress control or progressbar with matching minimum,
maximum, and current values. Provide a textual value when a percentage would
hide meaningful units. Respect reduced motion and do not rely on fill color
alone to communicate the result.

## Code and evidence

The [shared stylesheet](../../../provider-sdk/portalkit/railgrid-ui.css) implements the task-progress recipe. Its native progress variant preserves the browser's progress semantics while using the same track and accent colors.

The [progress review](http://127.0.0.1:5197/?entry=design.components.progress)
advances a simulated upload from three of eight files to an explicit
completion message. The quota comparison uses the dashboard's actual
[`MeteringUsageList`](../../../portal/src/components/MeteringUsageList.vue)
with sample usage. It labels approaching, reached, and exceeded limits
independently of color. The specimen does not perform an upload or read
organization usage. For all usage layouts, see the
[dashboard usage review](http://127.0.0.1:5197/?entry=design.components.dashboard-usage).

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
