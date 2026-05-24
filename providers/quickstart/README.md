# Quickstart provider

A minimal reference provider proving the kedge plugin surface end-to-end.
See [docs/providers.md](../../docs/providers.md) for the architecture this
example demonstrates.

## What it shows

- A single binary serving both the **UI** (HTML page, mounted at
  `/ui/providers/quickstart/` in the portal) and the **backend HTTP API**
  (mounted at `/services/providers/quickstart/`).
- The `postMessage` handshake (`kedge.ready` → `kedge.context`) — the page
  receives `{ user, tenant, theme, basePath }` from the portal shell.
- That the hub's auth middleware forwards the user's bearer token to the
  provider backend (the `/api/hello` response includes the
  `X-Kedge-User` header and the token length).

## Run it locally

In one terminal, the provider binary:

```sh
cd providers/quickstart
go run .
# listening on :8081
```

In another, the kedge hub (embedded kcp is the easiest path):

```sh
./bin/kedge-hub \
  --embedded-kcp \
  --static-auth-tokens=test:user-default \
  --listen-addr=:9443
```

Register the provider via its `ProviderCatalogEntry`:

```sh
kubectl --kubeconfig kcp-admin.kubeconfig \
  --context kedge-admin \
  ws use root:kedge:providers
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
docker build -t kedge-quickstart-provider:dev providers/quickstart
```

## Deploying in-cluster

Update `manifest.yaml`:

- `spec.ui.url` and `spec.backend.url` → the in-cluster Service DNS, e.g.
  `http://quickstart.providers.svc.cluster.local:8081`
- `spec.serviceAccountNamespace` → the Namespace where the Deployment runs

Then apply the manifest plus a Deployment + Service of your own. A Helm
chart for this provider arrives in Phase 4 (see `docs/providers.md`).

## What's *not* in this iteration (Phase 1A)

The platform pieces these depend on land in later phases:

- Heartbeat (`POST /api/providers/{name}/heartbeat`) — Phase 1C.
- Hub-minted `kedge-provider-kubeconfig` Secret — Phase 1B.
- A `ProviderBinding` and APIBinding flow — Phase 3.
- A "Providers" page in the portal — Phase 2.
- A first-party Helm chart — Phase 4.

For now this binary just demonstrates that an arbitrary external HTTP
service can be proxied through the hub at a stable, same-origin URL by
declaring a `ProviderCatalogEntry`. That's the foundation everything else
sits on.
