# `provider-sdk/serve` — one server layout for every provider

`provider-sdk/dataplane` decides what a data-plane **request** means. This
package decides what a provider's **server** is: which routes exist at all.

The list of route classes in
[`docs/provider-connectivity-contract.md`](../../docs/provider-connectivity-contract.md)
§"Pillar 2 route classes" is closed, but it was enforced by review alone, and
every provider hand-built its own mux. The copies drifted — one mounted the
data plane on an `http.ServeMux` (which rewrites the very paths the grammar
must refuse), one grew an `/api/*` facade for its portal, each re-wrote the
index fallback and the request log. `serve.New` takes one handler per class
and refuses anything else, so those are no longer mistakes a provider can make.

## The layout

| Path | Class | Options field | Notes |
|---|---|---|---|
| `/healthz` | (c) health | — | always 200. Liveness is not readiness: a provider whose watches are dead is alive and must not be restarted. |
| `/readyz` | (c) health | `Readiness` | **required**; normally `vwhealth.Handler(readiness)`. |
| `/mcp`, `/mcp/sse` | (b) MCP projection | `MCP` | one handler answers both; the hub's aggregate federates exactly these paths. |
| `/clusters/{id}/apis/{group}/{version}/{resource}/{name}/{verb}` | (a) data-plane verb, (a′) action | `DataPlane`, `Actions`, `Subresources` | the **kcp custom-subresource** path a shard forwards — the only way a verb is reached. Dispatched off the **raw** path. |
| `/workload-identities/…` | (e) hub-only | `HubOnly` | exact paths; must be under `HubOnlyPrefixes`, which is what the hub proxy refuses to callers. |
| `/oauth/…` | (d) browser OAuth | `OAuth` | the popup flow's `start`/`callback`/`config`. |
| `/agent/…` | (f) agent tunnel | `Extra` | `{Prefix, Class: ClassAgentTunnel, Handler}`; raw path. |
| `/webhooks/…` | (g) signed webhook | `Extra` | `{Prefix, Class: ClassWebhook, Handler}`; raw path. |
| everything else | — | `Portal` | the embedded bundle: the file for an asset path (last segment has a dot), `index.html` otherwise. |

There is no `/api/*` row, and `New` returns an error rather than registering
one. There is no `/dataplane/*` or `/actions/*` row either: a verb has no
hub-proxied spelling, and a handler set without `Subresources` to reach it
through is an error at construction. A nil field is a class the provider does
not serve: its routes then do not exist, because a stub that answers "ok" for
something the provider cannot do is worse than a 404.

## Verbs: `Subresources`

Every verb a provider declares is published on its APIExport as a kcp
**custom subresource** named `"<resource>/<verb>"`
(`provider-sdk/apiexportgen`), so a caller reaches it as an ordinary API path
and the serving shard reverse-proxies it here. `Options.Subresources` is the
table that says which coordinates exist and which handler each one belongs to:

```go
Subresources: map[string]serve.SubresourceRoute{
	"greetings/greet":      {},                                 // → Options.DataPlane
	"greetings/shout":      {Action: true, Version: "v1"},      // → Options.Actions
},
```

`serve.SubresourcesFromCatalogEntryFile(manifestPath)` builds the same table
from the provider's own `manifest.yaml` — `spec.dataPlane.verbs[]` become
plain routes and `spec.actions[]` become `{Action: true, Version}` from the
id's `/v<n>` suffix — which is the form to use, because the declaration then
cannot disagree with what `apiexportgen` published. It refuses a verb named
`status` or `scale`, and any verb outside `^[a-z][-a-z0-9]*[a-z0-9]$`, at
startup rather than at a tenant's Enable. The manifest is baked into the image
next to the objects `init` applies (`RAILGRID_KCP_DIR/catalogentry.yaml`);
without it the provider has no data plane and `New` says so.

`New` mounts one adapter at the raw prefix `/clusters/`. For each request it:

1. parses the path with `dataplane.ParseSubresourceRequest` (the component of
   a multi-component object arrives as `?component=`);
2. refuses a coordinate absent from the table (404), even when a handler
   would have answered it — the declaration is the contract;
3. reads the caller the shard stamped (`X-Remote-User`, `X-Remote-Group`,
   `X-Remote-Extra-*`) and refuses a request carrying none (401 — anonymous is
   not a fallback and the provider's own credential is never a substitute),
   and one over `dataplane.MaxHops` (508);
4. dispatches to `DataPlane` or `Actions` with the URL **untouched**, the
   parsed route in the request context (`dataplane.RouteFrom`, with an
   action's contract version restored from the table onto `Request.Version`)
   and the caller beside it (`dataplane.ProxiedIdentityFrom`). The adapter is
   the only place those two context values are ever set.

**The URL published in the provider's `DataPlaneEndpointSlice` must be its
shard-facing address**: anyone who can reach it directly can claim any
identity. The hub's backend proxy strips `X-Remote-*` and the hop counter from
everything it forwards for the same reason.

## Why `ServeHTTP` dispatches before the mux

`/clusters/`, `/agent/` and `/webhooks/` are matched on `r.URL.Path`
**before** any `http.ServeMux` sees the request. `ServeMux` cleans the path
and answers a non-clean one with a redirect; on the data plane that is wrong
twice over — `..` and `//` are exactly what the grammar must refuse, and a
redirect hands the caller back a path it never asked for, which once cleaned
may address a different object. The adapter therefore receives the path the
shard actually sent, and `dataplane.ParseSubresourceRequest` refuses it where
the contract says it does. This package's own test runs
`dataplane/conformance.Test` against a server built by `New`, so this is
verified through the server, not just the parser.

Everything else goes through a mux, and is wrapped in a request log
(`method`, `path`, `status`, `duration`) on a `logr.Logger` — klog when the
field is left zero, matching `provider-sdk/hubclient`.

## Example

```go
subresources, err := serve.SubresourcesFromCatalogEntryFile(manifestPath)
if err != nil {
	log.Fatalf("subresources: %v", err) // a declared verb kcp would refuse, or no manifest
}

handler, err := serve.New(serve.Options{
	Name:      "quickstart",
	Readiness: vwhealth.Handler(vwState),       // (c)
	Portal:    distFS,                          // the embedded Vite build
	DataPlane: server.NewDataPlane(server.Deps{ // (a)
		Callers:   callers,                     // a dataplane.ProviderCallerFactory
		Greetings: quickstartv1alpha1.GreetingsResource,
	}),
	Subresources: subresources,                 // the declared coordinates
})
if err != nil {
	log.Fatalf("server: %v", err) // a route the contract does not have
}
srv := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
```

A provider with more classes fills in more fields — `MCP`, `Actions`, `OAuth`,
`HubOnly: map[string]http.Handler{"/workload-identities/review": review}` — and
an edge or webhook provider adds `Extra: []serve.Route{{Prefix: serve.WebhooksPrefix,
Class: serve.ClassWebhook, Handler: hooks}}`. Nothing else is mountable.

The callers handed to `DataPlane` and `Actions` must be a
`dataplane.ProviderCallerFactory`
(`dataplane.NewCallerFactory(cfg, dataplane.WithProviderConfig(providerCfg, exportName))`):
with no caller bearer the gate needs a client that acts as the provider —
through the export's virtual workspace, which the factory reads from the
provider's `APIExportEndpointSlice`. The export must also claim
`authorization.k8s.io/subjectaccessreviews` (`create`, tenantScoped in the
manifest): kcp serves that builtin through the virtual workspace only for an
export that claims it.
