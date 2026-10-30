---
{"schema":1,"id":"design.ai.agent-overlay","title":"Workspace agent overlay prototype","kind":"pattern","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"Opt-in personal browser preference with a resizable compact chat and navigation to the actual Agents chat route. Context includes host provenance, an optional Infrastructure instance reference, and bounded provider-published JSON; it remains reference data and grants no authority."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/ai/agent-overlay.md#workspace-agent-overlay-prototype","role":"design"},{"path":"portal/src/components/AgentOverlay.vue","role":"implementation"},{"path":"portal/src/stores/agentOverlay.ts","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentChat.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentDetail.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/WorkspaceAssistantCard.vue","role":"implementation"},{"path":"portal/src/providers/agentContextPublisher.ts","role":"implementation"},{"path":"portal/src/providers/providerContext.ts","role":"implementation"},{"path":"portal/src/stores/pageResource.ts","role":"implementation"},{"path":"provider-sdk/portalkit/agent-context.ts","role":"implementation"},{"path":"providers/agents/portal/src/pageContext.ts","role":"implementation"},{"path":"providers/agents/portal/src/views/ChatMessage.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"test","ref":"PR2 host portal tests and production build","status":"passing","evidence":"447 host portal tests passed; portal typecheck and Vite production build passed on the PR2 branch."},{"kind":"test","ref":"PR2 Agents portal tests, typecheck, and production build","status":"passing","evidence":"813 Agents portal tests passed; vue-tsc and the production Vite build passed on the PR2 branch."},{"kind":"command","ref":"make verify-design-docs, make verify-portalkit, and make verify-ui-conformance","status":"passing","evidence":"All three shared gates passed after syncing the canonical asset manifest and generated copies."},{"kind":"browser","ref":"hack/agent-overlay-resize-test.mjs desktop/mobile light/dark preview fixture","status":"passing","evidence":"Synthetic context preview fit and scrolling passed at 1280×800 desktop and 390×600 mobile in light and dark themes, for compact and expanded layouts at low and high bottom offsets. This fixture does not validate a live host or provider backend."},{"kind":"browser","ref":"Branch-built host and Agents portal with live backend","status":"pending","evidence":"No branch-specific live acceptance is claimed. Existing browser evidence predates PR2 and remains historical."},{"kind":"browser","ref":"Historical pre-PR2 host and Agents browser fixtures","status":"not-run","evidence":"Historical evidence from the earlier overlay revision is not current validation for the provider-context preview changes."}]},"relatedDocuments":[{"id":"design.ai.agents-autonomy-and-runs","relation":"see-also","path":"docs/design/ai/agents-autonomy-and-runs.md"}]}
---

# Workspace agent overlay prototype

In **Models**, use the **Workspace assistant** card below the model list and above usage, choose an agent and page-context preference, then save. Choose Off to disable it. The launcher menu reopens the same settings. One selected
agent follows this user around the current workspace. The preference is stored
in this browser, keyed by user, organization, and workspace; it does not change
another user's experience or grant access to the agent.

The host mounts the existing Agents element above the router boundary. Its
`overlayState` property selects the compact conversation without owning browser
history. The floating launcher minimizes and reopens the retained conversation.
A top-left resize handle supports dragging and arrow keys; viewport limits keep
it on screen. Dragging disables the height transition and batches updates with
animation frames. Resize limits use the panel's computed CSS maximum/minimum
height and current available width, refreshed during the gesture. Reversing at
a limit responds immediately, without unwinding an oversized stored dimension.
Pointer cancellation, lost capture, hiding, and unmounting end the gesture.
**Open full chat in Agents** navigates to the ordinary provider
chat route. The floating window and launcher are both hidden while that agent's dedicated view is open. There is no fullscreen overlay or embedded full-provider mode.
The existing session is restored and active runs use the usual recovery path.
An explicit, short-lived in-memory handoff carries an unsent draft to that same
user, workspace, and agent. Saving Workspace assistant settings opens the bottom assistant bar.

## Bottom assistant bar

The default presentation is a 64px composer centered within the available main content width. Focusing the composer or choosing Expand grows the conversation upward; Collapse restores the bar using the same mounted composer and draft. Minimize shrinks the existing panel toward the Railgrid launcher in 240ms; reopening reverses the movement in 320ms. Reduced-motion users switch immediately. The chat stays mounted, and the minimized preference persists per user/workspace. The page reserves bottom clearance for the bar. On narrow screens the conversation becomes a bottom sheet. Expansion to Agents still navigates to the dedicated provider view.

## Compact layout

The floating surface uses one header with agent identity, navigation to the full
chat, an overflow menu, and minimize. Assistant settings and Turn off assistant live in the shared
ActionMenu. A single header icon toggles page context directly. Its pressed state and distinct on/off icons expose the state; a tooltip explains what is shared. The full explanation lives in Workspace assistant settings. The provider conversation header is omitted inside the overlay.

The shared AgentKit composer opts into compact geometry: a single line expands
with the draft up to the existing height limit, with send/stop beside the editor.
Empty compact-control positioning wrappers pass clicks through to the editor;
native and custom interactive descendants remain clickable. This lets manual
expansion accept a first click in the editor, without requiring focus in the bar.
Touch targets retain the larger coarse-pointer dimensions. The dedicated Agents
view keeps its existing conversation header and composer layout. After history loads while the overlay is hidden, opening it scrolls to the latest message once the transcript has measurable height. Subsequent resizing respects a user who scrolled to older messages.

The host owns positioning, shell/terminal clearance, selection, and page context.
On full-bleed workbenches, the assistant floats over the page without reserving
space or moving the underlying controls, in both compact and expanded states.
Overlap is intentional; minimize the assistant to access covered controls.
Ordinary scrolling pages retain their bottom clearance.
Agents owns conversation and run behavior. The bundle still loads through the
shared provider resolver and integrity-aware loader. Requests use an
Agents-scoped host transport with a synchronous authority fence. Workspace,
identity, access, or selected-agent changes retire the old mounted client;
minimizing and changing pages within the same workspace do not.

## Context and authority

The host attaches its workspace label, provider name, pathname, and provider
route fragment as provenance. A provider page may also publish one bounded
`ProviderAgentContext` through `railgridContext.publishAgentContext`. The
versioned contract accepts a title, optional summary, resource references, and
bounded JSON data. The host copies and freezes that value, then scopes it to the
current user, workspace, provider mount, and route epoch. Query-only navigation
also retires the page publisher; returning from A to B to A cannot revive a
callback from the first visit. Token refresh keeps a same-owner transport
usable, while an identity, access, workspace, provider, or mount change retires
it. The host constructs provenance outside the provider payload, so providers
cannot claim another route or workspace through the publisher.

The Infrastructure Instance resource pointer remains available on supported
detail pages and may identify the provider-local `get_instance` read tool. It
supports live investigation through the agent's configured tools; it does not
copy spec or status, or authorize an action. The host provenance omits query
parameters, DOM content, and form values. Provider-published JSON is normalized
and size-bounded, but the host does not detect credentials, secrets, logs, or
resource contents inside that JSON. Provider authors must publish only curated
reference data and must not include those values or commands. Invalid or
oversized provider input clears the current snapshot.

The overlay offers an exact serialized-envelope preview from the chat menu and
compact bar. It reports whether context is enabled for the next send and marks
provider-published fields as untrusted reference data. Pausing context excludes
the envelope from subsequent user messages while retaining earlier messages.
When enabled, context is appended to the user message at send time using the
provider's attended MCP chat tool. That tool supplies the caller-authorized
aggregate MCP connection; the Kubernetes chat subresource does not carry a
caller credential. The transcript renders saved context as a compact context
row. Expanding it reveals host provenance, provider summary, resource
references, and JSON data as escaped text. Malformed, oversized, or unknown
experimental envelopes remain verbatim in the message. Browsing alone starts
no model run.

Pausing context excludes it from subsequent sends; it cannot erase earlier
messages. Context is not system policy, a permission grant, or proof of the
resource's current state. The agent must inspect resources through its
configured tools and existing authorization rules.

## Prototype boundaries

- Selection persists on this browser only; there is no server-side preference.
- Workspace changes restore that workspace's selection and minimized preference, with the conversation collapsed to the bar when visible.
  Persisted sessions are recovered through the existing Agents session APIs;
  unsent drafts are not persisted across a workspace switch or browser reload.
- The header back arrow opens a searchable list of the selected agent’s threads. Selecting a thread or starting a new chat returns to the conversation; returning directly preserves the current draft and stream.
- Expansion navigates to the dedicated Agents chat view.
- Context identifies the current page and may include Infrastructure resource
  coordinates or a bounded provider summary, resource list, and JSON data.
  Selected rows, attachments, and background reasoning are outside this overlay
  contract.
- A missing/disabled provider hides the overlay. An incompatible bundle reports
  that an Agents update and page reload are needed. Resource loading, deletion,
  and denied reads retain the existing Agents read-state UI.
- Browser checks use mocked hub/model responses. They do not prove a live model
  call, live kcp authorization, or a production deployment.

## Focused regression checks

`make test-portal` includes fast unit coverage for resize deltas, minimum sizes,
viewport limits, and reversing after an overshoot. `make test-agent-ui-browser`
groups the composer hit-test and resize browser fixtures as **manual evidence
only**, not an automatic `make verify` or CI gate. Install the portal npm
dependencies and Playwright/Chromium before running; `PLAYWRIGHT_MODULE` and
`CHROMIUM_EXECUTABLE` can point to an existing installation. The resize fixture
checks its source-extraction markers explicitly and fails if the component's
structure changes. These fixtures do not establish live host/provider behavior.
