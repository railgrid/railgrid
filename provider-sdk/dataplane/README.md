# `provider-sdk/dataplane` — the data-plane server-kit

One package for the one REST shape a provider may serve to tenants: a **verb
on a bound resource**, addressed by the tenant's kcp logical-cluster ID,
authorized as the caller. It exists because every provider grew its own path
parser and its own gates, and four dialects of the same idea is four places to
get authorization wrong.

Nothing here imports a provider. Providers import this. The contract's test
suite is one package down, in
[`conformance/`](./conformance), so a provider binary never links `testing`
or the client-go fakes.

## The grammar

```
/{root}/clusters/{clusterID}/{resource}/{name}/{verb}[/{tail...}]
/{root}/clusters/{clusterID}/{resource}/{name}/components/{component}/{verb}[/{tail...}]
```

Under the reserved root `actions` the verb segment carries its contract
version, and `Request.Version` is filled in separately:

```
/actions/clusters/{clusterID}/{resource}/{name}/{action}/{version}
```

`ParsePath(root, path) (Request, bool)` returns `false` — never a partly
filled `Request` — for anything that is not exactly this. It refuses:

- an empty segment, `.`, `..`, a segment over 253 bytes, or one that is not
  already percent-encoding-clean (so `%2F` cannot smuggle a separator);
  the same check applies to every segment of `Tail`;
- a cluster segment that is not a kcp logical-cluster ID. A workspace path
  (`root:railgrid:tenants:acme`) is refused here rather than minted into a URL
  the hub proxy answers with 403;
- `components` as a plain verb, a component form with an empty component, or
  one with no verb after the component;
- `apis` in the resource position. That is the legacy edges dialect
  (`.../clusters/{id}/apis/{group}/{version}/{resource}/...`); this package
  does not serve it and does not translate it.

`ParseRequest(root, *http.Request)` is the same thing over a request, and
additionally refuses a path that arrived percent-encoded (`URL.RawPath` set).

## The two gates

`Gate` runs both **as the caller**, from the caller's own bearer, and returns
the addressed object and the client that read it:

1. **A real GET** of `{gvr}/{name}` in `{clusterID}`. It proves visibility and
   hands back the object, so the handler can pin its UID or spec against
   whatever it later reads with the provider's own identity. An object with a
   `deletionTimestamp` is denied.
2. **A SelfSubjectAccessReview** for `create` on the virtual subresource
   `{resource}/{verb}`, scoped to `{name}`. `create` is not negotiable: the
   hub materializes every data-plane grant as exactly that rule
   (`pkg/hub/serviceaccounts/workload_identity.go`), so any other verb string
   silently breaks workload identities. For an action route the subresource is
   the action name without its version — a grant is per action, not per
   contract revision.

Before either gate, a request whose path cluster disagrees with
`X-Railgrid-Cluster` is refused (`ErrClusterMismatch`, 400). The path is
authoritative; the header exists only to catch a request that was assembled
wrong. `Identity` takes the bearer from `Authorization: Bearer` and nowhere
else — there is no `?token=` fallback to compile out later.

Every probe-shaped failure (no object, no visibility, no grant) comes back as
`ErrDenied`, which `WriteError` answers with **404**, so a caller cannot use
the status to learn that an object exists. `WriteErrorAs(w, err, 403)` opts
into 403 on a route where the caller demonstrably already knows.
`WriteError` never echoes the error: the body is the status text and nothing
else. Log the error provider-side.

## Limits and the envelope

`Serve` is the whole response path of an action. It enforces POST only (405),
`MaxInputBytes` (413), a body that is exactly one `{"input": …}` object with
no unknown fields and no trailing content (400), `Timeout` on the executor
(504), then `MaxResultItems` and `MaxOutputBytes` (500), and writes the
`actionwire` envelope either way.

`MaxResultItems` bounds a result that marshals to a JSON array, or to an
object with a top-level `items` array; any other shape is unbounded by it.
Exceeding it **fails the request rather than truncating** — a silently
shortened list is indistinguishable from a complete one. An action that can
legitimately produce more pages in its own input.

