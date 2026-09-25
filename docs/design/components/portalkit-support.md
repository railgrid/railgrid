---
{"schema":1,"id":"design.components.portalkit-support","title":"PortalKit supporting contracts","kind":"reference","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Framework-neutral helpers own style recovery, tenant headers, page read state, dashboard tiles, and delayed loading across standalone bundles."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/portalkit-support.md#portalkit-supporting-contracts","role":"design"},{"path":"provider-sdk/portalkit/dashboardtile.ts","role":"implementation"},{"path":"provider-sdk/portalkit/page-state.ts","role":"implementation"},{"path":"provider-sdk/portalkit/styles.ts","role":"implementation"},{"path":"provider-sdk/portalkit/tenant.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/useDelayedLoading.ts","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[]}
---

# PortalKit supporting contracts

The plain supporting assets are part of the shared contract even though they do
not render a standalone component:

- `styles.ts` implements the standalone CSS handoff. It accepts a host only
  when the computed canonical marker and compatible version are present, keeps
  stale host styles untouched, and appends a versioned fallback when needed.
  The fallback imports canonical CSS through Vite's `?inline` loader, which
  embeds minified canonical rules; the authored stylesheet and synced source
  copies remain byte-identical. The current core style version is 18, from
  `RAILGRID_UI_CORE_VERSION` in `provider-sdk/portalkit/styles.ts`, and is read
  from `--railgrid-ui-core-version`. Existing style elements are never replaced.
  Optional AI styles load independently through `agentkit/styles.ts`, with
  their own marker and version; see
  [AI presentation](ai-conversation.md).
- `tenant.ts` owns the security-critical hub-proxy contract: `readTenant()`
  reads `railgrid:portal:tenant`; `tenantHeaders({ token, json })` emits
  `Accept`, optional JSON content type, bearer authorization, `X-Railgrid-Org`, and
  `X-Railgrid-Workspace`; `serviceBase()` rewrites `/ui/providers/*` to
  `/services/providers/*`, for `/oauth` and `/mcp` only. Callers never
  re-inline these headers. Bound CRs and their verbs are addressed by cluster
  in the path through `kube.ts` (`createKubeClient`, `kubeVerbPath`).
- `page-state.ts` and Vue `useDelayedLoading.ts` preserve truthful first-read,
  background-refresh, stale, error, retry, and delayed-loading semantics. A
  useful snapshot stays visible during background work; loading indicators do
  not replace content or disable actions.
- `dashboardtile.ts` supplies the framework-neutral dashboard resource summary
  contract. It follows the same stale/read-state and semantic-token rules as
  resource pages; provider facts remain provider-owned. Tailwind consumers use
  `tileClass`, while plain-DOM consumers use the matching
  `dashboardTileSemanticClass` hooks implemented in `railgrid-ui.css`. Both maps
  describe the same slots; the semantic map is names only and therefore
  requires the canonical stylesheet. Neither authorizes provider-local visual
  variants. A change to either map or its CSS increments the matching
  `RAILGRID_UI_CORE_VERSION` style-handoff contract before the assets are synced.

See the [resource reads pattern](../patterns/resource-reads.md) and
[provider integration foundation](../foundations/provider-integration.md) for
the cross-surface invariants.
