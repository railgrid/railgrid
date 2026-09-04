---
{"schema":1,"id":"design.components.toast","title":"Toast notifications","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Vue PortalKit ships a versioned cross-bundle transport, primary/fallback hosts, and contextual inline notifications; Agents temporarily retains the frozen framework-neutral bus."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/toast.md#toast-notifications","role":"design"},{"path":"provider-sdk/portalkit-vue/toast.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ToastHost.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/InlineNotification.vue","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"},{"path":"portal/src/App.vue","role":"implementation"},{"path":"portal/src/composables/useToastBottomOffset.ts","role":"implementation"},{"path":"providers/app-studio/portal/src/App.vue","role":"reference"},{"path":"provider-sdk/portalkit/toast.ts","role":"reference"},{"path":"providers/agents/portal/src/ui/toast.ts","role":"reference"},{"path":"provider-sdk/portalkit-vue/Toast.conformance.test.mjs","role":"reference"},{"path":"provider-sdk/portalkit-vue/Toast.behavior.test.mjs","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"test","ref":"node provider-sdk/portalkit-vue/Toast.conformance.test.mjs","status":"passing","evidence":"Six source-contract tests passed after the final rebase."},{"kind":"test","ref":"node provider-sdk/portalkit-vue/Toast.behavior.test.mjs","status":"not-run","evidence":"The test imports portal/node_modules/typescript, which is not installed in this worktree."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.navigation-and-feedback","relation":"implements","path":"docs/design/patterns/navigation-and-feedback.md"},{"id":"design.content.ui-copy","relation":"prerequisite","path":"docs/design/content/ui-copy.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"}]}
---

# Toast notifications

## Purpose

Vue PortalKit provides a versioned cross-bundle toast transport, an active host
that owns queue and lifecycle state, and an inline notification for contextual
recovery. Independently bundled providers can publish feedback without creating
competing document-level stacks.

## Use when

Use `toast.ts` with `ToastHost.vue` for document-level feedback that should
remain available while the user continues in the current surface. The root
shell mounts one `owner="primary"` host; a standalone Vue provider may mount an
`owner="fallback"` host. Use `InlineNotification.vue` beside a contextual
failure or recovery action.

## Avoid when

Do not tint toast backgrounds or add glow. Warnings and errors use their
semantic border in addition to their icon; decorative effects do not replace
status semantics. Do not emit a toast and inline notification for the same
contextual failure, and do not mount a second primary host.

## Anatomy and variants

The active host renders one bottom-right 6px card. Cards use `surface-raised`,
a `border-default` hairline, 8px stack geometry, and heavy elevation. Tone is
carried by the leading semantic icon; warning and error also use their semantic
border. Content is plain text with at most one action and an explicit dismiss
button. A source label appears only when the caller supplies it.

## Behavior

Priority is `error > warning > info > ok`. Only a strictly higher-priority
toast preempts the visible toast; the preempted item is requeued with its
remaining time. `ok` lasts 5s and `info` 6s by default. Warnings and errors
are persistent by default; an explicit finite duration is clamped to at least
5s. Toasts with an action remain persistent.

Hover, focus, and a hidden document pause the timer, and resume preserves the
remaining time. Primary/fallback host takeover preserves timer, action
busy/error, and focused-control state. Scope and dedupe keys keep replacement,
dismissal, and clear operations bounded.

Agents is the temporary exception: its Vue portal keeps the frozen
framework-neutral bus for its provider-local subscription adapter. The plain
bus is also present in the self-contained Quickstart TypeScript kit; it is not
the Vue toast implementation.

## Content

Messages are 13px primary text. Use semantic icons and borders for tone. Keep
copy plain text, expose at most one explicit action, and never infer a source or
provenance label.

## Layout and responsive behavior

Keep the active card bottom-right and account for navigation, terminal chrome,
and safe-area insets through `--k-toast-bottom-offset`. Action and dismiss
controls reach 44×44px on coarse pointers. Cards use the shared raised surface
and heavy elevation; their background is not tinted.

## Accessibility

The host keeps separate polite status and assertive alert live regions mounted
from first paint. Automatic announcements send errors to the alert channel and
other tones to status; callers may select polite, assertive, or off. Escape
dismisses only while focus is inside the host, then focus returns to the next
toast or its recorded origin. Reduced motion removes toast transitions and
action-spinner animation. Follow the [accessible interaction
policy](../accessibility/interaction.md) for the wider contract.

## Code and evidence

The canonical Vue implementation is `provider-sdk/portalkit-vue/toast.ts`,
`provider-sdk/portalkit-vue/ToastHost.vue`, and
`provider-sdk/portalkit-vue/InlineNotification.vue`, with shared styles in
`provider-sdk/portalkit/faros-ui.css`. The framework-neutral
`provider-sdk/portalkit/toast.ts` is a compatibility implementation. Verify
distribution with `make verify-portalkit`.

## Related guidance

Use the [navigation and feedback pattern](../patterns/navigation-and-feedback.md)
for feedback placement, the [UI copy policy](../content/ui-copy.md) for
messages, the [accessible interaction policy](../accessibility/interaction.md),
and the [review checklist](../quality/review-checklist.md).
