# Agents portal

Vite + TypeScript + [Vue 3](https://vuejs.org) micro-frontend for the agents
provider, mounted in the railgrid portal under `/ui/providers/agents/`. The Go
binary embeds `portal/dist` via `assets.go`.

```
npm install
npm run build      # → portal/dist/main.js (single IIFE bundle, embedded at go build time)
npm run typecheck
npm test
```

## Host contract

There is **no iframe and no postMessage**. The host
(`portal/src/pages/ProviderFrame.vue`) injects `/ui/providers/agents/main.js`,
which registers the custom element `railgrid-provider-agents`, then appends the
element and assigns a `railgridContext` **property** on it:

```ts
el.railgridContext = { subPath, token, user, tenant, orgUUID, workspaceUUID, theme, basePath }
```

The element renders in **light DOM**, so the portal's `:root` design tokens
cascade in and light/dark themes match without any extra plumbing. Its own
stylesheet (`src/style.css`) is injected once, with every selector namespaced
under `railgrid-provider-agents`.

Tenant objects are read and written straight against kcp on the hub's
`/clusters/<tenant>/…` front door (`src/resources.ts`, portalkit's kube client).
Provider verbs — chat, run, trace, test, … — are kcp custom subresources on
those same objects, addressed with `kubeVerbPath` from `src/portalkit/kube.ts`
(`/clusters/<tenant>/apis/agents.railgrid.ai/v1alpha1/<resource>/<name>/<verb>`)
and fetched through the host-owned transport, which injects the bearer. Only
`/oauth/providers` and `/mcp` still go to `basePath` with `/ui/providers/`
rewritten to `/services/providers/` (the hub's service proxy) — see
`src/portalkit/tenant.ts`. The host context is authoritative for the tenant; the
localStorage copy is only a fallback.

Navigation state lives in `location.hash` (`src/router.ts`), never in the host
router.

## Chat model and reasoning

The conversation composer uses AgentKit's shared `AIModelReasoningPicker`, as
App Studio does. Model and effort choices apply only to the current chat.
`GET/PATCH agents/{name}/session/{sessionID}` reads and saves these preferences;
it never patches the Agent or its ModelCredential. Each run pins the effective
choice so approval resumes and recovery retain it. New chats, background runs,
and spawned workers continue to use the Agent defaults. Explicit selections
require access to the selected ModelCredential when saved and when a new chat
run starts.

Session reads wait for pending writes to that same chat, including across view
remounts. An unsent chat with saved preferences remains selected after reload.
Chats that inherit the Agent default refresh their model and effort options when
that default changes; explicit chat choices stay fixed. Model selection is
disabled while a preference request is pending, without closing the reasoning
picker.

Effort choices come from the shared server-side model capability catalog.
Custom endpoints and unknown models show only model selection. On the official
OpenAI endpoint, Agents uses Eino's native Responses model for the GPT-6 family
and for selected earlier catalog models when a reasoning choice requires it.
Responses routing is limited to that endpoint; custom gateways use Chat
Completions by default unless a run resumes with a pinned protocol. This support
does not extend to every model served only by Responses. Other supported models
use Chat Completions. Unless a reasoning effort is explicitly selected,
Responses requests omit the reasoning field. In particular, GPT-6 Luna no
longer gets an implicit `none`; the model uses its own default. The run records
its transport and effective reasoning effort for approval resumes and recovery.

Claude effort is available only for documented Claude model IDs on Anthropic's
official Messages endpoint. Agents uses Anthropic's native
`output_config.effort`, not the OpenAI-compatible `reasoning_effort` field.
Effort is remembered separately for each credential in a chat.
Switching back to a credential whose remembered effort is no longer supported
resets that effort to model default; valid remembered choices are retained.

The first user message also starts a separate metadata request to generate a
thread title with the model selected for that chat. Title generation runs
asynchronously and does not delay the reply. When the saved title arrives, the
conversation header and thread rail update together; if generation fails, the
existing first-message preview remains the label.
The title attempt is one-shot: failures, timeouts, invalid output, or a temporary
store error leave the preview in place, and later turns do not retry.

Saved model preferences make an empty chat eligible for the thread list before
its first message. The list shows the newest 100 conversations and newest 100
empty chats independently; older entries remain stored. Deleting any one saved empty
chat frees a preference slot. Reload after deleting visible drafts to reveal
older drafts outside the displayed 100. Opening a new chat never silently evicts
another chat’s saved choices.

## Harness message presentation

Harness commentary uses the same ordered activity timeline as model-backed
Agents. Native message boundaries retain paragraphs and Markdown, completed
commentary precedes the commands it introduces, and the final answer stays in
its own answer body. Codex item IDs prevent repeated completed messages from
being displayed again after reconnect or checkpoint recovery. Legacy runners
without native message boundaries retain line breaks in live progress.

## Active run attachment

Chat automatically follows an active run after its original request disconnects,
a conversation is reopened, or an approval resumes through the inbox. While the
request stream is detached, the portal reads progress once per second, with
backoff on connection failures. Partial harness text and running commands remain
visible; completed rows share IDs with the transcript to prevent duplication.
The observer stops on completion, navigation, or lost access. A connection failure
does not cancel or restart the run, and Stop remains available while reconnecting. A Stop
request keeps observation active until the runner confirms a terminal state, even
when lifecycle events are unavailable.

Run status, trace, history, inbox, and approval decision requests have a 15-second
request budget, including response bodies and transports that ignore cancellation.
A stalled read enters the existing reconnect path. A stalled decision checks saved
inbox state before releasing its controls; it never automatically repeats the decision.

Question replies retain their admitted run as a reconnect target if the answer
stream closes before acknowledgement, even when inbox verification is unavailable.
Their acknowledgement has a 30-second deadline; the deadline is removed after
`start`, so a long continuation is not cut short. Recovery observes the existing
run and never submits another answer or chat request.
If an initial chat acknowledgement is lost, ambiguous failures discover active
runs in the selected session or reload a response that already completed. Explicit
admission refusals do not trigger this recovery. The unacknowledged draft is kept
because the client cannot prove which submission was accepted. Transient discovery
failures retry within that session across workbench tabs; thread and authority
changes invalidate those retries.
An admitted stream with no events for 15 seconds switches to the independent run
observer. Terminal lifecycle events release stalled streams immediately, except
while a Stop request is settling. Neither path cancels or resubmits execution.
Both Kubernetes resource errors and verb errors stop observation on lost access. Final
history hydration retries transient failures after run observation closes, including
when a terminal event races with a delayed Stop response. These retries survive
navigation within the same agent workbench, but stop on a thread or authority
change or lost access. New prompts pause terminal recovery until admission;
refusals resume it, including after a provisional start. A refused new prompt
also restores the previous active run observer. Lifecycle events invalidate older
progress reads so stale snapshots cannot reopen a consumed approval or hide a
new approval during a disconnect. Successful approval-detail refreshes also
invalidate older progress snapshots, including when events are unavailable.
Superseded progress errors cannot stop an observer that has newer state.
Approval and progress readers share a snapshot-acceptance boundary in both
directions, so an older approval read cannot restore a consumed permission or
question after newer progress was accepted. A fresh approval disclosure clears
an earlier access notice and restarts stopped progress observation, even without
lifecycle events. Metadata-only fallbacks do not prove trace access has returned.

Active runs use the composer primary action for Stop, including after a reload,
reconnect, or before the first assistant message. The same button cancels the
existing run and remains disabled while stopping until terminal confirmation.
The progress status retains View run. Drafts remain editable and are preserved;
Enter does not submit another prompt while the selected conversation is active. Notices appear only when progress
is reconnecting or a run lookup fails. Normal attachment has no background banner.

## Permission requests

Permission requests use the same card and composer dock pattern as questions.
Chat docks the newest unresolved request above the composer and leaves a receipt
in its original turn after approval or denial. Run details and Activity use the
same `AgentApprovalCard`. Command requests show the command, working directory,
and reason, with the complete server disclosure available under Full request.
Generic tools show their exact name and disclosed inputs. Missing or malformed
inputs disable approval; denial remains available. Decisions use the existing
inbox endpoint and retain its ownership, single-flight, and recovery checks.

## MCP Apps result views

Successful tool results may include a provider-owned MCP App view. The portal
keeps the app inside the conversation as a compact card and opens that same
result in a details pane when requested. The app owns its rich result UI; the
portal offers raw captured JSON only through an explicit disclosure. Source
references are attached to a citation only when the exact successful tool
receipt is present, and source-backed bytes or app HTML are requested again
through the attended MCP API before either view is mounted. Capture metadata is
the only artifact state cached by the browser.

Provider apps are default-deny: the attended backend supplies an independent
provider, resource, and content pin only after its operator-reviewed allowlist
and current resource checks pass. The portal requires that pin to match the app
metadata and verifies the exact HTML bytes before creating the frame. A missing
or mismatched pin leaves the authorized raw result available through the
explicit data disclosure, but never mounts app HTML. Only enable reviewed
providers whose apps are trusted with the full result and tool input after limited credential-like key-name filtering.
That filter cannot guarantee removal of secrets from arbitrary values.

This is a read-only host profile. It sends the recorded result text and
recorded arguments after limited credential-like key-name filtering to the iframe. The host grants no tools, resource
reads, prompts, sampling, downloads, chat messages, host navigation, or model
context updates, and rejects requests for those capabilities. App HTML must
match the independent backend-reviewed SHA-256 pin. The host inserts a restrictive CSP
and uses nested frames with opaque origins, scripts-only sandbox flags, no
referrer, and no host credentials. These controls are defense in depth, not a
claim that arbitrary HTML is harmless: a sandboxed app can still attempt to
navigate its own frame, and a later navigated document may make network
requests. App HTML must therefore be treated as provider-controlled code that
can see the complete disclosed result. Do not include secrets in app HTML or
captured results; only enable reviewed providers and account for the residual
own-frame navigation and network-request risk.

The static sandbox proxy is currently served only from the platform Agents
bundle path (`/ui/providers/agents`). The host context exposes the same
`basePath` for platform and org-owned bundles, so the portal checks the actual
Agents script URL and refuses to load the platform proxy when that URL carries
the host's `grant` query parameter. Org-owned Agents overrides need a
grant-aware auxiliary asset URL before MCP App frames can be enabled there.

## Layout

```
src/
  main.ts               entry: registers the custom element + injects style.css
  element.ts            thin <railgrid-provider-agents> Vue mount boundary
  App.vue               nav, routing, authority rotation, and store lifecycle
  api.ts                typed REST client + a spec-correct SSE reader
  store.ts              Slice<T> {data, loading, error} collections + /api/events subscription
  mutate.ts             the one write helper (optimistic → request → toast → refresh)
  router.ts             hash routes for the four tabs
  types.ts              entity + write DTO types, formatters
  conn-defs.ts          type-driven connection setup guides + shape/inbound derivation
  portalkit/            synced Vue kit + shared tenant/style helpers — edit upstream, not here
  ui/                   framework-neutral toast integration
  vue/                  Vue runtime bridge + framework-neutral chat projection helpers
  views/                Vue components for each surface (agents, agent config/chat, activity,
                        run detail, connections, toolsets, models, automation)
  test/                 vitest component + store tests
```

Four tabs: **Agents** · **Activity** · **Connections** (toolsets are a section)
· **Models**. Schedules and triggers are owned by their agent and edited in the
agent's Config pane, next to a live chat playground.

### Skills surfaces

The main navigation’s Skills library (`#/skills`) owns reusable package creation, import/export, editing, deletion, and read-only provider previews. `SkillsLibrary.vue` reuses the existing package editor and `AISkillCatalogList`; `skill-library.ts` reads Skill CRs and published provider packages without requiring an Agent. These library source-package keys are not execution IDs.

An Agent’s `#/agents/<name>/skills` workbench uses `AgentSkillActivation.vue` for enabled/available lists and missing-reference removal. Activation is absent from Config. Open library carries a return destination; browser navigation and the return link preserve the Agent’s chat/workbench context. Runtime availability and composer `/skill` selection continue to use the authorized per-Agent catalog.
