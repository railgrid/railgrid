# Application template: 3-tier app with an OIDC-guarded URL

Status: **Design proposal.** The exposure layer has since been implemented on
**Gateway API** rather than the plain `Ingress` this proposal describes: the
`application` template emits `gateway.networking.k8s.io/v1 HTTPRoute`s that
attach to a platform Gateway (default the cfgate `cloudflare-tunnel` Gateway in
`cfgate-system`), configured via `${kedge.gatewayName}` / `${kedge.gatewayNamespace}`
(`KEDGE_GATEWAY_NAME` / `KEDGE_GATEWAY_NAMESPACE`). Where this document says
`Ingress` / `ingressClassName` / `${kedge.ingressClass}` below, read HTTPRoute /
`parentRefs` / `${kedge.gatewayName}`. The current operational reference is
[providers/infrastructure/docs/application-template-architecture.md](../providers/infrastructure/docs/application-template-architecture.md).
Author: 2026-06-12
Related: `docs/infrastructure-architecture.md` (the platform/backend layer this builds on),
`providers/infrastructure/install/templates/redis-cache.yaml` (the in-graph secret-generation
pattern reused here), `providers/infrastructure/docs/credentials.md` (the tenant→data-plane
secret bridge), kcp `cache/v1alpha1` (CachedResource), `faroshq/kro-multicluster`.

## Summary

A tenant using the infrastructure provider should be able to deploy a simple
**frontend + backend + database** application, get a **URL** for it, and have that URL
**guarded by OIDC** so the app is not exposed unauthenticated to the internet.

The infrastructure provider can't do this today. Workloads land on a single shared **kro
runtime cluster** in a per-tenant namespace (`kedge-tenants-<hash>`), and the only templates
that exist (`simple-webapp`, `redis-cache`) produce `ClusterIP` Services. There is **no**
ingress/host/URL handling and **no** OIDC-on-workload handling anywhere in the repo.

This document proposes a single opinionated **`Application`** template
(`infrastructure.kedge.faros.sh`) that materializes a 3-tier app plus two net-new layers:

- An **exposure layer** — abstracted behind a plain Kubernetes `Ingress`; the concrete
  controller is chosen by platform config. The first implementation is **Cloudflare Tunnel**.
