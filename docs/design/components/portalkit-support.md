---
{"schema":1,"id":"design.components.portalkit-support","title":"PortalKit supporting contracts","kind":"reference","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Framework-neutral helpers own style recovery, tenant headers, page read state, dashboard tiles, and delayed loading across standalone bundles."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/portalkit-support.md#portalkit-supporting-contracts","role":"design"},{"path":"provider-sdk/portalkit/dashboardtile.ts","role":"implementation"},{"path":"provider-sdk/portalkit/page-state.ts","role":"implementation"},{"path":"provider-sdk/portalkit/styles.ts","role":"implementation"},{"path":"provider-sdk/portalkit/tenant.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/useDelayedLoading.ts","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[]}
---

# PortalKit supporting contracts

The plain supporting assets are part of the shared contract even though they do
not render a standalone component:

- `styles.ts` implements the standalone CSS handoff. It accepts a host only
  when the computed canonical marker and compatible version are present, keeps
  stale host styles untouched, appends exact vendored CSS under a versioned
  fallback ID, never replaces existing styles, and never downgrades a newer
  host version.
- `tenant.ts` owns the security-critical hub-proxy contract: `readTenant()`
  reads `faros:portal:tenant`; `tenantHeaders({ token, json })` emits
  `Accept`, optional JSON content type, bearer authorization, `X-Faros-Org`, and
  `X-Faros-Workspace`; `serviceBase()` rewrites `/ui/providers/*` to
  `/services/providers/*`. Callers never re-inline these headers. Cluster-in-
  path portals use their separate bearer-token model.
- `page-state.ts` and Vue `useDelayedLoading.ts` preserve truthful first-read,
  background-refresh, stale, error, retry, and delayed-loading semantics. A
  useful snapshot stays visible during background work; loading indicators do
  not replace content or disable actions.
- `dashboardtile.ts` supplies the framework-neutral dashboard resource summary
  contract. It follows the same stale/read-state and semantic-token rules as
  resource pages; provider facts remain provider-owned.

See the [resource reads pattern](../patterns/resource-reads.md) and
[provider integration foundation](../foundations/provider-integration.md) for
the cross-surface invariants.
