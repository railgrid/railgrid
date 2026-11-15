---
{"schema":1,"id":"design.components.spinner","title":"Spinner","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"The cited implementation is evidence for this review; product-wide adoption has not been audited."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/spinner.md","role":"design"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"unverified","checks":[{"kind":"review","ref":"Spinner design and product adoption audit","status":"pending"}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# Spinner

## Purpose

Communicate an operation in progress.

## Use when

An operation is in progress but the amount of work remaining cannot be stated accurately. Pair the indicator with a named action or status.

## Avoid when

Progress can be measured: use labeled task progress. Do not leave a spinner as the only representation of failure or completion.

## Anatomy and variants

1. **Indicator:** a compact, decorative icon using `.k-spin`.
2. **Context:** a named action or adjacent status says what is happening.
3. **Outcome:** completion, failure, or recovery replaces the pending state.

Compare idle, busy, finished, and failed states; the indicator itself does not
represent a measurable amount of work.

## Behavior

Keep focus on the originating action while busy and prevent duplicate requests.
If the action stays focusable, expose `aria-disabled` and guard its handler;
the attribute alone does not block activation. Announce the outcome without
pretending progress is measurable. Background refresh should preserve useful
content instead of replacing it with a spinner.

## Content

Name the operation, for example **Testing connection…**, rather than showing
an unexplained spinning icon. If it fails, say what failed and offer the
appropriate retry or next action. Avoid promising a duration or result.

## Layout and responsive behavior

Keep the icon and its action or status together without shifting unrelated
content. Allow longer labels to wrap at narrow widths. Review both themes and
the 390px frame; the shared spinner becomes static under reduced motion.

## Accessibility

Give an icon-only action an accessible name and expose busy state. Hide a
decorative spinner from assistive technology, and put live status outside a
busy region when it needs to be announced immediately. Keep the text and
outcome available when motion is reduced.

## Code and evidence

The [design-book specimen](http://127.0.0.1:5197/?entry=design.components.spinner)
uses a simulated connection test to compare idle, busy, success, failure, and
retry. Choosing a simulated outcome returns focus to the action when the
outcome controls disappear; this fixture does not test a real connection.
Chromium checks at 1440px light and 390px dark covered busy, failure, retry,
and success focus and status without page overflow; they do not establish
live connection or assistive-technology behavior.

The [implementation evidence](../../../provider-sdk/portalkit/railgrid-ui.css) is a starting point, not a product-wide adoption audit. The design and adoption review is pending. Open the [Agents models product fixture](http://127.0.0.1:5198/?provider=agents&scenario=spinner&route=%23%2Fmodels) for context; it uses fixture data and is not a live backend.

The [Edge service detail fixture](http://127.0.0.1:5199/service-detail.html)
mounts the product view with a simulated two-second refresh. Browser review
confirmed that the refresh control retains focus during and after the read;
the mounted behavior test also checks that repeated activation does not start
a second read. The fixture does not contact a hub. Mobile, dark-theme, and
live-backend behavior for this scenario remain unverified.

The [App Studio workbench fixture](http://127.0.0.1:5198/workbench-refresh.html)
mounts History and Integrations with simulated delayed reads. Both refresh
controls retain focus while pending and after completion. Integrations keeps a
confirmed empty result visible during refresh. Its [failed-refresh scenario](http://127.0.0.1:5198/workbench-refresh.html?scenario=failed-refresh)
also retains that result, keeps Retry focused while pending, and returns focus
to Refresh on recovery. This is fixture evidence, not a live-backend or
screen-reader audit.
The [partial-discovery scenario](http://127.0.0.1:5198/workbench-refresh.html?scenario=partial-discovery)
exercises the same focus behavior when a discovery warning clears.

The Code [connection](http://127.0.0.1:5197/code-refresh.html) and
[repository](http://127.0.0.1:5197/code-refresh.html?view=repository) fixtures
mount the product detail views with simulated three-second reads. Their refresh
actions retain focus while pending. The shared indicator animates normally and
stops under reduced motion; this is fixture evidence, not a live-backend audit.

The [Agents form fixture](http://127.0.0.1:5197/agents-form-busy.html) shows
simulated provider-connection and harness-identity saves using the shared
indicator. Browser review checked normal motion on the provider form and
reduced motion on the harness form, including a dark 390px viewport. The
harness save button retained focus when the simulated save began; mounted
tests cover duplicate-submission guards and provider-check focus. The fixture
does not submit credentials; live saves and assistive-technology behavior
remain unverified.

The [Quickstart greeting fixture](http://127.0.0.1:5198/quickstart-recovery.html?scenario=populated)
uses a simulated action to show a row's in-progress label and local result. At
390px with touch emulation, the action retained focus while busy and after
completion; repeat activation was blocked. It does not contact a hub.
If focus moves to another greeting control before completion, the result update
preserves that focus. A revealed exact timestamp also remains revealed through
the update. The latter behaviors are covered by a mounted fixture test.

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
