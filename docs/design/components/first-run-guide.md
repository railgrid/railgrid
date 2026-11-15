---
{"schema":1,"id":"design.components.first-run-guide","title":"FirstRunGuide","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"FirstRunGuide is the shared first-use surface; vanilla portals use the matching k-first-run semantic classes."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/first-run-guide.md#firstrunguide","role":"design"},{"path":"provider-sdk/portalkit-vue/FirstRunGuide.vue","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.resource-creation","relation":"implements","path":"docs/design/patterns/resource-creation.md"},{"id":"design.content.ui-copy","relation":"prerequisite","path":"docs/design/content/ui-copy.md"},{"id":"design.patterns.navigation-and-feedback","relation":"see-also","path":"docs/design/patterns/navigation-and-feedback.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"}]}
---

# FirstRunGuide

## Purpose

For an authoritative first-use empty collection, use `FirstRunGuide.vue` or
matching `k-first-run*` semantic markup in a vanilla portal. It explains the
value, offers the immediate primary action, and shows the shortest meaningful
resource journey. Missing prerequisites change the current step and action;
they do not leave a user at an inert empty table.

## Use when

Use it for an authoritative first-use empty collection. Use the Vue component
or matching `k-first-run*` semantic markup in a vanilla portal.

## Avoid when

Do not leave a first-use collection at an inert empty table. Missing
prerequisites change the current step and action rather than removing the next
meaningful step.

## Anatomy and variants

1. **Optional icon:** a small decorative cue for the resource or task.
2. **Title and explanation:** state what can be created and why it helps.
3. **Primary action:** names the immediate next step; an optional secondary
   action can support it without competing for emphasis.
4. **Journey:** an optional ordered list identifies current, completed, and
   upcoming prerequisite steps.

The supported implementations are `FirstRunGuide.vue` and matching vanilla
`k-first-run*` semantic markup.
For a single obvious next step, pass an empty steps list to use the centered
invitation without a progress journey. A small contextual icon may accompany
the concise title, explanation, and primary action. Use it only after a successful,
authoritative empty read; filtered results and failed reads use their own states.
The design-book fixture compares these cases with a prerequisite journey using
the shared `FirstRunGuide` and `ResourceTable` components. Its sample actions
and reads are illustrative; the Models and Edge Services fixtures show the
patterns in page context.

Step descriptions are optional when the labels explain the journey. The embedded
variant renders just the progress list within an existing creation card, below
the current task heading.

## Behavior

Missing prerequisites change the current step and action. The guide continues
to offer a meaningful next step rather than leaving the user at an inert empty
table.

When the next step is a route, use the `actions` slot for a real link with the
shared primary style. Preserve its destination and native modified-click
behavior; an ordinary click may be handed to the in-app router. Keep the
default button for actions that do not navigate.

## Content

Use a concise title, a short explanation, and one primary action. The copy
explains the value and shortest meaningful resource journey; the primary action
is the immediate next step.

## Layout and responsive behavior

The empty state is an invitation: use a concise title, an explanation, and one
focused primary action. Add a journey only when it helps explain prerequisites.
The primary action is
the only solid accent action in the view and follows the shared glow rule.

## Accessibility

The supported vanilla markup is semantic `k-first-run*` markup. Keep the
immediate primary action reachable and identifiable in the empty collection's
accessible structure; this component is not a replacement for the shared
interaction policy.

## Code and evidence

The canonical Vue component is [`FirstRunGuide.vue`](../../../provider-sdk/portalkit-vue/FirstRunGuide.vue),
and shared styling lives in [`railgrid-ui.css`](../../../provider-sdk/portalkit/railgrid-ui.css).
The [four-state review fixture](http://127.0.0.1:5197/?entry=design.components.first-run-guide)
links to the sample Models and Edge Services product fixtures. Distribution is
checked by `make verify-portalkit`.

Open [Edge Services with a connected edge](http://127.0.0.1:5199/empty-product.html)
or [without a connected edge](http://127.0.0.1:5199/empty-product.html?scenario=no-edge)
to compare a direct action with prerequisite guidance. The
[Agents Models](http://127.0.0.1:5198/?provider=agents&scenario=first-model&route=%23%2Fmodels)
and [App Studio Models](http://127.0.0.1:5198/?provider=app&route=~models)
fixtures start with a saved provider connection and no model bindings.

## Related guidance

See the [resource creation journey](../patterns/resource-creation.md), [UI copy
policy](../content/ui-copy.md), [navigation and feedback](../patterns/navigation-and-feedback.md),
and [accessible interaction policy](../accessibility/interaction.md).
