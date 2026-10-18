# AgentKit — optional AI presentation

AgentKit provides the shared conversation, workbench, and model UI used by
App Studio and Agents. App Studio is the visual reference. AgentKit depends on
PortalKit tokens and general controls; PortalKit has no AgentKit dependency.

## Ownership and imports

- `provider-sdk/agentkit-vue/`: canonical Vue components and neutral view types.
- `provider-sdk/agentkit/`: canonical CSS and `ensureAgentUIStyles()`.
- `provider-sdk/portalkit{,-vue}/`: general resource, form, navigation, status,
  confirmation, and tenant primitives.
- Provider adapters: transport, credentials, validation, Markdown sanitization,
  stream projection, authoritative run state, queues, attachments, and persistence.

Import individual components from the provider's generated `src/agentkit/`
folder. The SDK deliberately has no eagerly importing component barrel.
Canonical Vue components import their AgentKit peers locally and shared styles
from `../agentkit/styles`. Dependencies on canonical core Vue components use
`../portalkit-vue/`; the sync manifest explicitly rewrites the declared core
component paths to `../portalkit/` in distributed copies and verifies the result.

## Distribution

Providers remain self-contained Vite builds, including standalone Docker build
contexts. This extraction does not introduce an npm workspace, registry package,
or runtime dependency on another provider.

`hack/sync-portalkit.sh` declares the optional consumers in `AGENTKIT_PORTALS`:
App Studio and Agents. The same script maintains both libraries:

```sh
make sync-portalkit
make verify-portalkit
```

To add a consumer, register its portal in that explicit list and verify its
build. New canonical files must be registered in the corresponding AgentKit
manifest. The verifier rejects missing files, stale copies, unexpected files,
and AgentKit directories in nonconsuming portals. Removed AI/model PortalKit
copies are cleaned using an exact filename allowlist; unrelated files are never
recursively deleted.

Source distribution and browser payload are separate concerns: only imported
Vue modules enter a provider build, while a referenced inline stylesheet enters
that bundle's fallback payload. Splitting the stylesheet therefore matters even
when unused Vue components can be eliminated by the bundler.

## Styles

`ensureAgentUIStyles()` first ensures core PortalKit styles, then checks
`--railgrid-agent-ui-canonical: 1` and `--railgrid-agent-ui-version`. Its fallback uses
`k-agent-ui` or a versioned ID and never rewrites an existing style element.

Core PortalKit uses `--railgrid-ui-core-version: 36` (the
`RAILGRID_UI_CORE_VERSION` constant in `../portalkit/styles.ts`); the current
AgentKit presentation style/runtime version is 24 (the `AGENT_UI_VERSION`
constant in `styles.ts`), and the versioning remains independent. PortalKit
does not import AgentKit or include its optional styles.
CSS selectors remain `k-ai-*` and `k-model-*`, preserving the shared visual
vocabulary across providers. Deploy the host and its providers from the current
split together; legacy full-UI bundles are outside this migration's
compatibility scope.

## Contracts

Conversation chrome, transcript/message frames, activity rows, composer and
interrupt frames, primary actions, rails, workspace splits, and workbench tabs
are shared. Model connection cards/forms/selectors and usage-section framing
also belong here; model APIs and credential handling remain provider-owned.

`AIModelReasoningPicker.vue` combines configured model choice with the caller's
exact ordered reasoning options in one compact control. The caller supplies
`''` as the model-default/reset value and the discrete `effortOptions`; the
component previews while the range moves and emits `update:effort` only on
commit. It keeps persistence, per-model preferences, capability checks, and
recovery policy with the provider, and uses `useAnchoredPopover` for its
viewport-aware panel.

Composed turns, progress disclosure, grouped/flat activity feeds, safe execution
details, and validated plan presentation build on
these primitives. Providers supply status and formatted duration: AgentKit does
not start clocks, infer plans from prose, or turn unknown outcomes into success.
The execution detail frame accepts a neutral heading, labeled input, and an
output label so generic tool results use the same presentation as Studio shell
evidence without inventing command metadata. Provider adapters supply readable
row labels and keep exact tool identifiers in the details.
App Studio remains the visual reference; provider adapters own timing, status,
phase, and authority decisions.
`AITimestamp.vue` and `timestamp.ts` render a provider-owned valid timestamp as
semantic time with a relative label and full locale value on hover, focus, or
click; missing or invalid values are omitted. `AITurnProgress` inspects its
details slot at render time, so an empty slot has no disclosure affordance and a
later trace update can expose the details.
Rich composer commands, attachment receipts, project previews, and specialized
configuration/run workbenches stay with their provider.

`AIWorkbenchTabs.vue` keeps tab frames at their natural width up to 240px and
scrolls the strip when it overflows. Previous and next controls appear only
when needed; selecting a tab keeps it visible without moving focus. The
component preserves provider keyboard, drag, and close events.
`AIWorkbenchLauncher.vue` supplies the searchable tool picker used by Agents and
App Studio. Providers supply admitted items and own tab state, routes,
persistence, and reorder policy. Closing a tab is a presentation action, not
resource deletion.

## Pane motion

AgentKit owns pane motion through `--k-ai-pane-enter-duration` (280ms),
`--k-ai-pane-leave-duration` (190ms), and `--k-ai-pane-easing`. The shared
conversation rail, `AIWorkspace` right pane, and App Studio's custom split layout
consume these tokens. Keep layout-specific transitions with their components;
do not add independent provider timing settings. Resizing remains immediate and
pane transitions respect `prefers-reduced-motion`.

Conversation transcripts and composer docks share `--k-ai-conversation-content-width`
(820px). Apply `k-ai-composer-dock` to the composer form so its composer, queued
messages, and inline prompts stay aligned with the transcript at every pane width.

`AIWorkbenchPane` accepts `bodyOverflow="visible"` when its parent owns scrolling.
Use `k-ai-composer__actions--fill` for an actions group that fills the available controls row.
