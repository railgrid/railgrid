---
{"schema":1,"id":"design.foundations.provider-integration","title":"Provider visual integration and PortalKit distribution","kind":"policy","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Host-compiled and self-contained providers share the canonical stylesheet through the sync script and version 13 style handoff; standalone fallbacks remain source-aligned."},"appliesTo":["provider-portals","portalkit","portal"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/provider-integration.md#provider-visual-integration-and-portalkit-distribution","role":"design"},{"path":"hack/sync-portalkit.sh","role":"implementation"},{"path":"provider-sdk/portalkit/styles.ts","role":"implementation"},{"path":"portal/src/pages/ProviderFrame.vue","role":"implementation"},{"path":"providers/agents/portal/src/App.vue","role":"implementation"},{"path":"hack/verify-ui-conformance.mjs","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Current byte-for-byte PortalKit copy and manifest parity passed."},{"kind":"command","ref":"make verify-ui-conformance","status":"passing","evidence":"Current UI conformance covered 403 files with zero violations and 28 tests passed."},{"kind":"browser","ref":"Current Agents provider integration rendering (CSS v13)","status":"passing","evidence":"The built Agents v13 local fixture matrix passed all four desktop/mobile light/dark cases with errors[] and no viewport overflow, including fullBleed lifecycle and responsive workbench/separator behavior. Evidence: /tmp/agent-workspace-browser/correction-v13-matrix.log and screenshots in that directory. Host lifecycle was simulated; no live Tilt, backend, or real-host rendered claim is made."}]},"relatedDocuments":[{"id":"design.components.portalkit-assets","relation":"see-also","path":"docs/design/components/portalkit-assets.md"}]}
---

# Provider visual integration and PortalKit distribution

There are two integration modes with one look:

Provider custom elements render in the host document's light DOM, so host
tokens and cascading styles cross the element boundary. Self-contained bundles
namespace their own selectors under faros-provider-{name}; that boundary keeps
local rules from leaking while preserving the host-token contract.

1. **Host-compiled** (Infrastructure): `.vue` and `.ts` files are included in
   the host Tailwind scan through `@source` in `main.css`. Utilities, tokens,
   and radius remapping come from the host. A new provider in this mode must be
   added to that source list.
2. **Self-contained** (Code, Kuery, App Studio, Edges, Agents, Databricks, and
   Quickstart): each bundle ships namespaced CSS. Colors use `var(--color-*)`,
   and new fallback literals match the current dark-base values (for example,
   `--color-text-muted` uses `#8587a1`). Existing provider declarations may
   still use the accepted migration fallback `#5d5f78` for that token while
   their bundles migrate independently; it is not the current token and must
   not be copied into new styles. Selectors are under `faros-provider-{name}`,
   radii follow the law (or repeat the `--radius-*` overrides), and recipes
   mirror the [shared recipe contract](recipes.md).

PortalKit is canonical in `provider-sdk/portalkit` (vanilla TypeScript and CSS)
and `provider-sdk/portalkit-vue` (Vue SFCs and helpers). Edit canonical files,
then run `make sync-portalkit`; never edit vendored `*/src/portalkit/` copies.
The [PortalKit asset index](../components/portalkit-assets.md) maps every file
distributed by `hack/sync-portalkit.sh` to a contract document.

Standalone bundles call `ensureFarosUIStyles()`. A host stylesheet is accepted
only when computed root markers include `--faros-ui-canonical: 1` and a
compatible version 13 `--faros-ui-version`. A stale or unversioned `#k-faros-ui`
remains untouched while canonical CSS imported through Vite's `?inline`
loader is appended under a versioned fallback ID with
`data-faros-ui-source="portalkit-fallback"`. The runtime fallback may be
minified by Vite; the authored stylesheet and synced source copies remain
byte-identical. Existing style elements are never replaced, and a newer host
stylesheet is never downgraded.

The shared workbench tab recipe is canonical in `faros-ui.css` through the
`.k-workbench-tabs`, `.k-workbench-tab`, `.k-workbench-tab__button`,
`.k-workbench-tab__icon`, and `.k-workbench-tab__label` classes. Both App Studio
and Agents consume it: the recipe defines 32px bordered tabs, 112–240px width bounds,
6px radius, 12px/500 typography, accent active border/background at 40%/10%,
and 44px coarse-pointer sizing. App Studio retains drag, reorder, and close
tab lifecycle; Agents uses fixed Config and Runs tabs.

Agents may request the host's full-bleed layout with a bubbling
`faros-layout-change` event while a usable context is on an agent instance
route (`/agents/:name/...`). `ProviderFrame` accepts that boolean for Agents,
and the Agents shell reasserts `fullBleed: true` as the route or host context
changes. The request is cleared when leaving the agent instance, when context
is lost, and when the provider unmounts; it does not change provider routing or
tenant authority.
