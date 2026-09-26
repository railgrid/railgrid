# `provider-sdk/dataplane` — the data-plane server-kit

One package for the one REST shape a provider may serve to tenants: a **verb
on a bound resource**, published as a kcp **custom subresource** on the
provider's APIExport and reached like any other Kubernetes API path. It exists
because every provider once grew its own path parser and its own gates, and
four dialects of the same idea is four places to get authorization wrong.

There is exactly one transport. kcp authenticates the caller, authorizes the
verb with ordinary RBAC, and reverse-proxies the request to the provider with
the caller's identity stamped in requestheader headers. The hub's backend proxy
(`/services/providers/{name}/…`) carries no verbs at all; it is left with MCP,
browser OAuth, signed webhooks, the agent tunnel and health.

Nothing here imports a provider. Providers import this. The contract's test
suite is one package down, in [`conformance/`](./conformance), so a provider
binary never links `testing` or the client-go fakes.

## The grammar

Every `{resource}/{verb}` coordinate a provider declares
(`spec.dataPlane.verbs[]`, `spec.actions[]`) is an entry on its APIExport named
`"<resource>/<verb>"` (`provider-sdk/apiexportgen`), so the verb is an
ordinary API path that kubectl can discover:

```
/apis/{group}/{version}/{resource}/{name}/{verb}[/{tail...}][?component={component}]
```

A caller addresses it on whichever kcp front door it holds a credential for —
the hub's `/clusters/{id}` for a user or a tenant ServiceAccount, a provider's
own export virtual workspace for a claimed verb on another provider's kind —
and the serving shard forwards it to the provider as

```
/clusters/{clusterID}/apis/{group}/{version}/{resource}/{name}/{verb}[/{tail...}][?component={component}]
```

`ParseSubresourceRequest(r) (SubresourceRequest, error)` parses it. It refuses:

- an empty segment, `.`, `..`, a segment over 253 bytes, or one that is not
  already percent-encoding-clean (so `%2F` cannot smuggle a separator); the
  same check applies to every segment of `Tail`, and to the component;
- a path that arrived percent-encoded (`URL.RawPath` set);
- a cluster segment that is not a kcp logical-cluster ID. A workspace path
  (`root:railgrid:tenants:acme`) is refused here rather than minted into a URL;
- `status` and `scale`, which belong to the object's own shape and can never
  be a provider's verb;
- more than one `component` value.

**The component of a multi-component object travels as the `component` query
parameter**, never in the path: kcp reads `{name}/{subresource}` and treats
everything after the verb as the verb's own tail, so `…/components/{c}/…` in
the path would be routed as a subresource named `components`.

**An action's contract version is not in the path.** The provider's
declaration pins it (`spec.actions[].id` ends in `/v<n>`), and
`provider-sdk/serve`'s adapter restores it onto `Request.Version` from that
declaration before dispatching.

`SubresourcePath(group, version, Request)` / `SubresourceURL(base, …)` are the
inverse, for a consumer addressing a verb. Consumers use them instead of
string-building another provider's URL, so the grammar lives in one place.

## What a handler receives

A provider does not mount a handler on a path of its own. It fills in
`serve.Options.DataPlane` / `serve.Options.Actions` and
`serve.Options.Subresources` (from its manifest), and `serve.New` mounts one
adapter at `/clusters/` that:

1. parses the path (`ParseSubresourceRequest`), refusing what must be refused;
2. refuses a coordinate absent from the declaration, whatever a handler would
   have answered — the declaration is the contract;
3. reads the caller kcp stamped (`ProxiedCaller`) and refuses a request that
   carries none. **There is no bearer here**, and anonymous is not a fallback;
4. dispatches to the handler with the **URL untouched** and two values in the
   request context: the parsed route (`RouteFrom`) and the caller
   (`ProxiedIdentityFrom`). Those two context values are set in exactly one
   place — the adapter — so nothing else in the process can forge them.

A handler therefore begins:

```go
route, ok := dataplane.RouteFrom(r.Context())
if !ok || route.Resource != instances.Resource {
	dataplane.WriteError(w, dataplane.ErrBadPath)
	return
}
object, provider, err := dataplane.Gate(r.Context(), callers, instances, route.Request)
```

