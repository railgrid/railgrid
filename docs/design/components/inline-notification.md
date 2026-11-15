---
{"schema":1,"id":"design.components.inline-notification","title":"Inline notification","kind":"component","status":"draft","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The shared Vue component is shipped; adoption across all product surfaces has not been audited."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/inline-notification.md#inline-notification","role":"design"},{"path":"provider-sdk/portalkit-vue/InlineNotification.vue","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"test","ref":"providers/infrastructure/portal/src/toastMounted.test.ts","status":"passing","evidence":"Mounted tests cover the shared notification behavior; see the test for exact coverage."},{"kind":"review","ref":"Product-wide notification adoption and accessible announcements","status":"pending"}]},"relatedDocuments":[{"id":"design.components.toast","relation":"related"},{"id":"design.patterns.navigation-and-feedback","relation":"implements"},{"id":"design.accessibility.interaction","relation":"see-also"}]}
---

# Inline notification

## Purpose

Explain a condition or outcome beside the work it affects, with a local next action when one is available.

## Use when

A user needs context or recovery within a form, section, or resource. For example, a failed model connection can show the problem and retry next to that connection.

## Avoid when

A short, context-free success receipt belongs in a [toast](toast.md). Avoid emitting both a toast and an inline message for the same failure. Persistent state essential to the task must not disappear automatically.

## Anatomy and variants

1. **Tone icon:** a decorative icon distinguishes info, success, warning, or
   error alongside text.
2. **Body:** an optional title and a concise message explain the condition.
3. **Action:** an optional named action starts a caller-owned recovery.
4. **Dismiss:** an optional, named dismiss button is appropriate only when the
   message can safely be removed.

The shared `InlineNotification` renders these parts; the caller chooses the
copy, executes actions, and owns subsequent state.

## Behavior

An action emits `action`; the caller decides when it is busy, completed, or failed. While busy, the action remains focusable, exposes a busy label, `aria-busy` and `aria-disabled`, and ignores repeat activation. Its pointer indicates progress and it does not acquire the interactive hover treatment; text remains at full contrast. Scope busy state to the request and context so a late result cannot block or release a newer retry. If the caller removes a focused notification after recovery, it moves focus to the relevant remaining content. Dismiss emits `dismiss`; the caller owns whether to remove the notification. Do not offer dismissal when removing the message would hide an unresolved condition the user needs.

## Content

Name the affected object or operation, state what happened, and offer a concrete next action when useful. Keep copy short and specific; do not put secret values in feedback. The action label should describe the action rather than simply saying “OK.”

## Layout and responsive behavior

Place the notification at the point of failure or decision rather than at the top of an unrelated page. It must wrap within its container; test long messages and action labels at narrow widths. Actions and dismiss controls expand to at least 44 × 44 pixels for coarse or hybrid pointers; viewport width alone does not determine the target size. Do not rely on tone color alone to communicate the condition.

## Accessibility

By default errors use an assertive alert and other tones use a polite status; callers can explicitly choose polite, assertive, or off. Choose the least disruptive channel appropriate to the event and avoid repeating the same announcement elsewhere. Icons are decorative. Name the action and dismissal controls and expose the busy state. Newly inserted notifications need to be tested in their actual live-region context; this component alone cannot guarantee announcements across assistive technologies.

## Code and evidence

The [shared component](../../../provider-sdk/portalkit-vue/InlineNotification.vue) is used in [App Studio model settings](../../../providers/app-studio/portal/src/ModelsSettings.vue) for failures and recovery. The design-book specimen uses the shared component with a simulated action and shows dismissal on optional information rather than an unresolved failure; its controls restore focus when an example is removed or reset. In a browser with touch emulation, its Retry and Dismiss controls each measured 44 × 44 pixels; the [Agents usage fixture](http://127.0.0.1:5198/usage-recovery.html) Retry also measured 44 × 44. This measurement does not establish a product-wide touch audit. The [App Studio models fixture](http://127.0.0.1:5198/?provider=app&scenario=relationship&route=~models) provides product context. The [App Studio connection retry fixture](http://127.0.0.1:5198/?provider=app&scenario=app-connection-retry&route=~models) and [Agents toolset retry fixture](http://127.0.0.1:5198/?provider=agents&scenario=toolset-connection-retry&route=%23%2Fcreate%2Ftoolset) use real product views with simulated failed reads and delayed recovery. The [Edge Services](http://127.0.0.1:5199/product.html) and [Workloads](http://127.0.0.1:5199/product.html?view=workloads) product fixtures simulate a failed deletion followed by a successful retry; the failure is distinct from a stale collection read. These fixtures do not establish behavior against a live provider.

The [new-project model retry fixture](http://127.0.0.1:5198/?provider=app&scenario=app-model-retry&theme=dark&route=~new) simulates a failed model read and delayed recovery. At 390px, Retry stayed focused and announced its busy state; after recovery, focus moved to the model setup heading. It does not contact a live service.
When a newly saved model is still becoming available, a manual **Check again**
keeps the same notification and focused action in place with a busy label
throughout readiness polling. The initial automatic check may use a loading
status instead. The [design-book specimen](http://127.0.0.1:5197/?entry=design.components.inline-notification)
mounts App Studio's actual feedback component with simulated loading, pending,
failure, and retry states. Deferred tests cover polling; this saved-model path
has not yet been reviewed in a live provider.

## Related guidance

See [toast notifications](toast.md), [navigation and feedback](../patterns/navigation-and-feedback.md), and the [interaction policy](../accessibility/interaction.md).