## Wiring a handler

A provider does not hand-build its mux: [`provider-sdk/serve`](../serve)
assembles the whole HTTP surface from the closed list of Pillar 2 route
classes and mounts the handler below as `Options.DataPlane` (or
`Options.Actions`). It dispatches those prefixes off the **raw** request path,
before any `http.ServeMux` can clean `..` or `//` out of it and answer with a
redirect — which is what makes the refusals described above happen where the
contract says they do. What follows is the handler itself.

```go
callers, err := dataplane.NewCallerFactory(providerRESTConfig) // credentials dropped
if err != nil {
	return err
}
greetings := schema.GroupVersionResource{Group: "quickstart.railgrid.ai", Version: "v1alpha1", Resource: "greetings"}

actions := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	req, ok := dataplane.ParseRequest(dataplane.ActionsRoot, r)
	if !ok || req.Resource != greetings.Resource || req.Version != "v1" || req.Tail != "" {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}
	greeting, caller, err := dataplane.Gate(r.Context(), r, callers, greetings, req)
	if err != nil {
		logger.Info("data-plane request refused", "err", err) // detail stays here
		dataplane.WriteError(w, err)
		return
	}
	env := actionwire.New(r, "quickstart", req.Verb, actionwire.ResourceRef{
		APIVersion: greetings.GroupVersion().String(), Kind: "Greeting",
		Resource: greetings.Resource, Name: req.Name,
	})
	limits := dataplane.Limits{Timeout: 30 * time.Second, MaxInputBytes: 64 << 10, MaxOutputBytes: 512 << 10}
	dataplane.Serve(w, r, env, limits, func(ctx context.Context, input json.RawMessage) (any, *actionwire.Error) {
		return greet(ctx, caller, greeting, input)
	})
})

handler, err := serve.New(serve.Options{Name: "quickstart", Readiness: vwhealth.Handler(ready), Actions: actions})
```

`caller` is the caller-scoped client: keep using it for anything the caller
should be able to do themselves, and switch to the provider identity only for
work the caller is not entitled to perform directly — pinning the
authoritative object, reading a provider-private Secret — after checking it
against what gate 1 returned.

## Conformance

The suite lives in the sibling package `provider-sdk/dataplane/conformance`,
so this package never links `testing` or the client-go fakes into a provider
binary. `conformance.Test(t, handler, conformance.Fixtures{…})` drives any
provider's handler — or, better, the whole `serve.New` server it is mounted
in — through the contract's observable behaviour: granted verb 200,
missing bearer 401, path/header cluster mismatch 400, foreign cluster denied,
ungranted verb denied, malformed path 400, oversized input 413, unknown input
field 400.

Build the handler with a `conformance.FakeCallers` — one real
`(Cluster, Token)` pair and an `Allow` func for gate 2 — and hand the same
fake to the fixtures:

```go
callers := &conformance.FakeCallers{
	Cluster:   "aaaaaaaaaaaaaaaa",
	Token:     "caller-token",
	Objects:   []*unstructured.Unstructured{greetingObject("hello")},
	ListKinds: map[schema.GroupVersionResource]string{greetings: "GreetingList"},
	Allow:     func(a conformance.Attributes) bool { return a.Subresource == "greet" },
}
conformance.Test(t, newServer(callers), conformance.Fixtures{
	Callers:        callers,
	GrantedPath:    "/actions/clusters/aaaaaaaaaaaaaaaa/greetings/hello/greet/v1",
	DeniedPath:     "/actions/clusters/aaaaaaaaaaaaaaaa/greetings/hello/shout/v1",
	MalformedPaths: []string{"/actions/clusters/aaaaaaaaaaaaaaaa/greetings/../greet/v1"},
	MaxInputBytes:  4096,
	ExpectEnvelope: true,
})
```

Any cluster other than `Callers.Cluster` sees no objects, so "workspace A's
token cannot reach workspace B" is observable without two live workspaces.
`conformance.ForeignCluster` is the cluster ID the suite uses for that; a
fixture must not also use it as its own.
