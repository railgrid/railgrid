---
{"schema":1,"id":"design.foundations.provider-integration","title":"Provider visual integration and PortalKit distribution","kind":"policy","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Trusted platform providers use the host-compiled or self-contained integration modes and share the canonical stylesheet through the sync script and version 36 core style handoff; standalone fallbacks remain source-aligned. Org-owned BYO UI runs in an opaque-origin sandbox with a host-verified bundle, frame-local stylesheet payload, resolved-theme context, and no login token. Hosted BYO enrollment is intentionally gated pending supported pinned-KCP ResourceQuota accounting. The hosted BYO enrollment gate UI has live Safari evidence; authored BYO bundle/install acceptance remains not-run, and generic provider full-bleed live provider/backend acceptance also remains pending."},"appliesTo":["provider-portals","portalkit","portal"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/provider-integration.md#provider-visual-integration-and-portalkit-distribution","role":"design"},{"path":"hack/sync-portalkit.sh","role":"implementation"},{"path":"provider-sdk/portalkit/styles.ts","role":"implementation"},{"path":"portal/src/pages/ProviderFrame.vue","role":"implementation"},{"path":"providers/agents/portal/src/App.vue","role":"implementation"},{"path":"hack/verify-ui-conformance.mjs","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Current byte-for-byte PortalKit and AgentKit copy and manifest parity passed."},{"kind":"command","ref":"make verify-ui-conformance","status":"passing","evidence":"Current UI conformance scanned 463 files with zero violations and passed all 32 tests."},{"kind":"browser","ref":"Historical fixture: Core and provider rendered fixture matrices","status":"passing","evidence":"Historical evidence: six core and six provider dark/light desktop/mobile/hybrid cases passed with no captured errors or horizontal overflow; actual fonts loaded. This fixture evidence does not establish all routes, live Tilt, or assistive-technology verification."},{"kind":"browser","ref":"Historical fixture: Auth, Dex, and host hybrid fixtures","status":"passing","evidence":"Historical evidence: Auth, Dex, and host hybrid fixtures passed with the scoped rendered checks; these fixtures do not establish all routes, live Tilt, or assistive-technology verification."},{"kind":"browser","ref":"Generic provider full-bleed live acceptance","status":"not-run","evidence":"Live acceptance was blocked before application startup: the kind kubelet exhausted inotify watchers (inotify_init: too many open files), so the application backend did not start and no screenshots were obtained. Supplemental mock-browser checks recorded 72 rendered-shell and route-transition assertions separately; they do not establish or pass live provider/backend acceptance."},{"kind":"browser","ref":"Org-owned BYO provider browser acceptance","status":"not-run","evidence":"No authored-provider bundle install, rendering, or runtime acceptance was performed; the live checks below cover only the hosted enrollment gate UI."},{"kind":"browser","ref":"Hosted BYO enrollment gate UI (native Safari, 2026-11-01)","status":"passing","evidence":"Live Safari against the local stack after all seven native providers were Provisioned and catalog-ready, after accepting the local TLS certificate. Self-Hosting showed a readable closed-enrollment notice, all seven Self-host buttons were disabled, and Register was absent. Direct /settings/providers/new pending, resolved, and retry states kept name/cluster/Register disabled; Cancel returned to the prior page. In the default personal workspace, Disable controls remained available (not clicked). On ordinary local test workspace `48940e07-d618-41d8-ad06-1fb21a32f9cf`, platform Quickstart Enable and consent remained active and were canceled without enabling; Getting started Edges Enable opened and was dismissed with Escape, issuing no grant. Light/dark checks used desktop capture 1820x1328; zoom checks used Safari 300% page zoom. Safari native resizing did not provide a 390px physical viewport, so no mobile-width result is claimed. At 300% the notice wrapped and the lower form scrolled to the disabled submit and active Cancel without horizontal clipping. No existing org-owned providers were present, so org-row Remove/Upgrade behavior and live BYO frames were not observed. API proof at /private/tmp/railgrid-byo-live-ui-rqz7nd33/runapi-proof.json records 503 for register/mint/rotate and no org-providers parent. Runtime readiness used one owned Edges resource created after init; no source harness changes. Screenshots: artifacts/screenshots/byo-gate-self-hosting-light.jpg (closed notice and disabled controls, light), artifacts/screenshots/byo-gate-self-hosting-dark.jpg (same, dark), artifacts/screenshots/byo-gate-self-hosting-zoomed-light.jpg (300% zoom, light), artifacts/screenshots/byo-gate-self-hosting-zoomed-dark.jpg (300% zoom, dark), artifacts/screenshots/byo-gate-register-dark.jpg (direct create route at normal zoom, dark; no normal-zoom light capture), artifacts/screenshots/byo-gate-register-zoomed-light.jpg (create form at 300% zoom, light), artifacts/screenshots/byo-gate-register-zoomed-dark.jpg (300% zoom, dark; lower form shows hosting-cluster area, disabled Register provider, and active Cancel), artifacts/screenshots/byo-gate-platform-consent.jpg (active platform provider consent dialog)."},{"kind":"browser","ref":"Provider status recovery template fixture","status":"passing","evidence":"The ProviderFrame status template, computed state, and retry handler passed keyboard retry, pending/error/recovered states, same-organization request, and overflow checks at 1440px and 390px in light and dark themes with a mocked provider catalog. This does not establish live backend recovery."}]},"relatedDocuments":[{"id":"design.components.portalkit-assets","relation":"see-also","path":"docs/design/components/portalkit-assets.md"}]}
---

# Provider visual integration and PortalKit distribution

Trusted platform providers have two integration modes with one look. Their
custom elements render in the host document's light DOM, so host tokens and
cascading styles cross the element boundary. Self-contained platform bundles
namespace their own selectors under railgrid-provider-{name}; that boundary
keeps local rules from leaking while preserving the host-token contract.

1. **Host-compiled** (Infrastructure): `.vue` and `.ts` files are included in
   the host Tailwind scan through `@source` in `main.css`. Utilities, tokens,
   and radius remapping come from the host. A new provider in this mode must be
   added to that source list.
2. **Self-contained platform bundle** (Code, Kuery, App Studio, Edges, Agents, Databricks, and
   Quickstart): each bundle ships namespaced CSS. Colors use `var(--color-*)`,
   and new fallback literals match the current dark-base values (for example,
   `--color-text-muted` uses `#8587a1`). Existing provider declarations may
   still use the accepted migration fallback `#5d5f78` for that token while
   their bundles migrate independently; it is not the current token and must
   not be copied into new styles. Selectors are under `railgrid-provider-{name}`,
   radii follow the law (or repeat the `--radius-*` overrides), and recipes
   mirror the [shared recipe contract](recipes.md).

Org-owned BYO UIs use a separate runtime mode. The host verifies the bundle's
SHA-384 digest before sending its bytes to `/ui/provider-sandbox.html`, which
runs it in an opaque-origin iframe with `sandbox="allow-scripts"`. The grant
URL and user's login token stay in the host. The frame receives a structured,
token-free context over a `MessageChannel`, including the resolved theme. A
compiled portal stylesheet payload containing the canonical PortalKit CSS is
installed inside the frame's own document; no host DOM, CSS cascade, or console
globals cross into it. Full-bleed layout requests
from org frames travel as a `layout-change` bridge message and are accepted
only for boolean values from the current frame; they do not bubble from the
provider element into the host DOM. See the [BYO provider security
contract](../../byo-providers.md) and [provider integration
guide](../../providers.md).

PortalKit is canonical in `provider-sdk/portalkit` (vanilla TypeScript and CSS)
and `provider-sdk/portalkit-vue` (Vue SFCs and helpers). Edit canonical files,
then run `make sync-portalkit`; never edit vendored `*/src/portalkit/` copies.
The [PortalKit asset index](../components/portalkit-assets.md) maps every file
distributed by `hack/sync-portalkit.sh` to a contract document.

Standalone bundles call `ensureRailgridUIStyles()`. The helper's
`RAILGRID_UI_CORE_VERSION` and `RAILGRID_UI_CORE_VERSION_MARKER` in
`provider-sdk/portalkit/styles.ts` define the core contract. A host stylesheet is
accepted only when computed root markers include `--railgrid-ui-canonical: 1` and
a a core version compatible with `RAILGRID_UI_CORE_VERSION` `--railgrid-ui-core-version`. A stale or unversioned `#k-railgrid-ui`
remains untouched while canonical CSS imported through Vite's `?inline`
loader is appended under a versioned fallback ID with
`data-railgrid-ui-source="portalkit-fallback"`. The runtime fallback may be
minified by Vite; the authored stylesheet and synced source copies remain
byte-identical. Existing style elements are never replaced, and a newer host
stylesheet is never downgraded.

The optional AgentKit workbench tab recipe is canonical in
`provider-sdk/agentkit/agent-ui.css` through the `.k-workbench-tabs`,
`.k-workbench-tab`, `.k-workbench-tab__button`, `.k-workbench-tab__icon`, and
`.k-workbench-tab__label` classes. Both App Studio and Agents consume it: the
recipe defines 32px bordered tabs that keep their natural width between 112px
and 240px, a 6px radius, 12px/500 typography, accent active border/background
at 40%/10%, and 44px coarse-pointer sizing. Overflow keeps each tab intact in a
horizontal viewport and exposes previous/next scroll controls only when needed;
selecting a tab scrolls it into view without moving focus. App Studio retains
drag, reorder, and close tab lifecycle; Agents uses fixed Config and Runs tabs.
Core PortalKit does not own or load these optional recipes.

AgentKit is an optional layer. Its independent `AGENT_UI_VERSION` and
`--railgrid-agent-ui-version` marker are owned by `provider-sdk/agentkit/styles.ts`;
core PortalKit does not import AgentKit or imply that its recipes are present.

Providers may request the host's full-bleed layout with a bubbling
`railgrid-layout-change` event from their mounted custom-element root.
Full bleed hides the host provider name, version, readiness header, and
content padding, so the provider owns its in-view identity. Host readiness
and workspace access still control whether its bundle may mount; the request
changes layout only. `ProviderFrame` accepts boolean requests only from the
currently mounted provider element and clears the override when its route
changes or access is lost. It resets the override before every fresh element
is appended, including remounts after host-context changes, so an old
instance's layout state cannot carry into a new lifecycle. The mount listener
is bound before append; when the mount is reused and already has that
listener, it remains attached and resolves the current element at dispatch
time, so synchronous `connectedCallback` requests are still handled.
Providers that need full bleed must reassert `fullBleed: true` after every
route change, remount, or host-context reset. App Studio workspace routes
retain their full-bleed default. Agents use this request on agent instance
routes (`/agents/:name/...`) and reassert it as their route or host context
changes.

Provider-owned dialogs may keep their Teleport target inside the provider to
retain scoped styles. While a visible dialog is mounted with `role="dialog"`
and `aria-modal="true"`, the host raises `.railgrid-shell-main` above navigation
using `--k-layer-modal - 2`; hidden dialogs must be unmounted or use `hidden`.
App Studio places its modal backdrop at `--k-layer-modal - 1`, above AgentKit
thread rails and menus. Global confirmations and toasts retain the higher
shared layers. Closing the dialog restores the ordinary host stacking order.
Live Share checks at 932px and 390px in both themes verified dialog hit testing,
focus containment, and restoration of the host layer after Escape.

The unavailable-provider status offers **Refresh provider status**. This reloads
the catalog for the current organization and announces **Refreshing provider
status…** while the request is pending. A failed catalog request retains the
existing Retry action. Recovery does not bypass readiness or workspace binding
checks; the provider mounts only after the refreshed state permits access.
