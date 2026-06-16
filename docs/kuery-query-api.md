# kuery query API

The kuery provider exposes a fleet-wide query API over every edge cluster
engaged for your workspace. The portal's **Playground** tab is a UI on top of
it; this document is the programmatic reference.

## Endpoint

```
POST  {hub}/services/providers/kuery/api/query        # run a query
GET   {hub}/services/providers/kuery/api/query-schema  # JSON Schema for the body
GET   {hub}/services/providers/kuery/api/edges          # engaged edge names
```

- **Body** of `/api/query` is a kuery `QuerySpec` (JSON). The response is a
  `QueryStatus` with `objects[]`.
- **Tenanting is automatic.** The hub authenticates your bearer token, resolves
  your workspace, and injects the tenant scope server-side. Any
  `X-Kedge-Tenant` you send is stripped — you cannot query another tenant.

## Auth

Send `Authorization: Bearer <token>`:

- **OIDC user token** — works today. Use the token your portal session uses.
- **Service-account token** — non-interactive/bot access. (In progress: the hub
  needs to resolve a kcp ServiceAccount token to its workspace path before this
  works through the provider proxy.)

```bash
curl -sS "$HUB/services/providers/kuery/api/query" \
  -H "Authorization: Bearer $KEDGE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"filter":{"objects":[{"groupKind":{"kind":"Deployment"}}]},"objects":{"cluster":true}}'
```

## QuerySpec essentials

Fetch the full schema from `/api/query-schema`. Key fields:

| field | meaning |
|---|---|
| `root` | `objects` (default) or `clusters` (one node per edge; expand `members`). |
| `cluster.name` | restrict to one edge. |
| `filter.objects[]` | OR-ed filters: `groupKind{apiGroup,kind}`, `name`, `namespace`, `labels`, `categories`, `jsonpath`. |
| `limit`, `maxDepth` | root cap (def 100), transitive depth for `+` relations (def 10). |
| `objects` | response shape: `id`, `cluster`, `mutablePath`, sparse `object` projection, and `relations`. |

### Relations and impact direction

`objects.relations` follows coupling. Each relation has an **impact direction**
— `A→B` means *deleting A breaks B*:

- **Upstream** (the target depends on these; deleting them breaks it):
  `owners`, `references`, `selects`, `namespace`.
- **Downstream** (the target's blast radius; break if it's deleted):
  `descendants`, `selected-by`, `namespaced`, `members`.
- **Lateral**: `linked`, `grouped`.

Append `+` for the transitive form (`descendants+`, `owners+`, `linked+`).

Coupling is **declared** (ownerRefs, spec field references, label selectors,
namespace membership) — not runtime traffic — so an empty result is not proof
nothing depends on the object at runtime.

## Examples

All objects in a namespace:

```json
{ "filter": { "objects": [{ "namespace": "default" }] },
  "objects": { "cluster": true, "object": { "kind": true, "metadata": { "name": true, "namespace": true } } } }
```

Impact of a ConfigMap (who breaks if I change it + what it needs):

```json
{ "filter": { "objects": [{ "groupKind": { "kind": "ConfigMap" }, "namespace": "default", "name": "app-config" }] },
  "objects": { "cluster": true, "relations": { "references": {}, "selected-by": {}, "namespace": {} } } }
```

Per-cluster tree:

```json
{ "root": "clusters",
  "objects": { "cluster": true, "relations": { "members": { "limit": 50,
    "objects": { "object": { "kind": true, "metadata": { "name": true, "namespace": true } } } } } } }
```

For agent/LLM use, the `kuery_impact` MCP tool wraps the impact query and
returns the upstream/downstream split directly.
