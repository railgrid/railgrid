---
{"schema":1,"id":"design.foundations.provider-integration","title":"Provider visual integration and PortalKit distribution","kind":"policy","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Host-compiled and self-contained providers share the canonical stylesheet through the sync script and versioned style handoff; documented fallback generations remain source-aligned during migration."},"appliesTo":["provider-portals","portalkit","portal"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/provider-integration.md#provider-visual-integration-and-portalkit-distribution","role":"design"},{"path":"hack/sync-portalkit.sh","role":"implementation"},{"path":"provider-sdk/portalkit/styles.ts","role":"implementation"},{"path":"hack/verify-ui-conformance.mjs","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.components.portalkit-assets","relation":"see-also","path":"docs/design/components/portalkit-assets.md"}]}
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
compatible `--faros-ui-version`. A stale or unversioned `#k-faros-ui` remains
untouched while exact vendored CSS is appended under a versioned fallback ID
with `data-faros-ui-source="portalkit-fallback"`. Existing style elements are
never replaced, and a newer host stylesheet is never downgraded.
