# Application template exposure & Gateway API wiring

The `application` template provisions a 3-tier app (frontend + backend +
Postgres) and exposes **only the frontend**, behind oauth2-proxy, on a public
URL. This doc explains how that URL is wired to your cluster's Gateway API
edge and how to configure it.

See also: [credentials.md](credentials.md) (the `cloud-credentials` Secret the
OIDC client secret is bridged from).

## The exposure chain

For each `Application` instance the kro RGD materializes (see
[install/templates/application.yaml](../install/templates/application.yaml)):

```
public host (fqdn)
   │
   ▼
HTTPRoute  ── parentRefs: <KEDGE_GATEWAY_NAME>/<KEDGE_GATEWAY_NAMESPACE>
   │            hostnames: [expose.fqdn]
   ▼
Service  <name>-oauth : 4180
   │
   ▼
oauth2-proxy  ── OIDC gate (authenticated users only)
   │  --redirect-url=https://<fqdn>/oauth2/callback
   ▼
Service  <name>-frontend : <frontendPort>
```

The backend and Postgres tiers are **never** exposed — they are reachable only
via in-cluster DNS.

There is **no TLS config** on the generated HTTPRoute. The default design
terminates TLS at the Gateway edge (Cloudflare Tunnel via cfgate): the
`cloudflare-tunnel` Gateway reads the HTTPRoute `hostnames` and creates the DNS
record + tunnel route itself. oauth2-proxy runs with
`--reverse-proxy=true --cookie-secure=true` and trusts `X-Forwarded-*` from that
edge. If you front this with a different Gateway you own TLS/DNS yourself — see
"Using a non-Cloudflare Gateway".

## The knobs

All are environment variables on the **provider serve** container.

| Env var | Default | What it does |
|---|---|---|
| `KEDGE_GATEWAY_NAME` | `cloudflare-tunnel` | Substituted for the reserved `${kedge.gatewayName}` token in a Template's `backendConfig` before the kro RGD is authored. Ends up as the HTTPRoute `parentRefs[].name`. kro never sees the token — substitution happens in [backend/kro/rgd.go](../backend/kro/rgd.go) (`substituteTokens`). |
| `KEDGE_GATEWAY_NAMESPACE` | `cfgate-system` | Same, for the reserved `${kedge.gatewayNamespace}` token → HTTPRoute `parentRefs[].namespace`. |
| `KEDGE_APP_BASE_DOMAIN` | _(unset)_ | The DNS zone apps are served under, e.g. `apps.example.com`. **Also gates the Application instance controller** — see below. |

### `KEDGE_APP_BASE_DOMAIN` gates the whole feature

