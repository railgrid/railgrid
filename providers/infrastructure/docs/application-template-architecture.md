# Application template exposure & ingress wiring

The `application` template provisions a 3-tier app (frontend + backend +
Postgres) and exposes **only the frontend**, behind oauth2-proxy, on a public
URL. This doc explains how that URL is wired to your cluster's ingress
controller and how to configure it.

See also: [credentials.md](credentials.md) (the `cloud-credentials` Secret the
OIDC client secret is bridged from).

## The exposure chain

For each `Application` instance the kro RGD materializes (see
[install/templates/application.yaml](../install/templates/application.yaml)):

```
public host (fqdn)
   │
   ▼
Ingress  ── ingressClassName: <KEDGE_INGRESS_CLASS>   (host = expose.fqdn)
   │
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

There is **no `tls:` block** on the generated Ingress. The default design
terminates TLS at the edge (Cloudflare Tunnel): the `cloudflare` ingress
controller reads the Ingress `host` and creates the DNS record + tunnel route
itself. oauth2-proxy runs with `--reverse-proxy=true --cookie-secure=true` and
trusts `X-Forwarded-*` from that edge. If you front this with a different
controller you own TLS/DNS yourself — see "Using a non-Cloudflare controller".

## The two knobs

Both are environment variables on the **provider serve** container.

| Env var | Default | What it does |
|---|---|---|
| `KEDGE_INGRESS_CLASS` | `cloudflare` | Substituted for the reserved `${kedge.ingressClass}` token in a Template's `backendConfig` before the kro RGD is authored. Ends up as the Ingress `spec.ingressClassName`. kro never sees the token — substitution happens in [backend/kro/rgd.go](../backend/kro/rgd.go) (`substituteTokens`). |
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
`${schema.spec.expose.fqdn}` for the Ingress `host`, the oauth2-proxy
`--redirect-url`, and the reported `status.url`. The tenant must NOT set `fqdn`
or `credentialsSecretName` by hand — the controller owns both.

So to turn on app exposure you must set **`KEDGE_APP_BASE_DOMAIN`**;
`KEDGE_INGRESS_CLASS` only changes *which* controller fulfils the Ingress.

A wildcard DNS record `*.<KEDGE_APP_BASE_DOMAIN>` (or per-app records created by
your controller, as Cloudflare Tunnel does) must resolve to your ingress edge.

## Configuring it — by deploy mode

### Legacy chart mode (`operator.enabled=false`)

Set the two values; the chart's
[deployment.yaml](../deploy/chart/templates/deployment.yaml) renders them onto
the serve container as `KEDGE_APP_BASE_DOMAIN` / `KEDGE_INGRESS_CLASS`:

```yaml
# values.yaml
application:
  baseDomain: apps.example.com   # empty → feature disabled
  ingressClass: cloudflare       # swap to your controller's class
```

```sh
helm upgrade --install infrastructure \
  oci://ghcr.io/faroshq/charts/kedge-infrastructure-provider --version 0.0.13 \
  --set application.baseDomain=apps.example.com \
  --set application.ingressClass=nginx \
  ...
```

### Operator mode (`operator.enabled=true`)

**This is the mode the production install runs in.** In operator mode the serve
Deployment is built in code by `EnsureProviderServe`
([operator/serve.go](../operator/serve.go)), not by the chart's
`deployment.yaml`, so the exposure config travels through the
`InfrastructureProvider` CR. Set the `operator.application.*` values; the chart's
[operator-config.yaml](../deploy/chart/templates/operator-config.yaml) renders
them into `spec.application` on the CR, and the operator stamps the two env vars
onto the serve container:

```yaml
# values.yaml
operator:
  enabled: true
  application:
    baseDomain: apps.example.com   # empty → feature disabled
    ingressClass: cloudflare       # empty → in-binary default "cloudflare"
```

```sh
helm upgrade --install infrastructure \
  oci://ghcr.io/faroshq/charts/kedge-infrastructure-provider --version 0.0.13 \
  -n kedge-prod-infrastructure-operator --create-namespace \
  --set operator.enabled=true \
  --set-file operator.providerKubeconfig=./kedge/provider-infrastructure.kubeconfig \
  --set operator.application.baseDomain=apps.example.com \
  --set operator.application.ingressClass=cloudflare \
  ...
```

Equivalently, edit the CR directly:

```yaml
apiVersion: infrastructure.kedge.faros.sh/v1alpha1
kind: InfrastructureProvider
spec:
  application:
    baseDomain: apps.example.com
    ingressClass: cloudflare
```

The operator re-reconciles the serve Deployment on the next pass (≤2 min), which
sets `KEDGE_APP_BASE_DOMAIN` / `KEDGE_INGRESS_CLASS` and rolls the pods. Leaving
`baseDomain` empty keeps the Application controller disabled; leaving
`ingressClass` empty falls back to the in-binary `cloudflare` default.

> Do **not** `kubectl set env` the serve Deployment by hand — the operator
> overwrites its `spec` on every reconcile (`existing.Spec = want.Spec`), so the
> change is reverted. Configure it on the CR instead.

## Writing exposure into your own templates

Any Template's `backendConfig` can defer the controller choice to platform
config by writing the reserved token in the Ingress:

```yaml
- id: ingress
  template:
    apiVersion: networking.k8s.io/v1
    kind: Ingress
    spec:
      ingressClassName: ${kedge.ingressClass}   # ← substituted at RGD-author time
      rules:
        - host: ${schema.spec.expose.fqdn}       # ← stamped by the controller
          ...
```

`${kedge.ingressClass}` is the only `${kedge.*}` token today. It is replaced by
a plain string substitution on the backendConfig JSON before kro parses it, so
it is safe to use anywhere a class name is valid. kro's own `${...}` references
(`${schema.spec.*}`, `${someResource.metadata.name}`) pass through untouched.

## Using a non-Cloudflare controller

`KEDGE_INGRESS_CLASS=nginx` (or `traefik`, etc.) only changes
`spec.ingressClassName`. The shipped `application` template assumes edge TLS and
edge-managed DNS (the Cloudflare model), so for a generic in-cluster controller
you also need to:

- Provide DNS for `*.<baseDomain>` → your ingress LB.
- Terminate TLS yourself — add a `tls:` block to the Ingress (e.g. cert-manager
  with an `cert-manager.io/cluster-issuer` annotation). The shipped template has
  none; fork `install/templates/application.yaml` to add it, or author your own
  template.

oauth2-proxy already trusts forwarded headers (`--reverse-proxy=true`) and sets
secure cookies, so it works behind any TLS-terminating ingress.
</content>
