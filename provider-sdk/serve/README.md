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
| `/dataplane/…` | (a) data-plane verb | `DataPlane` | dispatched off the **raw** path. |
| `/actions/…` | (a′) action | `Actions` | dispatched off the **raw** path. |
| `/workload-identities/…` | (e) hub-only | `HubOnly` | exact paths; must be under `HubOnlyPrefixes`, which is what the hub proxy refuses to callers. |
| `/oauth/…` | (d) browser OAuth | `OAuth` | the popup flow's `start`/`callback`/`config`. |
| `/agent/…` | (f) agent tunnel | `Extra` | `{Prefix, Class: ClassAgentTunnel, Handler}`; raw path. |
| `/webhooks/…` | (g) signed webhook | `Extra` | `{Prefix, Class: ClassWebhook, Handler}`; raw path. |
| everything else | — | `Portal` | the embedded bundle: the file for an asset path (last segment has a dot), `index.html` otherwise. |

There is no `/api/*` row, and `New` returns an error rather than registering
one. A nil field is a class the provider does not serve: its routes then do
not exist, because a stub that answers "ok" for something the provider cannot
do is worse than a 404.

## Why `ServeHTTP` dispatches before the mux

`/dataplane/`, `/actions/`, `/agent/` and `/webhooks/` are matched on
`r.URL.Path` **before** any `http.ServeMux` sees the request. `ServeMux`
cleans the path and answers a non-clean one with a redirect; on the data plane
that is wrong twice over — `..` and `//` are exactly what the grammar must
refuse, and a redirect hands the caller back a path it never asked for, which
once cleaned may address a different object. The handler under those prefixes
therefore receives the path the caller actually sent, and
`dataplane.ParseRequest` refuses it where the contract says it does.
`provider-sdk/serve`'s own test runs `dataplane/conformance.Test` against a
server built by `New`, so this is verified through the server, not just the
parser.

Everything else goes through a mux, and is wrapped in a request log
(`method`, `path`, `status`, `duration`) on a `logr.Logger` — klog when the
field is left zero, matching `provider-sdk/hubclient`.

## Example

```go
handler, err := serve.New(serve.Options{
	Name:      "quickstart",
	Readiness: vwhealth.Handler(vwState),       // (c)
	Portal:    distFS,                          // the embedded Vite build
	DataPlane: server.NewDataPlane(server.Deps{ // (a)
		Callers:   callers,
		Greetings: quickstartv1alpha1.GreetingsResource,
	}),
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
