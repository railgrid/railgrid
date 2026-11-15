---
{"schema":1,"id":"design.foundations.motion","title":"Motion and reduced motion","kind":"token","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"Documents selected host and shared recipes. Product-wide motion and reduced-motion behavior have not been audited."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/motion.md#motion-and-reduced-motion","role":"design"},{"path":"portal/src/assets/main.css","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Cross-product reduced-motion and assistive-technology review","status":"pending"}]},"relatedDocuments":[{"id":"design.foundations.geometry","relation":"related"},{"id":"design.accessibility.interaction","relation":"related"},{"id":"design.components.status-badge","relation":"related"}]}
---

# Motion and reduced motion

Motion should explain a change, direct attention to a meaningful state, or maintain continuity. Keep it quiet and brief. Do not rely on movement, color, or animation alone to communicate status; labels and values remain available when motion is removed. Avoid decorative, looping movement in product chrome.

## Current recipes

These are observed implementations, not a universal token scale. Check the owning component for its exact behavior.

| Purpose | Current example | Reduced-motion behavior |
| --- | --- | --- |
| Continuous activity | `.k-spin` rotates in 800ms; `.live-dot` pulses every 2.5s for in-progress status. | Static indicator; retain a text label or accessible state. |
| Task or usage change | `.k-progress__bar` transitions its transform over 300ms. | Change immediately; retain the label and current value. |
| Brief feedback | Shared modal and toast entrances run for roughly 140–180ms. | Shared entrances are disabled; the feedback remains visible. |
| Control state | Shared controls generally transition color or geometry over 120–150ms. | Host globally shortens transitions; the shared select, filter, dashboard, toggle, and disclosure recipes suppress their spatial transitions independently. |
| Host entry | `.stagger-item` uses a 400ms entry. | Host reduces animation duration and iteration count. |

The host's global reduced-motion rule applies in the host document. Standalone providers also ship the shared stylesheet, whose reduced-motion rules cover specific recipes, including shared select, table-filter, and dashboard chevrons; do not assume the host's global rule exists in every standalone context. Verify each new animation in both contexts. Do not remove busy text, disable a control's feedback, or wait for an animation to finish before updating the underlying state.

App Studio also scopes a reduced-motion override to its provider roots. Its
`animate-spin` indicators become static, while the surrounding busy copy and
actions remain available. This is a provider-specific implementation, not
evidence that every standalone provider has the same coverage.

The [App Studio loading fixture](http://127.0.0.1:5198/?provider=app&scenario=app-slow-connections&route=~models)
holds a simulated provider-connection read for ten seconds. Browser review
confirmed its loading label remains present and its spinner is static with
reduced motion, and spins with normal motion. It does not contact a hub.

## Review

Use the interactive specimen to advance a simulated task, compare an in-progress status and loading indicator, then turn on the operating system's reduced-motion setting and repeat. The examples use shared CSS; the task does not transfer files. Open the linked agent run and dashboard fixtures for real composition with sample data. Browser and assistive-technology review across all products is still needed; this page is not evidence of universal conformance.
