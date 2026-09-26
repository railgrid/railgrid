# Quickstart provider

> [!IMPORTANT]
> **Read-only mirror — do not push or open PRs here.**
> The standalone [`railgrid/provider-quickstart`](https://github.com/railgrid/provider-quickstart)
> repository is **automatically synced** from the railgrid monorepo
> [`railgrid/railgrid`](https://github.com/railgrid/railgrid) (path `providers/quickstart/`)
> via [splitsh-lite](https://github.com/splitsh/lite). Every sync force-updates
> the mirror, so any direct change here is overwritten. File issues and PRs
> against [`railgrid/railgrid`](https://github.com/railgrid/railgrid) instead.
>
> This is also the canonical "copy me" template for a standalone provider repo:
> it ships its own `Dockerfile` and Helm chart (`deploy/chart/`). The image and
> chart are built and published from the railgrid monorepo CI (every PR builds
> them, so breaks are caught before the sync); the mirror itself carries no
> build workflows.

The reference railgrid provider: the smallest thing that demonstrates all three
pillars of the provider contract, written to be copied. The contract itself is
in [docs/providers.md](../../docs/providers.md) and
[docs/provider-connectivity-contract.md](../../docs/provider-connectivity-contract.md);
this is that contract as running code.

## What it demonstrates

**Pillar 1 — APIs are kcp APIs.** One kind, `Greeting`
([`apis/v1alpha1`](apis/v1alpha1)): a tenant sets `spec.message`, the provider
stamps `status.observedAt` and a `Ready` condition. The Go types are the source
of truth; `make codegen-quickstart-provider` generates the APIResourceSchema
into [`deploy/chart/files/schemas/`](deploy/chart/files/schemas), and this
binary's own `init` applies it together with the
`quickstart.providers.railgrid.ai` APIExport, the APIExportEndpointSlice and the
bind grant. Nothing about a Greeting lives in a database, a PVC or process
memory.

One reconciler ([`controller/greeting`](controller/greeting)) watches Greetings
in **every** tenant workspace that bound the export, through
`provider-sdk/apiexportprovider` — one workqueue per workspace, `req.ClusterName`
selecting the client. It runs under `provider-sdk/leaderelection` so more than
one replica is safe, and it polls nothing: no `resyncPeriod`, no `RequeueAfter`,
no ticker. See [`controller_manager.go`](controller_manager.go).

**Pillar 2 — REST is only for verbs, and a verb is a kcp custom subresource.**
Exactly one route carries tenant traffic, and it is a Kubernetes API path:

```
POST /clusters/{clusterID}/apis/quickstart.providers.railgrid.ai/v1alpha1/greetings/{name}/greet
```

The manifest declares `greet` on the `greetings` resource under
`spec.export.resources[]`; codegen publishes it on the APIExport as a custom
subresource whose storage points at this provider's `DataPlaneEndpointSlice`. A
caller addresses it on whichever kcp front door they hold a credential for — the
hub's `/clusters/{id}` for a user or a tenant ServiceAccount, `kubectl` included
— and kcp authorizes `create` on `greetings/greet` with ordinary RBAC, then
reverse-proxies the request to this binary with the caller's identity stamped in
`X-Remote-User` / `X-Remote-Group` headers. There is **no hub-proxied spelling**
of a verb: nothing under `/services/providers/quickstart/` carries tenant data,
and the provider refuses a verb request that arrives with a bearer instead of a
stamped caller (401).

Plus `/healthz` and `/readyz`. The layout is not hand-built: [`main.go`](main.go)
passes one handler per route class to `provider-sdk/serve`, which refuses
anything that is not one of them — and refuses to mount a data plane at all
without the manifest's `Subresources` table, so a missing manifest is a startup
error rather than a provider that quietly serves no verb. There is no `/api/*`,
and there will not be: if the UI needs to list, create or edit a Greeting it does
that against kcp, because a Greeting is a bound CR and a backend route that
mirrored it would be a deviation even when authorized correctly.

[`server/greet.go`](server/greet.go) is the file to read. It reads the route
`serve`'s adapter parsed from the request context (`dataplane.RouteFrom`) and
runs the one gate through `provider-sdk/dataplane`, **as the provider**:

1. **Visibility.** The caller must be able to see `greetings/{name}` in
   `{clusterID}`. The provider holds the caller's name and groups and no
   credential to read with, so `dataplane.Gate` creates a **SubjectAccessReview**
   for `get` on the object on the caller's behalf, then reads the object as
   itself — both through the provider's APIExport virtual workspace, the one
   door where it has standing in a tenant workspace. That is what makes
   "workspace A's caller cannot greet workspace B's Greeting" true, and it hands
   the object back so the verb reads `spec` from what the caller was entitled to
   see. kcp serves `SubjectAccessReview` there only for an export that claims
   `authorization.k8s.io/subjectaccessreviews`, which is the one permission
   requirement in [`manifest.yaml`](manifest.yaml) (`spec.requires`).
2. **The verb grant is not repeated.** kcp already authorized `create` on
   `greetings/greet` before it forwarded the request. `create` is not
   negotiable: the hub materializes every data-plane grant as exactly that rule.

A denial is `404`, never `403`, so a caller cannot learn whether the object
exists. The caller's name — from the stamped identity, `dataplane.ProxiedIdentityFrom`
— labels the greeting and authorizes nothing.
[`server/server_test.go`](server/server_test.go) drives this handler — mounted
in the same `serve.New` server `main.go` builds, with the routes read from this
very `manifest.yaml` — through `provider-sdk/dataplane/conformance`, the same
suite every provider's data plane is held to.

**Pillar 3 — the UI is a custom element.** [`portal/`](portal) builds one IIFE
`main.js` (Vite), embedded by [`assets.go`](assets.go) and served by the hub at
`/ui/providers/quickstart/`, SRI-pinned from
`CatalogEntry.status.ui.mainJSIntegrity`. It registers two elements:

- `<railgrid-provider-quickstart>` — the provider's page.
- `<railgrid-dashboard-tile-quickstart>` — the optional dashboard card, built on
  `portalkit/dashboardtile.ts`.

The host sets `element.railgridContext` as a **JS property, not an attribute**,
and re-sets it on every change (workspace switch, theme, token rotation). The
setter is the element's only lifecycle hook that matters: react to it, never
poll for it. Everything the element reads goes through the host-owned transport,
`portalkit/tenant.ts providerFetch(ctx)` — Greetings are listed and created with
`createKubeClient({ fetch: providerFetch(ctx), cluster: ctx.tenant })` over
`/clusters/{id}`, and the `greet` verb goes to the same place — it is a kcp
custom subresource, so the element POSTs to
`kube.verbPath(greetings, name, 'greet')`
(`/clusters/{id}/apis/…/greetings/{name}/greet`) through the same transport.
The provider's backend URL is never addressed from the browser. Navigation is a
`railgrid-navigate` CustomEvent; the element imports no router. The element
renders into light DOM so the portal stylesheet cascades in.

`portal/src/portalkit/` is a vendored copy of `provider-sdk/portalkit` — never
edit it; edit the canonical copy and run `make sync-portalkit`.

## How it is registered

The provider is described by two objects, both applied by an admin into
`root:railgrid:system:providers`:

- [`provider.yaml`](provider.yaml), kind `Provider` — the provisioning record.
  The hub's Provider controller creates the workspace
  `root:railgrid:providers:quickstart`, the `provider` ServiceAccount and the
  kubeconfig Secret. Nothing else.
- [`manifest.yaml`](manifest.yaml), kind **`CatalogEntry`** — the four
  questions, one section each: `spec.export` (the APIExport name and the verbs
  and actions served on each resource), `spec.requires` (everything needed from
  a group this provider does not own, which is where permission claims are
  written), `spec.serving` (routing, the portal entry, self-hosting) and
  `spec.hub` (what it asks of the hub itself — nothing, here).
  `spec.serving.backend.healthPath` points at `/readyz`, so the hub calls this
  provider unhealthy when its watches are dead, not only when the process is
  gone.

The CatalogEntry exists twice — `manifest.yaml`, which a reviewer reads, and
`deploy/chart/templates/catalogentry.yaml`, the copy that reaches production
(self-applied by the init container). There is no third copy in Go:
[`init_cmd.go`](init_cmd.go) applies the generated APIExport as it stands and
adds no requirement of its own. The two are one promise written down twice and
must change together; `node hack/verify-provider-contract.mjs` fails the build
when they drift. Past the subresource gate this provider requires **nothing**:
it reconciles only the Greetings its own APIExport serves.

`init` and `serve` are the two subcommands, and the split matters: `init` is the
only admin-credentialed step. It applies schemas, the APIExport, the endpoint
slice and the bind grant, then exits. `serve` runs with the minted provider
ServiceAccount and never holds an admin credential.

The heartbeat (`provider-sdk/hubclient`) POSTs to the hub so the catalog
controller's TTL does not flip the entry to NotReady. The hub records **any**
beat as liveness and ignores the body, so `CanSend` is what makes the signal
mean anything — here it is `vwState.Check() == nil`, the same readiness behind
`/readyz`. A provider whose watches are dead stops beating and the TTL turns it
red on its own.

## Run it locally

Four terminals' worth of `make` targets, in order. Everything runs on the host —
no kind, no Helm.

```sh
make run-hub-embedded-static      # hub + embedded kcp on :9443 / :6443
make install-provider-quickstart  # admin: apply provider.yaml + manifest.yaml
make init-provider-quickstart     # mint the runtime kubeconfig, run `init`
make run-provider-quickstart      # run the binary on :8081
```

Two caveats, both worth knowing before you copy this:

- `init-provider-quickstart` points `init` at the chart's `files/` directory,
  which holds both objects `init` applies: the generated APIExport and the
  `Greeting` schema. To re-run it by hand once the target has written
  `.kcp/quickstart-runtime.kubeconfig`:

  ```sh
  RAILGRID_PROVIDER_KUBECONFIG=.kcp/quickstart-runtime.kubeconfig \
  QUICKSTART_WORKSPACE_PATH=root:railgrid:providers:quickstart \
  RAILGRID_KCP_DIR=providers/quickstart/deploy/chart/files \
    ./bin/quickstart-provider init
  ```

  In a container the same directory is baked at `/etc/railgrid/kcp`, which is
  the default when `RAILGRID_KCP_DIR` is unset.

- `run-provider-quickstart` does not set `RAILGRID_PROVIDER_KUBECONFIG`, so the
  controller manager starts disabled and the `greet` verb fails closed (the
  portal still serves). It does set `RAILGRID_CATALOGENTRY_FILE`, which serve
  requires: with no manifest there is no data plane, and the binary refuses to
  start. Export the kubeconfig and `make` passes it through:

  ```sh
  RAILGRID_PROVIDER_KUBECONFIG=.kcp/quickstart-runtime.kubeconfig \
    make run-provider-quickstart
  ```

Then, in the portal, enable the provider on a workspace and create a Greeting.
From a shell, the same thing as a kube API call on the hub's kcp front door —
the verb is a custom subresource of the Greeting, so `kubectl` can reach it too:

```sh
curl -sk -X POST \
  -H "Authorization: Bearer test:user-default" \
  -H 'Content-Type: application/json' \
  -d '{"input":{}}' \
  "https://console.127.0.0.1.sslip.io:9443/clusters/$CLUSTER/apis/quickstart.providers.railgrid.ai/v1alpha1/greetings/hello/greet" | jq
```

```json
{
  "requestID": "…",
  "result": { "greeting": "Hello there, user-default" }
}
```

A caller without `create` on `greetings/greet` is refused by kcp before the
provider is asked; one who holds the grant but cannot see the object, or who
addresses a workspace they are not a member of, gets `404` — the gate does not
disclose whether the object exists.

Tear it down with `make uninstall-provider-quickstart` (deleting the `Provider`
triggers full teardown of the sub-workspace).

Other targets:

| Target | What it does |
|---|---|
| `make build-quickstart-provider` | Builds `portal/dist` (npm) then the Go binary into `bin/`. |
| `make codegen-quickstart-provider` | Regenerates deepcopy, the CRD and the APIResourceSchema from `apis/`. Run it after any change under `apis/`. |
| `make e2e-provider` | The end-to-end suite: hub + embedded kcp + this binary as host subprocesses, two workspaces, the reconciler, the verb through kcp and through the hub's front door, its cross-workspace denial, and the cross-provider claim hop. |

Provider-local checks, run from `providers/quickstart/`:

```sh
go build ./... && go test ./... && go vet ./...
../../hack/tools/golangci-lint run ./...
cd portal && npm ci && npm run build && npm run typecheck && npm test
```

## The chart

[`deploy/chart/`](deploy/chart) is a complete, publishable provider chart —
Deployment, Service, ServiceAccount, and the CatalogEntry rendered into a
ConfigMap the init container self-applies into the provider workspace. See
[deploy/chart/README.md](deploy/chart/README.md) for every value.

The one input it cannot default is the credential: a Secret named by
`providerKubeconfig.secretName` with the key `kubeconfig`. Both containers mount
it at `/var/run/secrets/railgrid` and read it as `RAILGRID_PROVIDER_KUBECONFIG` —
`init` to bootstrap the workspace, `serve` to watch tenant workspaces and to act
as the provider through its export virtual workspace when the `greet` verb
decides visibility and reads its object.

Build the image from the **repository root** (the build context includes
`provider-sdk/`, which `go.mod` replaces in):

```sh
docker build -f providers/quickstart/Dockerfile -t railgrid-quickstart-provider:dev .
```

## Running it yourself

This provider can run in your own cluster instead of on the platform. railgrid
creates a workspace for it in your organization, mints a credential scoped to
that workspace alone, and generates the exact `helm` commands — under
**Providers → Self-Hosting** in the portal.

Nothing to fill in. This is the smallest possible example of the flow, which is
why it is a good one to try first.

Once installed, the provider registers itself and your workspaces enable it
exactly like the platform copy. See
[docs/byo-providers.md](../../docs/byo-providers.md) for how the flow works, and
[deploy/chart/README.md](deploy/chart/README.md) for every chart value.
