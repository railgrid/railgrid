---
{"schema":1,"id":"design.components.copy-control","title":"Copy control","kind":"component","status":"draft","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"partial","notes":"Shared useClipboardFeedback owns scoped asynchronous copy feedback; CLI setup, MCP setup and the account menu adopt it. Other cited views retain local presentation and lifecycle code."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/copy-control.md","role":"design"},{"path":"providers/agents/portal/src/views/RunDetail.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/useClipboardFeedback.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/clipboard-feedback.test.mjs","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Copy control design and product adoption audit","status":"pending"},{"kind":"test","ref":"provider-sdk/portalkit-vue/clipboard-feedback.test.mjs","status":"passing","evidence":"Lifecycle tests cover pending-write guards, retained per-field errors, scope changes, obsolete completions, timers, disposal and unavailable clipboard. Browser evidence is recorded separately."}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# Copy control

## Purpose

Copy an adjacent value to the clipboard.

## Use when

A visible value such as an identifier or endpoint is useful to copy. Place the quiet icon beside that value.

## Avoid when

The action navigates, downloads, or shares content rather than copying a specific value. Do not reveal a secret merely to make copying possible.

## Anatomy and variants

1. **Value:** the visible identifier, endpoint, command, or diagnostic to copy.
2. **Action:** a quiet, named icon button beside that value.
3. **Feedback:** brief local confirmation on success; persistent, actionable
   guidance if copying fails.

For a masked one-time value, the displayed placeholder is not the copy source.

## Behavior

Copy only the intended value; reset transient success and retain an error until the user can recover. Keep non-secret values selectable for manual copying. For a masked one-time credential or URL, keep it masked by default. Offer retry, or an explicit reveal followed by manual copying where that is supported; never suggest selecting the masked placeholder.

### Scoped copy feedback

Vue consumers can use `useClipboardFeedback` from PortalKit for the asynchronous
feedback lifecycle. Supply a reactive scope that changes whenever the copied
value or its owning context changes. The helper prevents duplicate pending
writes, ignores obsolete completions, preserves each field's error during
retry, and clears only that field's error on success. Success lasts briefly;
changing scope or disposing the view clears feedback and timers. It never
stores clipboard contents or includes exception details in feedback.

Consumers own accessible action names, a polite success announcement, local
`InlineNotification` errors, and pending semantics. Keep pending controls
focusable with `aria-disabled` and `aria-busy`; the helper also guards repeated
activation. CLI installation/login/verification, MCP endpoint/client setup and account
identity copy use this contract. A selected method or client change cannot mark its new
snippet as copied by an older request. Closing the account menu invalidates
pending identity feedback, so reopening cannot show an obsolete result.
Provider-specific copy surfaces have
not all migrated.

### One-time credential handoff

When a newly issued credential cannot be retrieved later, identify the account
it belongs to, say that it must be saved before closing, and show its expiry or
say when expiry is unavailable. Copy the actual credential while it remains
hidden; make revealing it an explicit action for manual recovery. A clipboard
failure must not expose the value or imply that the hidden placeholder can be
copied. Dismissing the handoff clears the displayed credential and feedback.
An asynchronous issuance or clipboard result must not revive the handoff or
attach feedback to a replacement credential or a different workspace, route, or
permission context.

This is a product-specific reference, not a shared credential-dialog component
or a claim that every credential flow has been audited. In workspace settings,
the service-account token dialog starts with focus on Close, traps keyboard
focus, and restores focus on dismissal. Its actions wrap at narrow widths. The
user can copy while the token is hidden, reveal it for manual selection after a
copy failure, or close it. Review these states using the synthetic
[service-account fixture](http://127.0.0.1:5196/ui/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/settings/workspaces?preview=service-account):
choose **Actions → Issue token**. It does not contact a hub or validate a real
system clipboard.

**Ownership decision:** keep the run and Infrastructure implementations local for now. Both the run and Infrastructure value render local inline feedback. Consolidation needs a shared API that preserves each context and a deliberate consumer migration; neither consumer is claimed to use a shared component today. See [Infrastructure ViewValue](../../../providers/infrastructure/portal/src/components/ViewValue.vue).

## Content

Name the object in the action, for example **Copy run ID**. Confirmation should
be brief and local. On failure, explain how to retry or manually copy the
visible value; never recommend selecting a masked placeholder.

## Layout and responsive behavior

Keep the action and feedback associated with their value as text wraps. Long
identifiers and commands may wrap or scroll within their own region. Keep the
action reachable with a 44px coarse-pointer target and review both themes and
the 390px frame.

## Accessibility

Name the copied value in the button label and announce success or failure
without moving focus. A pending action that remains focusable must block repeat
activation in its handler as well as expose its unavailable state. Preserve
manual selection for visible, non-secret values.

## Code and evidence

The [Agents run](../../../providers/agents/portal/src/views/RunDetail.vue) and
[Infrastructure value](../../../providers/infrastructure/portal/src/components/ViewValue.vue)
show copy feedback beside the identifier or endpoint.

Open the [copy-control review](http://127.0.0.1:5197/?entry=design.components.copy-control)
to try an Infrastructure endpoint, an Agents run diagnostic, and a masked
setup handoff. Clipboard availability depends on browser permissions.
Mounted tests exercise successful copy and access failures for Infrastructure
values.

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
