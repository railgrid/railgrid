---
{"schema":1,"id":"design.ai.agent-overlay","title":"Workspace agent overlay prototype","kind":"pattern","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"Opt-in personal browser preference with a resizable compact chat and navigation to the actual Agents chat route. Context contains bounded navigation metadata and an optional Infrastructure instance reference; resource contents and background reasoning are not implemented."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/ai/agent-overlay.md#workspace-agent-overlay-prototype","role":"design"},{"path":"portal/src/components/AgentOverlay.vue","role":"implementation"},{"path":"portal/src/stores/agentOverlay.ts","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentChat.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentDetail.vue","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"test","ref":"make build-agents-provider-portal","status":"passing","evidence":"718 Agents tests and production build passed after rebasing onto current main; the review revision also passes the shared UI and synchronization gates."},{"kind":"test","ref":"make test-portal","status":"passing","evidence":"389 host tests passed, including resource-publisher validation/fencing and overlay-store persistence regressions; host typecheck passed. PortalKit parity, UI conformance, and design-doc gates passed."},{"kind":"browser","ref":"Host portal and built Agents bundle with mocked APIs","status":"passing","evidence":"Re-run on the post-rebase review revision with real host/provider UI and mocked APIs: light/dark desktop (1440px) and touch mobile (390px), Models settings, disclosure, bar expansion/collapse, minimized reload, threads, draft preservation, repeated settings focus, and stable provider context/fetch identity across navigation. This browser run does not establish live model behavior."}]},"relatedDocuments":[{"id":"design.ai.agents-autonomy-and-runs","relation":"see-also","path":"docs/design/ai/agents-autonomy-and-runs.md"}]}
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
it on screen. **Open full chat in Agents** navigates to the ordinary provider
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
Touch targets retain the larger coarse-pointer dimensions. The dedicated Agents
view keeps its existing conversation header and composer layout. After history loads while the overlay is hidden, opening it scrolls to the latest message once the transcript has measurable height. Subsequent resizing respects a user who scrolled to older messages.

The host owns positioning, shell/terminal clearance, selection, and page context.
Agents owns conversation and run behavior. The bundle still loads through the
shared provider resolver and integrity-aware loader. Requests use an
Agents-scoped host transport with a synchronous authority fence. Workspace,
identity, access, or selected-agent changes retire the old mounted client;
minimizing and changing pages within the same workspace do not.

## Context and authority

The prototype attaches workspace label, provider name, pathname, and a provider route fragment. Infrastructure instance detail pages also publish a bounded resource hint: API version, kind, resource plural, name, UID, and the provider-local read tool `get_instance`. This is an identity for live investigation through the agent’s configured tools, not copied spec/status or permission to execute actions. The host exposes publishing only to Infrastructure and accepts only its Instance coordinates and optional `get_instance` tool hint. It also fences callbacks to their mounted provider, route, and workspace; failed reads and navigation clear the hint. Query parameters,
DOM content, form values, credentials, logs, and secret values are excluded.
Context is serialized and appended to the user message as labeled reference
data at send time, using the provider's attended MCP chat tool. That tool supplies the caller-authorized aggregate MCP connection; the Kubernetes chat subresource does not carry a caller credential. The transcript renders this saved context as a compact context row beneath the message, with a page icon, readable label, and disclosure chevron. Expanding it reveals the workspace, full page label, exact location, and what was shared. The row has no separate card background; long labels truncate and remain available in the expanded details. Malformed references remain verbatim text. Browsing alone starts no model run.

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
- Context identifies the current page and, on Infrastructure instance details, the resource and read tool. Provider-published resource summaries,
  selected rows, attachments, and background reasoning are future work.
- A missing/disabled provider hides the overlay. An incompatible bundle reports
  that an Agents update and page reload are needed. Resource loading, deletion,
  and denied reads retain the existing Agents read-state UI.
- Browser checks use mocked hub/model responses. They do not prove a live model
  call, live kcp authorization, or a production deployment.