- An **auth layer** — a **per-app oauth2-proxy** in front of the frontend, supporting two
  OIDC modes: **Platform SSO** (the kedge identity, via the platform Dex) and **BYO external
  IdP** (the tenant's own Google/Entra/Okta).

Everything materializes through the existing kro backend; no new tenant-facing concept beyond
the `Application` kind is introduced.

## 1. Design principles

1. **The template emits primitives, not a controller.** The `Application` RGD produces a
   plain `networking.k8s.io/v1 Ingress`; the `ingressClassName` and any class annotations come
   from platform config. Swapping Cloudflare Tunnel for ingress-nginx or Gateway API is a
   config change, not a template change. This is the **exposure-layer seam**.
2. **Exposure follows kedge's dial-out philosophy.** Cloudflare Tunnel opens an *outbound*
   tunnel from the runtime cluster — no inbound load balancer, no public ingress IP, no
   firewall holes — the same reverse-tunnel shape kedge already relies on.
3. **The gate sits in the data path, per app.** Each app gets its own oauth2-proxy Deployment.
   Auth is enforced inline (oauth2-proxy is the Ingress upstream and proxies to the frontend),
   not by delegating to shared ingress-auth middleware. Self-contained, isolated blast radius.
4. **Two OIDC modes, one template.** The RGD always reads issuer/client-id from `spec.oidc.*`
   and the client secret from a bridged Secret. *Who populates those values* differs by mode;
   the template does not branch.
5. **No secret transits the control plane in clear text.** The OIDC client secret rides the
   existing tenant→data-plane Secret bridge; the oauth2-proxy cookie secret and the Postgres
   password are generated in-graph by one-shot Jobs (the `redis-cache` pattern). kcp never
   sees any of these values.
6. **Identity = the kcp workspace.** Tenant = the logical-cluster name of the workspace the
   `Application` CR lives in, exactly as the rest of the infrastructure provider already works.

## 2. Architecture

```
provider workspace (root:kedge:providers:infrastructure)
  │
  ├─ APIExport infrastructure.providers.kedge.faros.sh
  │    schemas: + application.infrastructure.kedge.faros.sh
  │    permissionClaims: secrets (unchanged — also carries oidc_client_secret in BYO mode)
  │
  ├─ Template "application"                ← platform catalog entry
  │    spec.schema      = frontend/backend/database/expose/oidc inputs
  │    spec.backendConfig = the 3-tier + oauth2-proxy + ingress kro resource graph
  │
  └─ (platform config, not tenant-visible)
       KEDGE_APP_BASE_DOMAIN   — Cloudflare zone apps live under
       KEDGE_INGRESS_CLASS     — exposure-layer ingressClassName (default "cloudflare")
       KEDGE_OIDC_ISSUER_URL   — platform Dex issuer (Platform SSO mode)
       KEDGE_DEX_GRPC_ADDR     — Dex client-management API (Platform SSO mode)


tenant workspace (root:kedge:orgs:<org>:<ws>)
  │
  ├─ APIBinding infrastructure                  ← user clicks Enable
  ├─ (BYO only) Secret cloud-credentials        ← holds oidc_client_secret
  └─ Application CR                             ← the app the tenant applies


provider binary
  │
  ├─ Template controller        ← establishes the application CRD + APIExport schema entry
  ├─ kro backend                ← authors the RGD on the runtime cluster from backendConfig,
  │                               after a token-substitution pass (§5)
  └─ application controller     ← cross-tenant (APIExport VW); stamps spec.expose.fqdn +
                                  spec.credentialsSecretName, bridges the BYO OIDC client
                                  secret onto the runtime cluster (Platform SSO / Dex: §7.2)


runtime cluster (shared kro cluster)            per-tenant namespace kedge-tenants-<hash>
  │
  ├─ cloudflared + cloudflare-tunnel ingress controller   ← admin-installed once
  │    watches Ingress(class=cloudflare) → programs DNS record + tunnel route
  │
  └─ materialized by kro from the RGD:
       frontend Deployment+Service
       backend  Deployment+Service                (cluster-DNS only, no Ingress)
       Postgres StatefulSet+Service+Secret+pwgen Job
       oauth2-proxy Deployment+Service + cookie-secret pwgen Job
       Ingress  host=<fqdn> → oauth2-proxy:4180   (no tls block; edge TLS at Cloudflare)
       bridged Secret cloud-credentials-<instance> (carries oidc_client_secret)
```

The exposure layer and the OIDC mode are the only two pluggable axes. Everything else is a
fixed, opinionated 3-tier graph.

## 3. Request path (once provisioned)

```
user browser
   │  https://<name>-<tenantHash>.apps.<base-domain>
   ▼
Cloudflare edge  ── terminates TLS, routes into the outbound tunnel
   ▼
cloudflared (runtime cluster) ──▶ Ingress ──▶ oauth2-proxy Service :4180
   │
   ├─ unauthenticated → 302 to OIDC issuer (platform Dex, or the tenant's IdP)
   │                    callback https://<fqdn>/oauth2/callback
   │
   └─ authenticated   → proxies to frontend Service
                          frontend ──(cluster DNS)──▶ backend Service
                                       backend ──(cluster DNS)──▶ Postgres Service
```

The backend and database have no Ingress and are unreachable from outside the namespace.

## 4. Data model — the `Application` instance

```yaml
apiVersion: infrastructure.kedge.faros.sh/v1alpha1
kind: Application
metadata:
  name: my-app
spec:
  name: my-app
  frontendImage: ghcr.io/acme/web:1.2.3
  frontendPort: 8080            # default
  frontendReplicas: 1
  backendImage: ghcr.io/acme/api:1.2.3
  backendPort: 8080             # default
  backendReplicas: 1
  database:
    enabled: true               # default
    size: small                 # small | medium | large
    version: "16"               # 15 | 16
  expose:
    hostnamePrefix: my-app       # optional DNS label; defaults to .name
    fqdn: ""                     # SERVER-INJECTED — do not set
  oidc:
    mode: byo                    # byo (default, supported) | platform (deferred — §7.2)
    emailDomains: ["*"]
    scopes: "openid email profile"
    issuerURL: ""                # BYO: required (platform mode is deferred — §7.2)
    clientID: ""                 # BYO: required (platform mode is deferred — §7.2)
  credentialsSecretName: ""      # SERVER-INJECTED — name of the bridged Secret
status:
  url: https://my-app-9f2db7ed8014.apps.example.com
  host: my-app-9f2db7ed8014.apps.example.com
  redirectURL: https://my-app-9f2db7ed8014.apps.example.com/oauth2/callback
  frontendReady: 1
  backendReady: 1
  databaseReady: 1
  oauthReady: 1
  dbConnectionSecretRef:
    name: my-app-db-credentials
    namespace: kedge-tenants-9f2db7ed8014
```

**Required tenant inputs:** `name`, `frontendImage`, `backendImage`, and (BYO mode only)
`oidc.issuerURL` + `oidc.clientID`. Everything else defaults. **`oidc.clientSecret` is
deliberately not in the schema** — it never travels through a CR spec.

Nested objects and arrays are safe: `backend/kro/simpleschema.go`'s `objectToSimpleSchema`
recurses and renders `[]elem` for arrays.

## 5. Host/URL scheme and config

**Host formula (deterministic):** `<prefix|name>-<tenantHash>.apps.<base-domain>`, where
`tenantHash` is the same 12-hex `tenantHash(tenantPath)` used to name the per-tenant
namespace. The host therefore inherits exactly the collision domain the namespace already
relies on — no new uniqueness risk. Within a namespace, the instance name is unique by
construction.

Because the host is computable **at create time** (both `name`/`prefix` and `tenantHash` are
known before kro reconciles), the portal/CLI can show the tenant the exact OAuth2 redirect URL
the moment they fill the form — which is what makes BYO IdP registration possible *before* the
app exists (see R1).

**Platform config** (env on the provider binary; plumbed via the Helm chart + dev env):

| Var | Purpose |
|---|---|
| `KEDGE_APP_BASE_DOMAIN` | Cloudflare zone apps live under |
| `KEDGE_INGRESS_CLASS` | exposure-layer `ingressClassName` (default `cloudflare`) |
| `KEDGE_INGRESS_ANNOTATIONS` | optional class-specific Ingress annotations |
| `KEDGE_OIDC_ISSUER_URL` | platform Dex issuer (Platform SSO mode) |
| `KEDGE_DEX_GRPC_ADDR` (+ CA/cert) | Dex client-management API (Platform SSO mode) |

The seed template is static YAML, so these can't be baked in. The kro backend gains a small
**kedge-level token-substitution pass** (reserved tokens `${kedge.appBaseDomain}`,
`${kedge.ingressClass}`, ingress annotations) applied to `spec.backendConfig` before
`buildRGD`. These are distinct from kro's own `${...}` references and are documented as a
reserved set.

## 6. The kro resource graph (`spec.backendConfig`)

All children set `namespace: default`, remapped to `kedge-tenants-<hash>` on the runtime
cluster (same convention as `redis-cache`/`simple-webapp`).

- **Postgres** (when `database.enabled`): `dbCredentials` Secret + `dbPwgen*`
  (ServiceAccount/Role/RoleBinding + idempotent Job minting a random password and a
  `postgres://…` URI) + `dbStatefulSet` (`postgres:${…version}`, PVC sized by `size`) +
  `dbService` (ClusterIP :5432).
- **Backend:** `backendDeployment` (`DATABASE_URL` from `${dbCredentials}/uri` when DB on) +
  `backendService` (ClusterIP; reachable only via cluster DNS).
- **Frontend:** `frontendDeployment` (`BACKEND_URL=http://${backendService}:<port>`) +
  `frontendService` (ClusterIP; **not** the Ingress backend).
- **oauth2-proxy:** `oauthCookieSecret` + `oauthPwgen*` (mint a 32-byte base64 cookie secret
  in-graph; idempotent so re-reconciles don't rotate and invalidate live sessions) +
  `oauthDeployment` (`quay.io/oauth2-proxy/oauth2-proxy`,
  `--provider=oidc`, `--oidc-issuer-url=${…oidc.issuerURL}`, `--client-id=${…oidc.clientID}`,
  `--client-secret` from `secretKeyRef ${…credentialsSecretName}/oidc_client_secret`,
  `--cookie-secret` from `${oauthCookieSecret}/cookie-secret`,
  `--redirect-url=https://${…expose.fqdn}/oauth2/callback`,
  `--upstream=http://${frontendService}:<port>`, `--reverse-proxy=true`,
  `--cookie-secure=true`, `--email-domain`, `--scope`) + `oauthService` (ClusterIP :4180).
- **Ingress** (plain `networking.k8s.io/v1`, controller-agnostic): host `${…expose.fqdn}`,
  `spec.ingressClassName: ${kedge.ingressClass}`, optional annotations, single path `/` →
  `oauthService:4180`. **No `tls:` block** — Cloudflare terminates at the edge and the
  controller auto-creates the DNS record + tunnel route from the Ingress host.

**Reference wiring:** `dbCredentials` → backend env; `backendService` → frontend env;
`frontendService` → oauth2-proxy `--upstream`; `oauthService` → Ingress backend;
`oauthCookieSecret` + bridged `credentialsSecretName` → oauth2-proxy secret args; injected
`expose.fqdn` → Ingress host + oauth2-proxy redirect/cookie domain + `status.url`.

`status` projects `url`, `host`, `redirectURL`, per-tier `*Ready` (readyReplicas), and
`dbConnectionSecretRef`. kro's aggregate `Ready` flips only when every tier is up.

## 7. Auth layer — two OIDC modes

The kedge **hub is an OIDC relying party, not a provider** (`pkg/server/auth/handler.go`
builds `oidc.NewProvider(IssuerURL)` against an upstream; it is a PKCE public client
configured with `--idp-issuer-url`/`--idp-client-id` in `cmd/kedge-hub/main.go`). The actual
issuer is **Dex**, deployed alongside (`hack/dex/`, `hack/dev/dex/dex-config-dev.yaml`). Dex
exposes full OIDC endpoints and a **gRPC client-management API**. So "platform SSO" points
oauth2-proxy at **Dex**, not at the hub.

### 7.1 Mode B — BYO external IdP (default; supported)
The supported v1 mode. The tenant registers their own OAuth2 client (Google/Entra/Okta) with
redirect `https://<fqdn>/oauth2/callback`, supplies `oidc.issuerURL` + `oidc.clientID` in the
CR, and puts the client secret under key `oidc_client_secret` in their workspace
`cloud-credentials` Secret. The **Application instance controller** reads that Secret through
the APIExport VW and writes the bridged `cloud-credentials-<name>` Secret into the runtime
per-tenant namespace; oauth2-proxy reads it via `secretKeyRef`. No new permission claim is
needed — the `secrets` `tenantScoped` claim in `manifest.yaml` already covers it.

### 7.2 Mode A — Platform SSO (deferred; NOT yet supported)
The intended "guard with kedge's own identity" mode. Because the hub is a relying party (not
an issuer), oauth2-proxy would target the platform **Dex**, and the controller would mint a
per-app Dex OAuth2 client (Dex matches redirect URIs exactly — no wildcards — so one shared
client can't serve many apps), inject `spec.oidc.issuerURL`/`clientID`, bridge the secret, and
`DeleteClient` on teardown.

**This is not implemented.** It requires hub-wide infrastructure that doesn't exist today: the
kedge Dex deployment has **no gRPC client-management API enabled** and uses **ephemeral
storage** (so minted clients wouldn't survive a restart). Wiring Dex (gRPC + persistent storage
+ a Service + TLS, plus a security review of the client-admin API) is tracked as a separate
epic. Until then `oidc.mode=platform` is rejected by the controller with an `OIDCConfigured`
condition pointing the tenant at BYO.

### 7.3 Secret handling (no clear text through kcp)
Both modes converge on the same bridged Secret key, so the RGD is identical; only *who writes
the value* differs. The oauth2-proxy **cookie secret** is generated in-graph by a one-shot Job
(the `redis-cache` pwgen pattern) — kro only ever sees a reference, never the value.

The bridged Secret is named from the instance CR's `metadata.name`, which can differ from
`spec.name`; the application controller therefore injects `spec.credentialsSecretName` so the RGD
references the right name unambiguously.

## 8. Exposure layer prerequisites (runtime cluster, admin-installed once)

Not kedge code — installed via Helm/GitOps on the shared runtime cluster:

- **Cloudflare Tunnel** (`cloudflared`) + the **cloudflare-tunnel ingress controller**, which
  watches Ingress objects of the cloudflare class and programs both the tunnel route and the
  Cloudflare DNS record per host.
- A **Cloudflare-managed zone** for `<base-domain>`. Per-app DNS records are created
  automatically from each Ingress — there is no wildcard record to pre-provision.
- **TLS at the Cloudflare edge** (Universal/Edge SSL). Per-app Ingresses need no `tls:` block;
  there is no cert-manager or wildcard-cert requirement on the cluster.

Documented in `providers/infrastructure/docs/runtime-ingress.md`, including how to swap the
ingress class for a different controller.

## 9. PR breakdown

**Status:** the BYO slice landed in one PR (#270 + a config/docs follow-up): the `apps` host
helper, the `${kedge.ingressClass}` substitution in `backend/kro`, the `application` template,
and the cross-tenant **application controller** (fqdn stamp + BYO secret bridge), plus chart/env
plumbing. Remaining items below are follow-ups; AP-5 (Platform SSO) is **deferred** behind the
Dex-infra work in §7.2.

| PR | Title | Acceptance criteria |
|---|---|---|
| **AP-1** | Exposure layer (Cloudflare Tunnel) — *ops, follow-up* | cloudflared + cloudflare-tunnel ingress controller on the runtime cluster against a Cloudflare zone; a test Ingress (cloudflare class) auto-creates the DNS record + tunnel route and serves `https://x.apps.<base>` with edge TLS; a `runtime-ingress.md` covers setup + how to swap the class. (Ops/doc, not kedge code.) |
| **AP-2** | Config + host computation + controller stamping — *landed* | `KEDGE_APP_BASE_DOMAIN`/`KEDGE_INGRESS_CLASS` plumbed; the controller injects `spec.expose.fqdn` + `spec.credentialsSecretName`; host formula + DNS-label prefix validation; token-substitution pass in `backend/kro`. Unit tests for the formula + substitution. |
| **AP-3** | `Application` Template seed — *landed* | `install/templates/application.yaml` added; applying it establishes `applications.infrastructure.kedge.faros.sh`, lists it in `APIExport.spec.schemas`, and kro accepts the RGD; `kubectl get templates` shows it. |
| **AP-4** | E2E — BYO mode — *follow-up* | `docs/credentials.md` gains an `oidc_client_secret` row; a tenant with `cloud-credentials` (incl. `oidc_client_secret`) applies an `Application` with `oidc.mode=byo`; all tiers Ready; `status.url`/`redirectURL` set; the URL 302s to the tenant's IdP; post-login lands on the frontend; frontend→backend→Postgres works; delete GCs everything. |
| **AP-5** | Platform SSO mode (Dex client minting) — *deferred (needs Dex infra, §7.2)* | hub Dex gRPC + persistent storage + Service + TLS (separate epic); provider `dex/` gRPC helper; `KEDGE_OIDC_ISSUER_URL` + `KEDGE_DEX_GRPC_ADDR` plumbed; the controller mints a Dex client + injects issuer/clientID/secret; teardown deletes it; orphan sweeper for the failure window. |
| **AP-6** | Portal/MCP surfacing (light) — *follow-up* | instance detail renders `url` + `redirectURL` (copyable) + per-tier readiness; redirect URL shown at create time (BYO). |

## 10. Risks and open questions

- **R1 — IdP redirect chicken/egg.** oauth2-proxy needs `--redirect-url` and the IdP needs the
  same URL registered, but the host isn't "real" until provisioned. Mitigated: the host is
  deterministic and computable at/before create time, so the tenant registers it first. Make
  `expose.hostnamePrefix` immutable post-create to prevent redirect drift.
- **R2 — Cloudflare Tunnel exposure.** TLS + DNS at the edge per host (no cluster certs, no
  wildcard record). Dependencies: a Cloudflare zone + API token for the controller, and a
  healthy tunnel (single data path). Per-app DNS/route creation is async, so `status.url` may
  resolve a few seconds after the Ingress lands. Because the layer is abstracted
  (`ingressClassName` + token substitution), swapping to nginx/Gateway later is config-only;
  tenant *custom* domains (non-`*.apps`) are out of v1 scope.
- **R3 — Bridged Secret name coupling.** `metadata.name` vs `spec.name` can differ. Resolved
  by injecting `spec.credentialsSecretName` server-side.
- **R4 — One large RGD (~16 resources).** Accepted (single opinionated template). Keep it
  heavily commented like `redis-cache.yaml`; revisit kro RGD composition if it grows further.
- **R5 — Platform SSO depends on Dex gRPC + client lifecycle.** Requires Dex deployed with the
  gRPC API enabled and reachable, plus credentials. Per-app clients must be GC'd on delete
  (finalizer + sweeper) or they accumulate. Dex storage must be **persistent** (kube-CRD or
  SQL) — an in-memory dev Dex drops minted clients on restart. Confirm the kedge Dex
  deployment's storage + gRPC config before committing AP-5. BYO mode has none of these
  dependencies and is the lower-risk path to ship first.
- **R6 — "Platform SSO" ≠ hub issuer.** The hub is a relying party; platform SSO points at
  Dex. If kedge later federates directly to a hosted IdP with no Dex, platform mode needs that
  IdP to offer programmatic client registration, or falls back to BYO.
- **O1 — kro conditional resources** for `database.enabled=false`. Verify the fork supports
  `includeWhen`/CEL gating; otherwise always-provision Postgres in v1 (document the toggle as a
  no-op) or ship a DB-less variant.
- **O2 — Per-app client secrets.** v1 shares one `oidc_client_secret` key across a tenant's
  apps; distinct per-app clients would need a per-key convention later.
- **O3 — Internal hops are plaintext** within the namespace (same trust domain; note for
  security review). Some IdPs (Entra/Okta) may need extra oauth2-proxy flags — keep
  `scopes`/`emailDomains` as inputs and add passthrough flags later if needed.

## 11. Decisions captured

- **Target** = the existing shared kro runtime cluster, per-tenant namespace. No edge targeting.
- **Exposure** = an abstracted plain `Ingress`; concrete impl = Cloudflare Tunnel; swappable
  via `ingressClassName` config.
- **Auth** = per-app oauth2-proxy. v1 supports **BYO external IdP** (default). Platform SSO
  (Dex-minted clients) is designed but deferred — it needs hub Dex gRPC + persistent storage.
- **Template** = one opinionated `Application` kind, not composable blocks.
- **Secrets** = OIDC client secret rides the existing tenant→data-plane bridge; cookie secret
  and DB password are generated in-graph. Nothing sensitive transits kcp.

## 12. What this doc is not

A design proposal. Code lands in the PRs in §9 after review. Nothing here changes runtime
behavior on its own.
