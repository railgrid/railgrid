# Quickstart provider

> [!IMPORTANT]
> **Read-only mirror — do not push or open PRs here.**
> The standalone [`faroshq/provider-quickstart`](https://github.com/faroshq/provider-quickstart)
> repository is **automatically synced** from the faros monorepo
> [`faroshq/faros`](https://github.com/faroshq/faros) (path `providers/quickstart/`)
> via [splitsh-lite](https://github.com/splitsh/lite). Every sync force-updates
> the mirror, so any direct change here is overwritten. File issues and PRs
> against [`faroshq/faros`](https://github.com/faroshq/faros) instead.
>
> This is also the canonical "copy me" template for a standalone provider repo:
> it ships its own `Dockerfile` and Helm chart (`deploy/chart/`). The image and
> chart are built and published from the faros monorepo CI (every PR builds
> them, so breaks are caught before the sync); the mirror itself carries no
> build workflows.

A minimal reference provider proving the faros plugin surface end-to-end.
See [docs/providers.md](../../docs/providers.md) for the architecture this
example demonstrates.

## What it shows

- A single binary serving both the **UI** (HTML page, mounted at
  `/ui/providers/quickstart/` in the portal) and the **backend HTTP API**
  (mounted at `/services/providers/quickstart/`).
- The `postMessage` handshake (`faros.ready` → `faros.context`) — the page
  receives `{ user, tenant, theme, basePath }` from the portal shell.
- That the hub's auth middleware forwards the user's bearer token to the
  provider backend (the `/api/hello` response includes the
  `X-Faros-User` header and the token length).

## Run it locally

In one terminal, the provider binary:

```sh
cd providers/quickstart
go run .
# listening on :8081
```

In another, the faros hub (embedded kcp is the easiest path):

```sh
./bin/faros-hub \
  --embedded-kcp \
  --static-auth-tokens=test:user-default \
  --listen-addr=:9443
```

Register the provider via its `ProviderCatalogEntry`:

```sh
kubectl --kubeconfig kcp-admin.kubeconfig \
  --context faros-admin \
  ws use root:faros:providers
kubectl apply -f providers/quickstart/manifest.yaml
```

Check the hub picked it up:

```sh
kubectl get providercatalogentry quickstart -o yaml
# status.conditions[Ready].status: "True"
```

Curl the backend through the hub proxy:

```sh
curl -sk -H "Authorization: Bearer test" \
  https://localhost:9443/services/providers/quickstart/api/hello | jq
```

Expected response:

```json
{
  "message": "hello from the quickstart provider",
  "provider": "quickstart",
  "servedAt": "2026-05-22T...",
  "userHeader": "",
  "tokenLength": 11
}
```

`tokenLength` proves the hub forwarded the `Authorization` header.

Open the UI in a browser:

```
https://localhost:9443/ui/providers/quickstart/
```

You should see the demo HTML page. The "Backend API" section fetches
`/services/providers/quickstart/api/hello` from the browser, proving the
backend proxy works from the page too.

## Build the image

```sh
docker build -t faros-quickstart-provider:dev providers/quickstart
```

## Deploying in-cluster

Update `manifest.yaml`:

- `spec.ui.url` and `spec.backend.url` → the in-cluster Service DNS, e.g.
  `http://quickstart.providers.svc.cluster.local:8081`
- `spec.serviceAccountNamespace` → the Namespace where the Deployment runs

Then apply the manifest plus a Deployment + Service of your own. A Helm
chart for this provider arrives in Phase 4 (see `docs/providers.md`).

### Two ways a provider's kcp credentials get bootstrapped

quickstart uses the **hub-provisioned** model: you apply the
`CatalogEntry` and the hub catalog controller creates the provider
workspace, mints the runtime `faros-provider-kubeconfig` Secret, and
applies the APIExport. quickstart doesn't read kcp itself, so it just
needs the routing — no kubeconfig.

A provider that *does* talk to kcp can also **self-bootstrap** with an
init container that holds a kcp admin kubeconfig and mints its own
runtime kubeconfig — no hub provisioning step. The infrastructure
provider demonstrates this end-to-end; see
[providers/infrastructure](../infrastructure/README.md#b-self-bootstrap-with-an-init-container-bootstrapenabledtrue)
and the "Alternative: self-bootstrap via an init container" section of
[docs/providers.md](../../docs/providers.md). When you graduate this
quickstart to a real Helm chart, copy that pattern if your provider
needs kcp access.

## What's *not* in this iteration (Phase 1A)

The platform pieces these depend on land in later phases:

- Heartbeat (`POST /api/providers/{name}/heartbeat`) — Phase 1C.
- Hub-minted `faros-provider-kubeconfig` Secret — Phase 1B.
- A `ProviderBinding` and APIBinding flow — Phase 3.
- A "Providers" page in the portal — Phase 2.
- A first-party Helm chart — Phase 4.

For now this binary just demonstrates that an arbitrary external HTTP
service can be proxied through the hub at a stable, same-origin URL by
declaring a `ProviderCatalogEntry`. That's the foundation everything else
sits on.

## Running it yourself

This provider can run in your own cluster instead of on the platform. faros
creates a workspace for it in your organization, mints a credential scoped to
that workspace alone, and generates the exact `helm` commands — under
**Providers → Self-Hosting** in the portal.

Nothing to fill in. This is the smallest possible example of the flow, which is
why it is a good one to try first.

Once installed, the provider registers itself and your workspaces enable it
exactly like the platform copy. See
[docs/byo-providers.md](../../docs/byo-providers.md) for how the flow works, and
[deploy/chart/README.md](deploy/chart/README.md) for every chart value.
