---
{"schema":1,"id":"design.components.ai-execution-details","title":"AI execution details","kind":"component","status":"draft","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"AgentKit renders an already-sanitized projection. Providers own disclosure validation, authorization, and execution lifecycle."},"appliesTo":["agents","app-studio"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/ai-execution-details.md#ai-execution-details","role":"design"},{"path":"provider-sdk/agentkit-vue/AIExecutionDetails.vue","role":"implementation"},{"path":"provider-sdk/agentkit/activity.ts","role":"implementation"},{"path":"provider-sdk/agentkit/agent-ui.css","role":"implementation"},{"path":"providers/app-studio/portal/src/AssistantExecDetails.vue","role":"reference"},{"path":"providers/agents/portal/src/components/AgentToolDetails.vue","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Cross-product execution details and assistive-technology review","status":"pending"}]},"relatedDocuments":[{"id":"design.components.ai-action-row","relation":"related"},{"id":"design.components.ai-interrupt","relation":"related"},{"id":"design.ai.evidence-and-status","relation":"related"},{"id":"design.accessibility.interaction","relation":"related"}]}
---

# AI execution details

## Purpose

Present the command, output, and reported outcome of an execution, or disclose a proposed command before approval.

## Use when

A provider has an authorized, sanitized execution projection that helps a person understand or review an action. Agents and App Studio map their own data into the shared presentation.

## Avoid when

Do not treat this component as a parser, authorization gate, or execution engine. Do not display raw secrets or unvalidated provider content, and do not present proposed execution as a completed result.

## Anatomy and variants

1. **Command or input:** the supplied, authorized projection of what ran or is
   proposed.
2. **Output:** a bounded preview of supplied output, with an explicit marker
   when the provider truncated the source.
3. **Controls:** copy, line wrapping, and expansion where applicable.
4. **Outcome:** reported status, optional exit code and duration, and any
   supplied detail or safe link.

The approval variant presents the proposed command, sanitized arguments, and
scope fields without presenting an execution receipt.

## Behavior

Activity output starts with a bounded preview and offers expansion when additional supplied text exists. The provider's `outputTruncated` flag separately indicates that the source itself omitted content; expanding the preview cannot restore it. Copy includes the command or input and supplied output. Success clears after two seconds; failure remains visible with a manual-copy hint until the next attempt or a command/input change. A late clipboard result is ignored when the command or input changes. An unrecognized status remains unavailable unless a supplied nonzero exit code establishes failure. A supplied exit code is shown for a non-running result, including an otherwise unverified status; it is not invented.

## Content

Use accurate provider-reported states and distinguish a nonzero exit from a successful execution. Keep proposed actions, running work, failure, and completed receipts distinct. Explain missing or truncated output without inventing a result.

## Layout and responsive behavior

The output can scroll horizontally or wrap on request, and long output is independently scrollable. Keep the status and expansion controls usable at narrow widths. On devices with a coarse pointer, including hybrid devices, keep the receipt controls visible and provide at least 44-by-44-pixel targets. The approval variant should expose the scope needed for a decision.

## Accessibility

Output is keyboard-focusable, the wrap control exposes pressed state, the expansion control exposes expanded state, and copy feedback has status semantics. The provider remains responsible for naming its surrounding disclosure and ensuring sensitive values are not presented. Screen-reader and cross-product checks remain open.

## Code and evidence

[`AIExecutionDetails.vue`](../../../provider-sdk/agentkit-vue/AIExecutionDetails.vue) renders the view; [`activity.ts`](../../../provider-sdk/agentkit/activity.ts) owns status and safe-link presentation. App Studio's [`AssistantExecDetails.vue`](../../../providers/app-studio/portal/src/AssistantExecDetails.vue) and Agents' [`AgentToolDetails.vue`](../../../providers/agents/portal/src/components/AgentToolDetails.vue) provide their own projections. The [execution-details review](http://127.0.0.1:5197/?entry=design.components.ai-execution-details) uses sample data, not live execution.

## Related guidance

See [AI activity row](ai-action-row.md), [AI interruption](ai-interrupt.md), and [AI evidence and status](../ai/evidence-and-status.md).