`ProxiedCaller` reads `X-Remote-User`, `X-Remote-Group` and the
`X-Remote-Extra-*` extras (prefix stripped, key lower-cased). `CheckHops`
refuses a request that has crossed more than `MaxHops` (10) proxies. These
headers are believed **only because the connection is one the provider already
trusts**: the URL a provider publishes in its `DataPlaneEndpointSlice` must be
its shard-facing address, because anyone who can reach it directly can claim
any identity. The hub's backend proxy strips these headers from everything it
forwards for the same reason.

## The gate

`Gate(ctx, callers, gvr, req)` runs the contract's gate for the caller in `ctx`
and returns the addressed object together with a client acting **as the
provider** in `req.ClusterID`.

1. **Visibility keeps its meaning and changes its mechanism.** The caller must
   be able to see the parent object, but the provider holds the caller's name
   and groups and no credential to read with — so it creates a
   **SubjectAccessReview** (`SubjectAccessReviews()`) for `get` on
   `{resource}/{name}` on the caller's behalf, and only then reads the object
   as itself. Both go through the provider's APIExport virtual workspace, the
   one door where the provider has standing in a tenant workspace. kcp serves
   `SubjectAccessReview` there only for an export that **claims**
   `authorization.k8s.io/subjectaccessreviews` (`create`, tenantScoped) and a
   binding that accepted it, so every provider with a verb declares that
   claim; without it the gate fails closed with a 500. A `deletionTimestamp`
   denies.
2. **The verb grant is not repeated.** kcp authorized the `{resource}/{verb}`
   noun with ordinary RBAC before it proxied the request at all, mapping the
   HTTP method onto the RBAC verb (GET → get, POST → create, an upgrade → its
   method). The coordinate is the capability and the method is the provider's
   transport detail, so every grant the hub mints on a verb coordinate carries
   `SubresourceVerbs` (every verb kcp can map onto), and a composition claim on
   one is spelled `verbs: ["*"]`.

A **foreign provider** — a ServiceAccount from another logical cluster,
forwarded through its own export virtual workspace — is authorized by the
claim kcp already enforced (`ProxiedIdentity.IsForeignProvider`); the gate
reads the parent as the provider and asks no review, which would refuse an
identity with no RBAC in the tenant workspace.

Every probe-shaped failure (no object, no visibility) comes back as
`ErrDenied`, which `WriteError` answers with **404**, so a caller cannot use
the status to learn that an object exists. `WriteErrorAs(w, err, 403)` opts
into 403 on a route where the caller demonstrably already knows. A request
with no caller is `ErrNoCaller` (401). `WriteError` never echoes the error:
the body is the status text and nothing else. Log the error provider-side.

**After the gate a handler acts as the provider, not as the caller.** There is
no caller credential to act with. Any further question about the caller — may
they read a second object the verb touches, may they perform the write the
verb performs on their behalf — is `Authorize(ctx, provider, identity, attrs)`,
a SubjectAccessReview run on the caller's behalf through the same client the
gate returned.

## Callers

`NewCallerFactory(base, dataplane.WithProviderConfig(providerCfg, exportName))`
builds the factory a verb handler needs (`ProviderCallerFactory`):

- `AsProvider(clusterID)` — a client acting as the provider **through its
  export's virtual workspace** (`…/services/apiexport/<cluster>/<export>/clusters/<id>`),
  read once from the `APIExportEndpointSlice` named after the export in the
  provider workspace, or pinned with `WithProviderEndpoint(url)`. Never the
  shard's own `/clusters/<id>`: the provider identity has no RBAC inside a
  tenant workspace, only the standing its export gives it.
- `ExportVerbURL(ctx, gvr, req)` and `ProviderHTTPClient()` — how a provider
  calls a verb **another provider** serves: a custom subresource it has
  claimed (a `spec.dependencies[].composes[]` entry naming
  `"{resource}/{verb}"` with verbs `["*"]`, because kcp checks the HTTP
  method as the verb), addressed through its own
  export virtual workspace with its own credential. kcp authorizes the call
  against the claim the tenant accepted and forwards it to the owning
  provider impersonating this one; that provider's gate sees a foreign
  provider. Cross-provider work is done as the provider, never on a user's
  behalf — the end-user identity is not carried.
- `For(clusterID, token)` — a client acting as a **bearer-credentialed
  caller**. Only the MCP class still has one of those: the hub's MCP
  aggregate forwards the caller's bearer with each tool call, and `Identity(r)`
  reads it. A verb handler never calls this.

