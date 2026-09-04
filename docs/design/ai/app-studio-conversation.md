---
{"schema":1,"id":"design.ai.app-studio-conversation","title":"App Studio conversation and modes","kind":"pattern","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"App Studio ships durable project threads, explicit response and approval modes, bounded context selections, and an explicit plan-to-implementation handoff."},"appliesTo":["app-studio","assistant","provider-portals"],"owner":"app-studio","canonicalSource":[{"path":"docs/design/ai/app-studio-conversation.md#app-studio-conversation-and-modes","role":"design"},{"path":"providers/app-studio/api/assistant_mode_prompt.go","role":"implementation"},{"path":"providers/app-studio/api/assistant_threads.go","role":"implementation"},{"path":"providers/app-studio/portal/src/ResponseModePicker.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/ApprovalModePicker.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/assistantThreadProjection.ts","role":"implementation"},{"path":"providers/app-studio/portal/src/conversationResilience.ts","role":"implementation"},{"path":"providers/app-studio/portal/src/responseModePicker.test.mjs","role":"reference"},{"path":"providers/app-studio/portal/src/approvalModePicker.test.mjs","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-design-docs","status":"passing","evidence":"The design knowledge-base metadata, IDs, sources, and links validate."},{"kind":"test","ref":"providers/app-studio/portal/src/responseModePicker.test.mjs","status":"not-run","evidence":"Portal dependencies are not installed in this worktree."},{"kind":"test","ref":"providers/app-studio/portal/src/approvalModePicker.test.mjs","status":"not-run","evidence":"Portal dependencies are not installed in this worktree."}]},"relatedDocuments":[{"id":"design.patterns.navigation-and-feedback","relation":"see-also"},{"id":"design.ai.agents-autonomy-and-runs","relation":"see-also"},{"id":"design.ai.evidence-and-status","relation":"see-also"}]}
---

# App Studio conversation and modes

App Studio's assistant is a durable project conversation, not an opaque chat
box. The thread projection keeps user messages and assistant message segments
as the public conversation. Progress, action activity, plans, approvals, and
errors belong to the assistant segment that owns them so a reload or reconnect
does not invent a second answer or lose the run state.

## Shipped contract

### Mode is authority

The response mode is explicit and fixed for a run:

- **Default** answers, inspects, or makes a requested change. The user's
  request determines whether the turn is inspection-only or may act.
- **Plan** investigates with bounded evidence and produces a decision-complete
  plan. It is read-only: it does not edit files, hydrate templates, restart or
  rebuild runtimes, provision infrastructure, commit, or imply implementation.
- **Review** is a separately scoped read-only execution over the current
  workspace and repository. It reports prioritized correctness, security,
  regression, durability, and missing-test findings; review text cannot grant
  mutation authority.

The server fixes the mode for the run. User wording or model output cannot
change it. The client may offer **Implement the plan** only for a completed,
error-free Plan run; selecting it explicitly switches the next turn to
Default and sends the implementation request. A plan card is not evidence that
implementation happened.

### Approval is a separate policy

Approval mode controls effectful actions independently of response mode:

- **Ask when needed** runs routine workspace, build, test, and lint actions and
  asks before consequential external effects.
- **Always ask** asks before actions that change state or invoke external
  operations.
- **Never allow** keeps the assistant read-only and rejects actions requiring
  approval.

When approval is requested, show the action disclosure and its current
permission state. If command details are unavailable or invalid, disable
**Allow** and require denial/retry; never fill in missing arguments from
memory or prose.

### Context is bounded and receipt-backed

User turns may select a bounded set of skill IDs, provider resource references,
annotations, and attachment receipts. The server validates those selections,
records the selected metadata with the turn, and retains attachment bytes in
the attachment store. Skill bodies and attachment/text contents are not part
of the public thread projection merely because a selection chip exists. Invalid
or stale references are dropped or rejected rather than rendered as trustworthy
context.

Thread and turn reads/writes are scoped to the authenticated user, organization,
workspace, and project. A reconnect must reconcile the server-owned durable
projection with the live stream by stable turn/message identity and revision;
it must not let an older response overwrite a newer segment.

## Retrieval guidance

For a conversation change, read this document together with
[resource reads](../patterns/resource-reads.md), the relevant component
contract, and [evidence and status](evidence-and-status.md). Keep user-facing
copy concrete about the authority granted, the action waiting, or the evidence
observed. Treat status, progress, and action activity as different disclosures;
do not collapse them into a generic “working” or “done” label.

## Explicit exclusions and gaps

These are not shipped generic App Studio design contracts:

- There is no generic confidence or uncertainty vocabulary. Use concrete
  states, sources, and missing evidence; do not invent a confidence score or
  badge.
- There is no generic assistant undo or reversal contract. Workspace history
  restore is a separate, explicit operation and must not be presented as an
  automatic undo for arbitrary assistant actions.
- There is no general generated-artifact provenance contract. Exact commit and
  release evidence is documented in [evidence and status](evidence-and-status.md),
  but it does not establish lineage for every model-generated output.
- Current raw run-error presentation is an implementation observation under
  privacy review, not normative content guidance. Do not copy an unsanitized
  backend error into a new conversation, approval, or notification surface.