The Application instance controller is **opt-in**. It starts only when BOTH
`KEDGE_APP_BASE_DOMAIN` and a runtime kubeconfig (`KRO_KUBECONFIG`, or the
in-cluster runtime) are present
([application_controller.go:37](../application_controller.go#L37)). Without the
base domain it logs `application controller: disabled` and Application
instances never get an fqdn.

The controller computes the public hostname as
([apps/host.go:58](../apps/host.go#L58)):

```
<hostnamePrefix | name>-<tenantHash>.<KEDGE_APP_BASE_DOMAIN>
```

and stamps it onto `spec.expose.fqdn`. The RGD then reads
`${schema.spec.expose.fqdn}` for the HTTPRoute `hostnames`, the oauth2-proxy
`--redirect-url`, and the reported `status.url`. The tenant must NOT set `fqdn`
or `credentialsSecretName` by hand — the controller owns both.

So to turn on app exposure you must set **`KEDGE_APP_BASE_DOMAIN`**;
`KEDGE_GATEWAY_NAME` / `KEDGE_GATEWAY_NAMESPACE` only change *which* Gateway the
HTTPRoutes attach to.

A wildcard DNS record `*.<KEDGE_APP_BASE_DOMAIN>` (or per-app records created by
your Gateway, as Cloudflare Tunnel does) must resolve to your Gateway edge.

## Configuring it — by deploy mode

### Legacy chart mode (`operator.enabled=false`)

Set the values; the chart's
[deployment.yaml](../deploy/chart/templates/deployment.yaml) renders them onto
the serve container as `KEDGE_APP_BASE_DOMAIN` / `KEDGE_GATEWAY_NAME` /
`KEDGE_GATEWAY_NAMESPACE`:

```yaml
# values.yaml
application:
  baseDomain: apps.example.com   # empty → feature disabled
  gateway:
    name: cloudflare-tunnel      # point at your Gateway
    namespace: cfgate-system
```

```sh
helm upgrade --install infrastructure \
  oci://ghcr.io/faroshq/charts/kedge-infrastructure-provider --version 0.0.13 \
  --set application.baseDomain=apps.example.com \
  --set application.gateway.name=my-gateway \
  --set application.gateway.namespace=my-gateway-system \
  ...
```

### Operator mode (`operator.enabled=true`)

**This is the mode the production install runs in.** In operator mode the serve
Deployment is built in code by `EnsureProviderServe`
([operator/serve.go](../operator/serve.go)), not by the chart's
`deployment.yaml`, so the exposure config travels through the
`InfrastructureProvider` CR. Set the `operator.application.*` values; the chart's
[operator-config.yaml](../deploy/chart/templates/operator-config.yaml) renders
them into `spec.application` on the CR, and the operator stamps the env vars
onto the serve container:

```yaml
# values.yaml
operator:
  enabled: true
  application:
    baseDomain: apps.example.com   # empty → feature disabled
    gateway:
      name: cloudflare-tunnel      # empty → in-binary default "cloudflare-tunnel"
      namespace: cfgate-system     # empty → in-binary default "cfgate-system"
```

```sh
helm upgrade --install infrastructure \
  oci://ghcr.io/faroshq/charts/kedge-infrastructure-provider --version 0.0.13 \
  -n kedge-prod-infrastructure-operator --create-namespace \
  --set operator.enabled=true \
  --set-file operator.providerKubeconfig=./kedge/provider-infrastructure.kubeconfig \
  --set operator.application.baseDomain=apps.example.com \
  --set operator.application.gateway.name=cloudflare-tunnel \
  ...
```

Equivalently, edit the CR directly:

```yaml
apiVersion: infrastructure.kedge.faros.sh/v1alpha1
kind: InfrastructureProvider
spec:
  application:
    baseDomain: apps.example.com
    gateway:
      name: cloudflare-tunnel
      namespace: cfgate-system
```

The operator re-reconciles the serve Deployment on the next pass (≤2 min), which
sets `KEDGE_APP_BASE_DOMAIN` / `KEDGE_GATEWAY_NAME` / `KEDGE_GATEWAY_NAMESPACE`
and rolls the pods. Leaving `baseDomain` empty keeps the Application controller
disabled; leaving the `gateway` fields empty falls back to the in-binary
`cloudflare-tunnel` / `cfgate-system` defaults.

> Do **not** `kubectl set env` the serve Deployment by hand — the operator
> overwrites its `spec` on every reconcile (`existing.Spec = want.Spec`), so the
> change is reverted. Configure it on the CR instead.

## Writing exposure into your own templates

Any Template's `backendConfig` can defer the Gateway choice to platform config
by writing the reserved tokens in the HTTPRoute:

```yaml
- id: httpRoute
  template:
    apiVersion: gateway.networking.k8s.io/v1
    kind: HTTPRoute
    spec:
      parentRefs:
        - group: gateway.networking.k8s.io
          kind: Gateway
          name: ${kedge.gatewayName}            # ← substituted at RGD-author time
          namespace: ${kedge.gatewayNamespace}  # ← substituted at RGD-author time
      hostnames:
        - ${schema.spec.expose.fqdn}             # ← stamped by the controller
      rules:
        - matches:
            - path: { type: PathPrefix, value: / }
          backendRefs:
            - name: ${schema.spec.name}-frontend
              port: ${schema.spec.frontendPort}
```

`${kedge.gatewayName}` / `${kedge.gatewayNamespace}` are the only `${kedge.*}`
tokens today. They are replaced by a plain string substitution on the
backendConfig JSON before kro parses it, so they are safe to use anywhere a
name is valid. kro's own `${...}` references (`${schema.spec.*}`,
`${someResource.metadata.name}`) pass through untouched.

## Using a non-Cloudflare Gateway

Pointing `KEDGE_GATEWAY_NAME` / `KEDGE_GATEWAY_NAMESPACE` at a different Gateway
(nginx-gateway-fabric, Envoy Gateway, Traefik, etc.) only changes the HTTPRoute
`parentRefs`. The shipped `application` template assumes edge TLS and
edge-managed DNS (the Cloudflare model), so for a generic in-cluster Gateway you
also need to:

- Provide DNS for `*.<baseDomain>` → your Gateway's LB.
- Terminate TLS yourself — configure an HTTPS listener with a certificate on the
  Gateway (e.g. cert-manager). The shipped template attaches to whatever listener
  the parent Gateway exposes; the Gateway owns TLS.
- Ensure the Gateway's `allowedRoutes` permits HTTPRoutes from the per-tenant
  namespaces the apps land in (the route is created in the tenant's runtime
  namespace, the Gateway typically in a platform namespace).

oauth2-proxy already trusts forwarded headers (`--reverse-proxy=true`) and sets
secure cookies, so it works behind any TLS-terminating Gateway.
</content>
