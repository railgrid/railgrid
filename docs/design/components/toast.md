---
{"schema":1,"id":"design.components.toast","title":"Toast notifications","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Vue PortalKit ships a versioned cross-bundle transport, primary/fallback hosts, and contextual inline notifications; Agents uses the separate framework-neutral compatibility bus."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/toast.md#toast-notifications","role":"design"},{"path":"provider-sdk/portalkit-vue/toast.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ToastHost.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/InlineNotification.vue","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"},{"path":"portal/src/App.vue","role":"implementation"},{"path":"portal/src/composables/useToastBottomOffset.ts","role":"implementation"},{"path":"providers/app-studio/portal/src/App.vue","role":"reference"},{"path":"provider-sdk/portalkit/toast.ts","role":"reference"},{"path":"providers/agents/portal/src/ui/toast.ts","role":"reference"},{"path":"provider-sdk/portalkit-vue/Toast.conformance.test.mjs","role":"reference"},{"path":"provider-sdk/portalkit-vue/Toast.behavior.test.mjs","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"test","ref":"node provider-sdk/portalkit-vue/Toast.conformance.test.mjs","status":"passing","evidence":"Six source-contract tests passed after the final rebase."},{"kind":"test","ref":"node provider-sdk/portalkit-vue/Toast.behavior.test.mjs","status":"passing","evidence":"Seven transport behavior tests passed in this checkout; these do not mount the host."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.navigation-and-feedback","relation":"implements","path":"docs/design/patterns/navigation-and-feedback.md"},{"id":"design.content.ui-copy","relation":"prerequisite","path":"docs/design/content/ui-copy.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"},{"id":"design.foundations.layering","relation":"see-also","path":"docs/design/implementation/layering.md"}]}
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

Do not tint toast backgrounds or add glow. Use the icon and message to convey
status; decorative effects do not replace status semantics. Do not emit a toast and inline notification for the same
contextual failure, and do not mount a second primary host.

## Anatomy and variants

1. **Tone icon:** a leading semantic icon communicates the kind of message
   alongside text.
2. **Message:** plain text describes the outcome; an optional source label
   appears only when supplied by the caller.
3. **Action or link:** at most one optional recovery action or navigation link.
4. **Dismiss:** an explicitly named control closes the notification.

The active Vue host renders one bottom-right card with 6px corners, a raised
surface, a subtle border, and restrained elevation. The framework-neutral
compatibility renderer can show a stack; its queue behavior differs as noted
below.

## Behavior

Priority is `error > warning > info > ok`. Only a strictly higher-priority
toast preempts the visible toast; the preempted item is requeued with its
remaining time. `ok` lasts 5s and `info` 6s by default. Warnings and errors
are persistent by default; an explicit finite duration is clamped to at least
5s. Toasts with an action or link remain persistent.

For a Vue toast, supply either an action or a native navigation link. If both
are supplied, the action wins and the link is discarded. The link accepts a
root-relative path beginning with one slash, a fragment beginning with `#`,
or an `http://` or `https://` URL. Other forms, including protocol-relative
URLs, relative paths, and `javascript:` URLs, are silently discarded. A
discarded link does not make the toast persistent: an `ok` or `info` notice
then expires normally unless another duration is specified. Use an accepted
destination so the intended navigation remains available.

Hover, focus, and a hidden document pause the timer, and resume preserves the
remaining time. Primary/fallback host takeover preserves timer, action
busy/error, and focused-control state. Vue notifications share a document-wide
transport across bundles. Their scope defaults to `global`, so give a feature or provider a stable, distinct `scope`
when it needs to replace or clear only its own notices. A `dedupeKey` replaces
an existing notice only within the same scope; replacement dismisses the old
notice with reason `replaced` and creates a new ID. `clearToasts(scope)` clears
that scope across bundles. `dismissToast(id, scope)` checks the scope only when
one is supplied; an omitted scope can dismiss any matching ID. With `useToast`,
set a default scope to constrain its dismissal and make its no-argument
`clearToasts()` effective. Without a default or explicit scope, that service's
clear is a no-op. Scope is a coordination boundary, not an authorization boundary.

The Vue host keeps a pending action focusable, marks it busy and temporarily
unavailable, announces that it is in progress, and ignores repeated activation.
On failure it keeps the toast and its safe retry message available. If a
focused toast is replaced, focus transfers to the next toast or its origin;
the host does not reclaim focus if the user has moved it elsewhere during the
render.

Agents uses the framework-neutral bus through a provider-local adapter; Quickstart also uses that bus. Its success and informational messages expire after 5s and 6s, pausing for hover, focus, or a hidden document and resuming with the remaining time. Errors and action-bearing messages persist until dismissed or acted on. Action promises keep their toast and focused control available while pending, and failed actions show a safe retry message. When the stack is full, transient notices yield before persistent ones. If persistent notices exceed the usual three-card limit, the stack scrolls instead of discarding a recovery action. The legacy bus does not implement the Vue host’s priority queue, host takeover, or native-link API. Its `clearToasts()` removes notices created by that module instance, not every notice in the document; independently bundled copies share the rendered stack but retain separate ownership. The Vue transport and legacy bus are separate, so clearing either does not clear the other.

The scrolling stack follows new arrivals when the reader is already at its
end. It does not jump while focus or a pointer is inside the stack, or while
the reader has scrolled away from the end.

## Content

Messages are 13px primary text. Use semantic icons and borders for tone. Keep
copy plain text, expose at most one explicit action or native navigation link, and never infer a source or
provenance label.

## Layout and responsive behavior

Keep the active card bottom-right and account for navigation, terminal chrome,
and safe-area insets through `--k-toast-bottom-offset`. Action and dismiss
controls reach 44×44px on coarse pointers. Cards use the shared raised surface
and restrained elevation; their background is not tinted.

## Accessibility

The host keeps separate polite status and assertive alert live regions mounted
from first paint. Automatic announcements send errors to the alert channel and
other tones to status; callers may select polite, assertive, or off. Escape
dismisses only while focus is inside the host, then focus returns to the next
toast or its recorded origin. Reduced motion removes toast transitions and
action-spinner animation. Follow the [accessible interaction
policy](../accessibility/interaction.md) for the wider contract.

The framework-neutral renderer also supports Escape and focus return, and it
does not evict a focused transient notice. Clearing its stack returns focus to
another remaining notification or the focused notification's origin. Its live region is created when the
first notice appears, so it does not have the Vue host's pre-mounted channels;
screen-reader behavior across products remains to be reviewed.

## Code and evidence

The canonical Vue implementation is `provider-sdk/portalkit-vue/toast.ts`,
`provider-sdk/portalkit-vue/ToastHost.vue`, and
`provider-sdk/portalkit-vue/InlineNotification.vue`, with shared styles in
`provider-sdk/portalkit/railgrid-ui.css`. The framework-neutral
`provider-sdk/portalkit/toast.ts` is a compatibility implementation. Verify
distribution with `make verify-portalkit`.
Use the [toast review](http://127.0.0.1:5197/?entry=design.components.toast)
to compare a short success receipt, persistent error, a native link, a retry
that fails once, and Undo. The
[Agents notification fixture](http://127.0.0.1:5197/agents-toast.html)
uses the framework-neutral renderer and simulated actions.

## Related guidance

Use the [navigation and feedback pattern](../patterns/navigation-and-feedback.md)
for feedback placement, the [UI copy policy](../content/ui-copy.md) for
messages, the [accessible interaction policy](../accessibility/interaction.md),
and the [review checklist](../quality/review-checklist.md).