## Limits and the envelope

`Serve` is the whole response path of an action. It enforces POST only (405),
`MaxInputBytes` (413), a body that is exactly one `{"input": …}` object with
no unknown fields and no trailing content (400), `Timeout` on the executor
(504), then `MaxResultItems` and `MaxOutputBytes` (500), and writes the
`actionwire` envelope either way.

`MaxResultItems` bounds a result that marshals to a JSON array, or to an
object with a top-level `items` array; any other shape is unbounded by it.
Exceeding it **fails the request rather than truncating** — a silently
shortened list is indistinguishable from a complete one.

## Wiring a handler

```go
callers, err := dataplane.NewCallerFactory(hubRESTConfig,
	dataplane.WithProviderConfig(providerRESTConfig, "quickstart.providers.railgrid.ai"))
if err != nil {
	return err
}
greetings := schema.GroupVersionResource{Group: "quickstart.railgrid.ai", Version: "v1alpha1", Resource: "greetings"}

actions := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	route, ok := dataplane.RouteFrom(r.Context())
	if !ok || route.Resource != greetings.Resource || route.Version != "v1" || route.Tail != "" {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}
	greeting, provider, err := dataplane.Gate(r.Context(), callers, greetings, route.Request)
	if err != nil {
		logger.Info("verb refused", "err", err) // detail stays here
		dataplane.WriteError(w, err)
		return
	}
	env := actionwire.New(r, "quickstart", route.Verb, actionwire.ResourceRef{
		APIVersion: greetings.GroupVersion().String(), Kind: "Greeting",
		Resource: greetings.Resource, Name: route.Name,
	})
	limits := dataplane.Limits{Timeout: 30 * time.Second, MaxInputBytes: 64 << 10, MaxOutputBytes: 512 << 10}
	dataplane.Serve(w, r, env, limits, func(ctx context.Context, input json.RawMessage) (any, *actionwire.Error) {
		return greet(ctx, provider, greeting, input)
	})
})

subresources, err := serve.SubresourcesFromCatalogEntryFile(manifestPath)
handler, err := serve.New(serve.Options{Name: "quickstart", Readiness: vwhealth.Handler(ready), Actions: actions, Subresources: subresources})
```

## Conformance

`conformance.Test(t, handler, conformance.Fixtures{…})` drives a handler — or,
better, the whole `serve.New` server it is mounted in — through the contract's
observable behaviour: granted verb 200, no stamped caller 401, a caller who
cannot see the object denied, foreign cluster denied, undeclared verb not
served, malformed path refused, oversized input 413, unknown input field 400.
Every request is stamped the way a shard stamps one (`X-Remote-*` headers)
**and** carries the identity and the parsed route in its context, so a bare
handler and a server behave the same.

Build the handler with a `conformance.FakeCallers` — one `Cluster`, one
granted `User`, the `Objects` the provider can see there, and an `Allow` func
answering every access review — and hand the same fake to the fixtures:

```go
callers := &conformance.FakeCallers{
	Cluster:   "aaaaaaaaaaaaaaaa",
	User:      "alice@railgrid.test",
	Objects:   []*unstructured.Unstructured{greetingObject("hello")},
	ListKinds: map[schema.GroupVersionResource]string{greetings: "GreetingList"},
	Allow:     func(a conformance.Attributes) bool { return a.Verb == "get" && a.Name == "hello" },
}
base := "/clusters/aaaaaaaaaaaaaaaa/apis/quickstart.railgrid.ai/v1alpha1/greetings/hello/"
conformance.Test(t, newServer(callers), conformance.Fixtures{
	Callers:        callers,
	GrantedPath:    base + "greet",
	DeniedPath:     base + "shout",
	MalformedPaths: []string{"/clusters/aaaaaaaaaaaaaaaa/apis/quickstart.railgrid.ai/v1alpha1/greetings/../greet"},
	MaxInputBytes:  4096,
	ExpectEnvelope: true,
})
```

Any cluster other than `Callers.Cluster` sees no objects, so "workspace A's
caller cannot reach workspace B" is observable without two live workspaces.
`conformance.ForeignCluster` is the cluster ID the suite uses for that, and
`conformance.StrangerUser` the authenticated identity it denies.
