---
{"schema":1,"id":"design.ai.agent-overlay","title":"Workspace agent overlay prototype","kind":"pattern","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"Opt-in personal browser preference with a resizable right-side chat panel and navigation to the actual Agents chat route. Context includes host provenance, an optional Infrastructure instance reference, and bounded provider-published JSON; it remains reference data and grants no authority."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/ai/agent-overlay.md#workspace-agent-overlay-prototype","role":"design"},{"path":"portal/src/components/AgentOverlay.vue","role":"implementation"},{"path":"portal/src/stores/agentOverlay.ts","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentChat.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentDetail.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/WorkspaceAssistantCard.vue","role":"implementation"},{"path":"portal/src/providers/agentContextPublisher.ts","role":"implementation"},{"path":"portal/src/providers/providerContext.ts","role":"implementation"},{"path":"portal/src/stores/pageResource.ts","role":"implementation"},{"path":"provider-sdk/portalkit/agent-context.ts","role":"implementation"},{"path":"providers/agents/portal/src/pageContext.ts","role":"implementation"},{"path":"providers/agents/portal/src/views/ChatMessage.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"test","ref":"PR2 host portal tests and production build","status":"passing","evidence":"447 host portal tests passed; portal typecheck and Vite production build passed on the PR2 branch."},{"kind":"test","ref":"PR2 Agents portal tests, typecheck, and production build","status":"passing","evidence":"813 Agents portal tests passed; vue-tsc and the production Vite build passed on the PR2 branch."},{"kind":"command","ref":"make verify-design-docs, make verify-portalkit, and make verify-ui-conformance","status":"passing","evidence":"All three shared gates passed after syncing the canonical asset manifest and generated copies."},{"kind":"browser","ref":"hack/agent-overlay-resize-test.mjs desktop/mobile light/dark preview fixture","status":"passing","evidence":"Synthetic context preview fit and scrolling passed at 1280\u00d7800 desktop and 390\u00d7600 mobile in light and dark themes, for compact and expanded layouts at low and high bottom offsets. This fixture does not validate a live host or provider backend."},{"kind":"browser","ref":"Historical pre-PR2 host and Agents browser fixtures","status":"not-run","evidence":"Historical evidence from the earlier overlay revision is not current validation for the provider-context preview changes."},{"kind":"test","ref":"Side-panel host and Agents suites","status":"passing","evidence":"451 host tests and 814 Agents tests passed, with both production builds and typechecks."},{"kind":"command","ref":"Side-panel design and distribution checks","status":"passing","evidence":"make verify-design-docs, make verify-portalkit and make verify-ui-conformance passed with zero UI conformance violations."},{"kind":"browser","ref":"Live Tilt side-panel acceptance","status":"passing","evidence":"Live backend with temporary development agents: focus and typing stay compact; submit opens the panel; pointer and keyboard resize, draft retention, provider-route replacement, full-bleed layout, all navigation dock modes, minimize/reopen and reduced motion passed. Desktop and 390px mobile were checked in light and dark themes. Screenshots: artifacts/screenshots/agents-side-panel-{light,dark,mobile-light,mobile-dark}.png. No successful model response was exercised because this fresh workspace has no model connection."}]},"relatedDocuments":[{"id":"design.ai.agents-autonomy-and-runs","relation":"see-also","path":"docs/design/ai/agents-autonomy-and-runs.md"}]}
---

# Workspace agent overlay prototype

In **Models**, use the **Workspace assistant** card below the model list and above usage, choose an agent and page-context preference, then save. Choose Off to disable it. The launcher menu reopens the same settings. One selected
agent follows this user around the current workspace. The preference is stored
in this browser, keyed by user, organization, and workspace; it does not change
another user's experience or grant access to the agent.

The host mounts the existing Agents element above the router boundary. Its
`overlayState` selects a chat-only presentation without owning browser history.
The same conversation, composer draft, run, approvals, and tool results survive
opening, closing, resizing, and navigation within a workspace. The panel has no
thread rail or configuration workbench. **Open in Agents** hands the
conversation and unsent draft to the ordinary agent route. The assistant hides
while that agent's dedicated view is open.

## Bottom assistant bar

The default presentation is a 64px composer (80px on touch devices) centered within the available main
content width. Focusing or typing does not open the panel. A valid submission
opens the conversation from the right; empty or disabled submissions do not.
**Expand conversation** also reopens the retained chat without sending. Closing
the panel restores the compact composer and focuses it without losing the draft.
Minimize retains the conversation behind the Railgrid launcher.

## Right side panel

On desktop the panel fills the height remaining between navigation docks and starts at 440px wide. Dragging
its left edge or using Left/Right arrow keys resizes it; Home and End select its
width limits. The usual minimum is 320px, and the maximum is 60% of the width
remaining after navigation, while preserving at least 360px for main content.
When those minima cannot both fit on desktop, the main-content floor takes
precedence and the panel may be narrower than 320px. Navbar expansion and
collapse recalculate the limit from its measured width. Reversing direction at either limit
moves immediately. Pointer cancellation, lost capture, hiding, and unmounting
end the gesture. Resizing the browser clamps the visible width.

The host reserves the panel's width in the main content track, including the
provider/frame mount and full-bleed workbenches. Navigation owns the outer frame:
top and bottom docks span the full width, and a right dock sits outside the chat.
The panel follows the measured height of horizontal navigation.
Floating navigation is centered and clamped within the remaining workspace;
terminal insets include the panel. Below 768px the chat takes the full viewport
width remaining inside navigation, with the background content and terminal inert
until it closes; navigation remains available.

One header holds agent identity, page-context toggle, options, and close.
**Open in Agents** and **Hide assistant** live in the options menu. Closing returns
to the compact composer; hiding retains the conversation behind the launcher.
The resize edge uses the same AgentKit pane-divider styling and multi-dot grip
as the conversation/workbench divider. The expanded view uses the full AgentKit composer;
the collapsed bar uses its compact geometry. Model-help controls appear in the
expanded chat so their extra row cannot clip the compact composer border. Thread navigation and agent
configuration remain available in the dedicated Agents view. Context preview
remains bounded and scrollable in both presentations.

The bundle loads through the shared provider resolver and integrity-aware loader.
Its host transport retains the synchronous authority fence: workspace, identity,
access, or selected-agent changes retire the mounted client. Presentation changes
and page navigation do not replace the client or interrupt streaming.

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

## Model recovery

An idle model-backed overlay checks its remembered selection against the loaded
model collection. When that selection is missing, a recovery panel replaces the
whole composer: no empty model picker, reasoning controls, or disabled send
button. The transcript stays visible and the unsent draft stays in memory.
An active reply keeps its Stop control until work finishes.

Loading, failed reads, forbidden reads, missing configuration, and available
replacement choices are separate states. Replacement selection is explicit and
updates only this conversation. Setup access is checked with Kubernetes
SelfSubjectAccessReview; permission checks read no Secrets. Users without setup
access receive administrator guidance, and a failed access check offers retry.

Connect model opens Add model, where saved provider connections and the Connect
provider link remain available. The host collapses the overlay without replacing
its mounted conversation and shows Return to assistant. Returning refreshes the
model list and restores the expanded conversation and its draft. This flow does
not automatically select a newly connected model, send a message, or change the
Agent's workspace configuration.

The compact assistant bar also keeps recovery visible: it shows a short status
and Review setup, which expands the full recovery panel. The same recovery
states replace the dedicated Agents chat composer. In Config, a missing primary
model or fallback is explicitly marked unavailable, and invalid references
cannot be saved as healthy configuration. A stale or failed credential read is
not presented as proof that a model was deleted.

Dedicated chat setup retains the original thread and draft in the current
identity's AppStore. Both cancel and successful model creation return to that
conversation; setup does not assign the new model automatically. Draft handoff
is consumed on return and cleared when the store's authority is retired.

## Prototype boundaries

- Selection persists on this browser only; there is no server-side preference.
- Workspace changes restore that workspace's selection and minimized preference, with the conversation collapsed to the bar when visible.
  Persisted sessions are recovered through the existing Agents session APIs;
  unsent drafts are not persisted across a workspace switch or browser reload.
- The side panel contains only the current conversation; use the dedicated Agents view to select or create threads.
- **Open in Agents** in the options menu navigates to the dedicated Agents chat view.
- Context identifies the current page and may include Infrastructure resource
  coordinates or a bounded provider summary, resource list, and JSON data.
  Selected rows, attachments, and background reasoning are outside this overlay
  contract.
- A missing/disabled provider hides the overlay. An incompatible bundle reports
  that an Agents update and page reload are needed. Resource loading, deletion,
  and denied reads retain the existing Agents read-state UI.
- Historical fixtures use mocked responses. Current live Tilt checks exercise
  real provider mounts and submission without a configured model. They do not
  prove a successful model turn or a production deployment.

## Focused regression checks

`make test-portal` includes fast unit coverage for resize deltas, minimum sizes,
viewport limits, and reversing after an overshoot. Side-panel publication clears
on authority changes, and navigation insets include its width.
`make test-agent-ui-browser`
groups the composer hit-test and resize browser fixtures as **manual evidence
only**, not an automatic `make verify` or CI gate. Install the portal npm
dependencies and Playwright/Chromium before running; `PLAYWRIGHT_MODULE` and
`CHROMIUM_EXECUTABLE` can point to an existing installation. The resize fixture
checks its source-extraction markers explicitly and fails if the component's
structure changes. These fixtures do not establish live host/provider behavior.
