---
{"schema":1,"id":"design.components.ai-interrupt","title":"AI interruption","kind":"component","status":"draft","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"partial","notes":"AgentKit provides the frame; App Studio owns review controls and authorization. Agents currently uses a separate approval card."},"appliesTo":["app-studio","agents"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/ai-interrupt.md#ai-interruption","role":"design"},{"path":"provider-sdk/agentkit-vue/AIInterrupt.vue","role":"implementation"},{"path":"provider-sdk/agentkit/agent-ui.css","role":"implementation"},{"path":"providers/app-studio/portal/src/ReviewRequest.vue","role":"reference"},{"path":"providers/agents/portal/src/components/AgentApprovalCard.vue","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Cross-product interruption and assistive-technology audit","status":"pending"}]},"relatedDocuments":[{"id":"design.components.ai-conversation","relation":"related"},{"id":"design.ai.app-studio-conversation","relation":"related"},{"id":"design.accessibility.interaction","relation":"related"}]}
---

# AI interruption

## Purpose

Present a request that needs a person's approval or clarification before an AI workflow can continue.

## Use when

An attended workflow needs a decision or answer tied to a specific request. App Studio uses `AIInterrupt` through `ReviewRequest` for approval and follow-up; Agents uses a separate `AgentApprovalCard` for approval.

## Avoid when

Do not use the frame as proof of authorization or as a substitute for an ordinary status message. A request whose details are missing must not become approvable merely because a card rendered.

## Anatomy and variants

The shared frame supports approval and follow-up kinds, a title, description, optional icon, busy state, an error row, caller-provided content, and action slots. The provider determines the controls and the request details. Invalid details can be shown in the error row.

## Behavior

The component renders supplied state; it does not execute a decision, validate authority, or disable slot actions. In App Studio's follow-up, a selected offered choice is shown on the choice itself; the Other input displays a custom answer rather than repeating that selection. While the component is mounted, a custom answer that matches an offered label remains in Other. Restored string-only answers do not carry their input mode and matching values are shown as offered choices. The caller must validate the current request, prevent duplicate submissions, keep unavailable approvals disabled, and handle denial or recovery. A resolved or failed state must reflect the actual result, not just a click.

## Content

Name the requested action and its relevant scope. Expose enough information for an informed decision without disclosing unauthorized data. When details cannot be inspected, explain why approval is unavailable and offer a safe next step.

## Layout and responsive behavior

Keep the request, error, and actions together; let controls wrap at narrow widths. Review long commands and questions in the provider context rather than assuming this frame determines their formatting.

## Accessibility

Callers provide named controls, associate questions and errors with their fields, preserve keyboard access, and expose busy state. The frame announces its error row as an alert and exposes `aria-busy` when requested. The frame does not provide a focus trap or automatically move focus.

The Agents approval card keeps the selected action focusable while its decision
is pending and blocks repeat decisions. If the card remains mounted with a
resolved receipt, focus moves to that receipt only when it was on an action.
Callers that remove the card own the subsequent focus destination. The
[design-book fixture](http://127.0.0.1:5197/?entry=design.components.ai-interrupt)
simulates a delayed success or failure without sending a decision. Browser
review covered those states; live decision handling and screen-reader behavior
remain unverified.

In Agents Inbox, a focused decision stays in place until its item leaves the
queue, then focus follows the remaining detail or the empty state. In run
detail, a removed decision card hands focus to the Execution heading. The
[run fixture](http://127.0.0.1:5200/?scenario=approval) simulates an approval
and a running state without contacting a backend.

In Agents chat, a resolved docked interruption returns focus to the named
conversation transcript when focus was still inside the removed card. The
[chat fixture](http://127.0.0.1:5197/agents-chat-approval.html) simulates an
approval after a sample message; no message or decision leaves the fixture.
Its [multiple-request scenario](http://127.0.0.1:5197/agents-chat-approval.html?scenario=multiple)
also shows an older inline approval and a newer docked request; resolving the
inline card returns focus to the transcript without dismissing the other one.

App Studio groups each clarification question with its answer controls and
names each Other input for its question. Continue, Allow, and Deny preserve focus
while their request is pending and block repeated activation. The design-book
fixture demonstrates a delayed failed clarification followed by a local retry;
it sends no answer. Browser and assistive-technology checks remain separate.

## Code and evidence

The [shared frame](../../../provider-sdk/agentkit-vue/AIInterrupt.vue) displays
the request and feedback. The [App Studio caller](../../../providers/app-studio/portal/src/ReviewRequest.vue)
owns the action controls and authorization predicate. The clarification fixture
mounts that caller with sample questions and a local failed submission followed
by a successful retry.

## Related guidance

See [AI conversation](ai-conversation.md), [App Studio modes and authority](../ai/app-studio-conversation.md).
