# kuery query API

The kuery provider exposes a fleet-wide query API over every edge cluster
engaged for your workspace. The portal's **Playground** tab is a UI on top of
it; this document is the programmatic reference.

## A query is a verb on a SavedView

There is one route, and it is a verb on a named resource you must be able to
see and must be granted the verb on:

```
POST {hub}/services/providers/kuery/dataplane/clusters/{clusterID}/savedviews/{name}/run
```

- `{clusterID}` is your workspace's kcp logical-cluster ID — the same value the
  portal and `kubectl` address it by. A workspace path
  (`root:railgrid:tenants:acme`) is refused, not translated.
- `{name}` is a `SavedView` in that workspace (see below).
- The **body** is `{"input": {"query": <QuerySpec>}}`, or `{"input": {}}` to run
  the view's own `spec.query` as saved.
- The **response** is an [action envelope](./provider-actions.md): `result` is a
  kuery `QueryStatus` with `objects[]`, or `error` carries a code and a message.

The QuerySpec JSON Schema is a static asset beside the portal bundle:

```
GET {hub}/ui/providers/kuery/query-schema.json
```

**There is no un-named query route.** `POST /api/query`, `GET /api/edges`,
`GET /api/status` and the `?tenant=` development bypass were deleted, not
deprecated: they derived the tenant from an `X-Railgrid-Cluster` header and
never looked at the bearer at all, which was safe only for as long as the hub
proxy stripped and re-injected that header. Nothing replaced them at `/api/`.

## Authorization

Send `Authorization: Bearer <token>`. Before the query engine is touched, the
provider runs two checks **as you**, with your own credential:

1. a real `GET` of `savedviews/{name}` in `{clusterID}` — you must be able to
   see the view; and
2. a `SelfSubjectAccessReview` for `create` on `savedviews/run`, scoped to
   `{name}` — you must be granted the verb on that view.

So a bearer for workspace A cannot run a view in workspace B even by sending a
forged `X-Railgrid-Cluster` straight to the provider pod: the cluster in the
path is what is gated, and a request whose header disagrees with its path is
refused outright. Every refusal — no such view, no visibility, no grant — comes
back as the same `404`, so the status cannot be used to probe for what exists.

- **OIDC user token** — the token your portal session uses.
- **Service-account token** — non-interactive/bot access. Grant the SA `get` on
  the view and `create` on `savedviews/run` for it.

Results are additionally scoped to the edges kuery has actually engaged for
your workspace. That set is the `Engagement` records in the provider's own
workspace (see [the architecture](./kuery-provider-architecture.md)); a query
naming an edge outside it is refused with `not_engaged`, and so is a query from
a workspace with no engaged edge at all — rather than answered with an empty
result that reads like a clean fleet.

## SavedViews

`SavedView` is kuery's one exported kind (`kuery.providers.railgrid.ai/v1alpha1`,
cluster-scoped). It is an ordinary object in your workspace: create it with
`kubectl`, GitOps it, and grant per-view access with ordinary RBAC.

```yaml
apiVersion: kuery.providers.railgrid.ai/v1alpha1
kind: SavedView
metadata:
  name: fleet-deployments
spec:
  displayName: Deployments across the fleet
  query:
    filter:
      objects:
        - groupKind: { apiGroup: apps, kind: Deployment }
    objects:
      cluster: true
      object: { metadata: { name: true, namespace: true }, spec: { replicas: true } }
    limit: 50
```

`spec.query` is an embedded object rather than a fully-typed schema because
QuerySpec is recursive (`objects.relations` values carry another `objects`),
which a CRD cannot express. A reconciler validates it against the published
JSON Schema instead and reports the verdict on `status.conditions[Ready]`, so a
typo is visible the moment you save the view rather than the next time it runs.
`status.lastOpenedAt` records the last run.

Ad-hoc queries are not an exception to any of this: the portal playground and
the MCP tools run a per-user scratch view named
`playground-<first 12 hex of sha256(your identity)>`, which they create in your
workspace with your own credential on first use. Revoking someone's access to
that name stops their ad-hoc queries.

```bash
curl -sS "$HUB/services/providers/kuery/dataplane/clusters/$CLUSTER/savedviews/fleet-deployments/run" \
  -H "Authorization: Bearer $RAILGRID_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"input":{}}' | jq .result
```

## QuerySpec essentials

Fetch the full schema from `/ui/providers/kuery/query-schema.json`. Key fields:

| field | meaning |
|---|---|
| `root` | `objects` (default) or `clusters` (one node per edge; expand `members`). |
| `cluster.name` | restrict to one edge. Must be an edge engaged for your workspace. |
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

Each of these is a `spec.query` on a SavedView, or the `input.query` of one run.

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
returns the upstream/downstream split directly. Both MCP tools run through the
same two gates as the REST verb: pass `savedView` to run a specific view, or
omit it to use your own scratch view.
