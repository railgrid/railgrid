# railgrid-operator: a managed railgrid, with the kcp-operator embedded

Status: **NOT IMPLEMENTED.** Proposal written 16 September 2026; no phase has
started. Nothing in this document describes shipped behaviour: there is no
`operator/` module, no `cmd/railgrid-operator` binary, no
`platform.railgrid.ai` API group, no `Platform` or `Provider` kind, no
`railgrid-operator` chart and no `railgrid platform` CLI group. Section 2
describes the current state and is accurate as of the date above; everything
from section 3 on is a plan. When a phase lands, update this line and move the
document out of `docs/roadmap/`.

Companion to [install-external-kcp.md](../install-external-kcp.md),
[install-embedded-kcp.md](../install-embedded-kcp.md), [helm.md](../helm.md),
[providers.md](../providers.md) and
[provider-authoring-plan.md](provider-authoring-plan.md). The provider
authoring plan is about the *author* of one provider; this plan is about the
*operator* of one platform. They share the CatalogEntry self-hosting recipe
and nothing else.

## 1. The target

Someone standing up a production railgrid today follows nine shell scripts,
hand-carries three kubeconfigs, edits a Helm values file per provider, copies
a Secret out of kcp into the host cluster for every provider, and then owns
every upgrade of kcp, the hub and eight providers by hand. The target is one
custom resource:

```yaml
apiVersion: platform.railgrid.ai/v1alpha1
kind: Platform
metadata:
  name: prod
  namespace: railgrid-system
spec:
  version: v0.1.0                      # release manifest: kcp + hub + provider versions
  domain: railgrid.example.com         # hub at https://railgrid.example.com, kcp at kcp.railgrid.example.com
  kcp:
    mode: managed                      # embedded | managed | external
    managed:
      shards: [root, theseus]
      etcd:
        endpoints: ["https://etcd.kcp-etcd.svc.cluster.local:2379"]
        tlsSecretRef: {name: etcd-client-tls}
  certificates:
    issuerRef: {name: letsencrypt-prod, kind: ClusterIssuer}
  networking:
    gatewayAPI:
      parentRef: {name: eg, namespace: envoy-gateway-system}
  hub:
    replicas: 2
    auth:
      oidc:
        issuerURL: https://idp.example.com
        clientID: railgrid
        clientSecretRef: {name: hub-oidc, key: clientSecret}
      staticTokensSecretRef: {name: hub-static-tokens}
    adminUsers: [ops@example.com]
    security: hardened                 # enforce / platform / narrow role
  providers:
    - name: edges
    - name: infrastructure
      values:
        application: {baseDomain: apps.example.com}
    - name: agents
      database: {secretRef: {name: agents-db, key: database-url}}
```

`kubectl apply` of that object, on a cluster that has cert-manager and a
Gateway API implementation, must produce within a few minutes: a two-shard kcp
behind a front-proxy, a two-replica stateless hub against it, both reachable at
the named hostnames with real certificates, and the three providers registered,
credentialed, installed and `Ready` in the catalog. `kubectl get platform prod`
must say so in one `Ready` condition, and say *which* piece is not ready when
it is not. Bumping `spec.version` must roll kcp, the hub and the providers in
that order and stop at the first thing that fails. Nothing in that flow may
require the operator of the platform to read a kubeconfig out of kcp.

The measure is the one every operator on the market is held to: the CR is the
whole API, status is truthful, deletion is clean, and the day-2 operations
(upgrade, credential rotation, scale a shard, add a provider) are edits to the
CR rather than runbooks.

What this is **not**: a replacement for the Helm charts (they stay the
build artifact the operator installs), a multi-cluster kcp (one platform, one
cluster in v1), or a tenant-facing feature (nothing here is visible to an
organization).

## 2. Where we are

Accurate as of 16 September 2026.

### 2.1 Two installs, nine scripts

`hack/install/01`–`09` are the documented install and are run verbatim by
`make e2e-install-external` and `make e2e-install-embedded`
(`test/e2e/suites/installexternal`, `installembedded`).

| Step | External multi-shard | Embedded |
|---|---|---|
| Cluster | `01-kind-cluster.sh` | same |
| cert-manager + `Issuer selfsigned` | `02-cert-manager.sh` | not needed |
| Envoy Gateway, TLS passthrough on 8443 | `03-envoy-gateway.sh` | optional |
| etcd, single member, plaintext, 8Gi | `04-etcd.sh` | not needed (kcp embedded etcd on the hub PVC) |
| kcp-operator **v0.10.0** from the upstream kustomization | `05-kcp-operator.sh` | not needed |
| `RootShard root`, `Shard theseus`, `FrontProxy`, three `Kubeconfig`s, three `TLSRoute`s, static-token CSV Secret | `06-kcp-shards.sh` | not needed |
| Hub chart, `kcp.external.enabled=true`, front-proxy kubeconfig mounted from a Secret, 2 replicas, `hostAliases` for every kcp hostname, `TLSRoute` | `07-railgrid-hub-external.sh` | `08-railgrid-hub-embedded.sh` (StatefulSet, 1 replica) |
| external-dns for Cloudflare | `09-cloudflare-dns.sh` | same |

Three things in that flow are hand-carried state: the static token
(`.railgrid-install/hub-token`), the three extracted kubeconfigs
(`.railgrid-install/kcp-*.kubeconfig`), and the gateway ClusterIP that every
pod needs as a `hostAlias` because kcp's advertised hostnames must resolve
in-cluster. `docs/install-external-kcp.md` is honest that the etcd is
dev-grade and that production wants a three-member TLS cluster, and leaves
that to the reader.

`Tiltfile.cluster` is the only place the *whole* platform is stood up in one
cluster: kcp via the upstream Tilt stack, Dex, a shared Postgres
(`providers-db` with a per-provider `CREATE DATABASE` init), every provider
via its chart with `catalogEntry.enabled=false`, plus a host-side
`<name>-register` / `<name>-init` / `provider_kubeconfig_sync` per provider.
It is a dev loop, not an install, but it is the most complete picture of what
"a running railgrid" contains.

### 2.2 What the hub needs from its host

Less than the scripts assume. With kcp configured, embedded or external,
`pkg/hub/server.go` uses the kcp config for everything: `InstallCRDs` writes
the `pkg/hub/bootstrap/crds` into kcp, and no code path in `pkg/hub` reaches
the host cluster (`rest.InClusterConfig` is only the fallback when no kcp is
configured at all). The `railgrid-hub-cluster-admin` ClusterRoleBinding that
`07-railgrid-hub-external.sh` creates, with the comment "the hub installs CRDs
into its own cluster", grants a permission the hub never uses. The hub chart
ships no ServiceAccount or RBAC of its own. That is good news for this plan:
the hub needs a kubeconfig Secret and a TLS Secret and nothing else from its
host, so the operator can hold the cluster permissions and the hub none.

The hub's kcp identity in external mode is a client-certificate kubeconfig
minted by an operator `Kubeconfig` CR with group `system:kcp:admin`. The
operator renews the certificate; client-go does not reload a kubeconfig file,
so a renewal today silently leaves the hub on the superseded certificate until
its pods restart.

The static token is mapped into kcp by hand: `hack/install/lib.sh`
`static_token_csv` re-implements the derivation in
`pkg/util/identity.NewStaticToken` (`railgrid:static:<first 16 hex of
sha256("static-token/<token>")>`) in shell, and the CSV must be wired into
`spec.auth.tokenAuthFile` on every shard **and** the front-proxy. OIDC is not
wired into the external kcp at all: `pkg/hub/kcp/embedded.go` configures kcp's
native OIDC authenticator in embedded mode, but `06-kcp-shards.sh` renders no
`spec.auth.oidc`, so an OIDC hub against the documented external kcp is not a
tested configuration.

### 2.3 How a platform provider gets installed today

Counting from `Tiltfile.cluster` `provider_pod`, the `install-provider-*` and
`init-provider-*` Makefile targets and `docs/helm.md`, a first-party provider
takes six steps across two credentials:

| # | Step | Who | Credential |
|---|---|---|---|
| 1 | `kubectl apply` the `Provider` (`admin.railgrid.ai`) and `CatalogEntry` into `root:railgrid:system:providers` | admin | kcp admin kubeconfig |
| 2 | The hub's `ProviderReconciler` creates `root:railgrid:providers:<name>`, the `provider` ServiceAccount, and writes the minted kubeconfig to Secret `<name>-kubeconfig` in that kcp workspace's `default` namespace | hub | – |
| 3 | Read that Secret out of kcp and write it into the host cluster as Secret `railgrid-provider-kubeconfig` (key `kubeconfig`) in the provider's namespace | admin | kcp admin kubeconfig + host kubeconfig |
| 4 | Create the hub token Secret the chart's `hub.tokenSecretRef` points at | admin | host kubeconfig |
| 5 | `helm upgrade --install providers/<name>/deploy/chart` with `hub.url`, `hub.internalURL`, `providerKubeconfig.secretName`, `service.port`, plus provider-specific values (Postgres DSN for `agents`, `app-studio`, `kuery`; kro, gateway and base domains for `infrastructure`; external URL for `edges`) | admin | host kubeconfig |
| 6 | The chart's init container runs `<provider> init` (`provider-sdk/install.Bootstrap`: schemas, APIExport, endpoint slice, bind grant, CatalogEntry) and the pod heartbeats | provider | minted kubeconfig |

Step 3 is the one nobody owns. `Tiltfile.cluster` has a
`provider_kubeconfig_sync` local resource for it, the Makefile has a `printf`
that builds a kubeconfig from the `provider-token` Secret, and
`providers/infrastructure/deploy/chart/values.yaml` still says the hub's
"HostSecretWriter" delivers the Secret when the hub runs with `--kubeconfig`.
No such writer exists in `pkg/`. The admin REST surface
(`POST /api/admin/providers`, `GET .../kubeconfig`, `POST
.../credentials/rotate`, `DELETE`) does steps 1 and 2 and hands back the
kubeconfig over HTTP, which is what `provider-authoring-plan.md` builds its
CLI on; it does not do steps 3 to 5 either.

Provider charts are published per provider on `providers/<name>/vX.Y.Z` tags
by `provider-release.yaml` to `oci://ghcr.io/railgrid/charts/<chart>`; the hub
chart (`railgrid-hub`, currently `v0.0.40`) ships with the repo-wide `v*`
release. There is no artifact that says which provider versions go with which
hub version; `Tiltfile.cluster` and CI use whatever is in the tree.

### 2.4 What the kcp-operator now offers

The kcp-operator has changed shape since the `v0.10.0` the install scripts
pin. On `main` (tagged `v0.33.0` on 15 September 2026, with the operator now
versioned alongside the kcp it deploys, currently kcp v0.33.0):

- **Controllers are importable.** #285 moved them out of `internal/` to
  `pkg/controller`, on `sigs.k8s.io/multicluster-runtime`. Two entry points,
  `controller.AddConfigControllers(mgr, controller.Options{Engage, Address})`
  and `controller.AddWorkloadControllers(mgr, controller.Options{Engage})`,
  register everything; `pkg/config.ParseControllerGroups` and
  `--enabled-controller-groups=config,workload` select them in the upstream
  binary. The `sdk` submodule (`github.com/kcp-dev/kcp-operator/sdk`,
  tag `sdk/v0.33.0`) carries the API types and clients.
- **Config and workload are split by the `Compiled*` objects.** #278, #282,
  #283 and #284 added `deploy.operator.kcp.io/v1alpha1` with
  `CompiledRootShard`, `CompiledShard`, `CompiledFrontProxy`,
  `CompiledCacheServer` and `CompiledVirtualWorkspace`. The *config* group
  (RootShard, Shard, FrontProxy, CacheServer, VirtualWorkspace, Kubeconfig,
  KubeconfigRBAC) orchestrates cert-manager and compiles every resolved input
  into a `Compiled*` object labelled with component labels and carrying
  certificate revisions. The *workload* group turns `Compiled*` into
  Deployments, Services and ConfigMaps. The two groups can run in different
  clusters (the `config-workload` e2e topology); whoever carries a compiled
  object across also carries the Secrets its Deployment mounts, selected by
  the labels. This is the "deployer" pattern.
- **Downstream can shape engagement and dialing.** Every `SetupWithManager`
  takes `mcbuilder.EngageOptions`, so an embedding operator can restrict which
  clusters the controllers engage. `client.Addresser` (#287) decides how the
  Shard and KubeconfigRBAC controllers reach the root shard and shards;
  `client.InCluster{}` is the default.
- **`Kubeconfig` mints identities and can provision their RBAC.**
  `spec.targetWorkspace` plus `spec.authorization.clusterRoleBindings` binds
  the group `kubeconfig:<name>` in that workspace; deletion unprovisions. That
  is a first-class alternative to the hub minting ServiceAccount tokens for
  provider credentials, and the right primitive for the hub's own identity.
- **`VirtualWorkspace` deploys any virtual-workspace-framework server** with
  `spec.command`, `spec.initContainers` and a per-container
  `kubeconfigSecretRef`. railgrid serves its virtual workspaces
  (`pkg/virtual`: agent-proxy, mcp) inside the hub process, so this is not
  needed in v1, but it is the path if they are ever split out.
- **Bundles are gone** (#293). There is no upstream "whole installation"
  object any more; an installation is the set of `RootShard`, `Shard`,
  `FrontProxy` and `Kubeconfig` objects, and composing them is the embedding
  operator's job.
- **Dependency shape.** The root module depends on `k8s.io/*` v0.36.0,
  `controller-runtime` v0.24.1, `multicluster-runtime` v0.24.1, cert-manager
  v1.20.2 API types and `k8c.io/reconciler`. railgrid's root module replaces
  `k8s.io/*` with the `kcp-dev/kubernetes` fork and pins
  `multicluster-runtime` to a v0.24.2 pre-release. `pkg/controller` imports
  `internal/resources` from its own module, which Go permits for an external
  importer of `pkg/controller`. `replace` directives in kcp-operator's own
  `go.mod` (`sdk => ./sdk`) do not apply to importers, so the `sdk` must be
  required at a tag.

### 2.5 The gaps, grouped

- **G1, no owner for the install.** Nine scripts, three kubeconfigs and a
  token file are the "API". Nothing reconciles; a drifted step is found by
  the next person who runs the script.
- **G2, the provider credential hop.** Step 3 of §2.3 is done differently in
  three places and documented as done by code that does not exist.
- **G3, no version manifest.** Which kcp, hub and provider versions are known
  to work together is implicit in the tree.
- **G4, upgrades are runbooks.** Rolling kcp, then the hub, then providers,
  with the hub restarted when its kcp certificate renews, is nowhere
  automated.
- **G5, kcp-operator is pinned two releases back**, and upstream has since
  changed the shape in exactly the way that makes embedding possible.
- **G6, external kcp has no OIDC** and the static-token mapping is duplicated
  in shell.
- **G7, over-privileged hub, under-privileged nobody.** The hub gets
  cluster-admin it does not use; the thing that actually needs to write
  Secrets and Deployments (a person, today) has no principal.
- **G8, databases and third-party prerequisites are the reader's problem.**
  Postgres for three providers, kro for one, cert-manager and a gateway for
  everything, with no preflight that says what is missing.

## 3. The design

One operator, one top-level CR, the kcp-operator controllers compiled in, and
the existing Helm charts as the thing it installs.

### 3.1 One binary, one module

A new Go module `operator/` (added to `go.work`, standalone like every
provider) with `cmd/railgrid-operator`. It is its own module for the same
reason the providers are: its dependency graph (kcp-operator root and `sdk`,
cert-manager API types, `k8c.io/reconciler`, the Helm SDK) has no business in
the hub binary, and the hub's `kcp-dev/kubernetes` replaces have no business
in the operator. The operator imports from the railgrid root module only
`apis/**` and `pkg/util/identity`, enforced by a `depguard` rule in its
`.golangci.yml`; anything else it needs from `pkg/hub` moves to `provider-sdk`
first.

The manager is a single `mcmanager.Manager` with no multicluster provider
(the local cluster only), exactly as kcp-operator's `cmd/operator/main.go`
builds it, with the cache restricted to the platform namespaces
(`cache.Options.DefaultNamespaces`). Into it go:

1. `controller.AddConfigControllers(mgr, controller.Options{Address:
   operatorclient.InCluster{}})` and `controller.AddWorkloadControllers(mgr,
   controller.Options{})` from kcp-operator: the embedded kcp-operator. Both
   groups run in-process in v1; keeping them separable is what lets a later
   phase place the workload group elsewhere.
2. The `Platform` reconciler (§3.3), which *writes* `RootShard`, `Shard`,
   `FrontProxy` and `Kubeconfig` objects and *reads* their status. It never
   renders a kcp Deployment itself.
3. The `Provider` reconciler (§3.6).
4. A `PlatformRelease` reader (§3.8), which is data, not a controller.

The operator's chart, `deploy/charts/railgrid-operator`, ships the
`platform.railgrid.ai` CRDs, the `operator.kcp.io` and `deploy.operator.kcp.io`
CRDs copied from the pinned kcp-operator release (verified in CI against the
`go.mod` version), a ServiceAccount with a ClusterRole scoped to the
resources it manages, and a leader-elected Deployment. Installing the upstream
kcp-operator in the same cluster is unsupported and detected: the operator
sets `Degraded` with reason `ForeignKCPOperator` when a Deployment in the
cluster carries the upstream `app.kubernetes.io/name: kcp-operator` label.
`spec.kcp.managed.operator: external` turns the embedded controllers off and
leaves only the CR rendering for people who must run upstream's.

### 3.2 The API

Group `platform.railgrid.ai/v1alpha1`, generated with the same
`controller-gen` pipeline as `apis/` (`make codegen` grows an operator
target), but the CRDs are installed by the operator chart, never by the hub.

**`Platform`**, namespaced, one per installation. The full example is in §1.
The spec is small on purpose; everything the operator can derive, it derives.

| Field | Meaning | Default |
|---|---|---|
| `version` | Name of the `PlatformRelease` to install (§3.8) | the operator's own release |
| `domain` | Hub hostname. kcp lives at `kcp.<domain>`, shards at `<shard>.kcp.<domain>` | required |
| `kcp.mode` | `embedded`, `managed`, `external` (§3.4) | `managed` |
| `kcp.managed.shards[]` | Shard names; the first is the root shard | `[root]` |
| `kcp.managed.etcd` | `endpoints`, `tlsSecretRef`, or `devSingleMember: true` | required unless dev |
| `kcp.managed.operator` | `embedded` or `external` | `embedded` |
| `kcp.external.kubeconfigSecretRef` | Front-proxy admin kubeconfig for a kcp the operator does not own | – |
| `certificates.issuerRef` | cert-manager issuer for every serving certificate, kcp and hub | required unless `selfSigned: true` |
| `networking.gatewayAPI.parentRef` / `networking.ingress` / `networking.none` | How hostnames reach Services (§3.7) | required |
| `networking.trustedProxyCIDRs` | Passed through to `hub.trustedProxyCIDRs` | `[]` |
| `hub.replicas`, `hub.resources`, `hub.image` | Hub workload | `2`, chart defaults, release manifest |
| `hub.auth.oidc`, `hub.auth.staticTokensSecretRef`, `hub.auth.disableTokenLogin` | Hub *and* kcp authentication (§3.4) | one of the two required |
| `hub.adminUsers`, `hub.security` | `hub.adminUsers`; `security: default` or `hardened` maps onto the five `hub.security.*` chart values | `[]`, `default` |
| `hub.values` | Escape hatch: raw `railgrid-hub` chart values merged last; keys the operator owns are rejected | `{}` |
| `providers[]` | Inline `Provider` specs (§3.6); each is materialized as a `Provider` owned by the Platform | `[]` |
| `database.external.secretRef` | A Postgres DSN with `CREATE DATABASE` rights; per-provider databases are created from it | – |

Status: `conditions` (`Ready`, `KCPReady`, `HubReady`, `ProvidersReady`,
`Degraded`), `observedVersion`, `kcp.frontProxyURL`, `hub.externalURL`,
`hub.internalURL`, `providers[]{name, ready, version}`.

**`Provider`**, namespaced, one per installed platform provider. Usually
created by the `Platform` from `spec.providers[]`; can also be created on its
own against an existing `Platform` (`spec.platformRef`) so that adding a
provider is not a Platform edit.

On the name: a `Provider` kind already exists in `admin.railgrid.ai`
(`apis/admin/v1alpha1`), but it lives inside kcp, in
`root:railgrid:system:providers`, and is never a CRD in the host cluster. The
operator's `Provider` is the only `providers` resource a host cluster has, so
`kubectl get providers -n railgrid-system` is unambiguous, and the two are
in different groups. Where this document has to name both, the kcp one is
"the admin `Provider`". The operator creates the admin `Provider` on the
user's behalf (§3.6), so a person running a managed platform only ever
touches this one.

```yaml
apiVersion: platform.railgrid.ai/v1alpha1
kind: Provider
metadata:
  name: agents
  namespace: railgrid-system
spec:
  platformRef: {name: prod}
  chart:                      # omitted for first-party names: comes from the release manifest
    repository: oci://ghcr.io/railgrid/charts
    name: railgrid-agents-provider
    version: 0.4.2
  namespace: railgrid-providers
  values: {}                  # merged over what the operator computes
  database:
    name: agents              # created from Platform.spec.database; DSN Secret written for the chart
  requires:                   # preflight, surfaced as conditions
    - crd: resourcegraphdefinitions.kro.run
```

Status: `conditions` (`Registered`, `Credentialed`, `Installed`,
`CatalogReady`, `Ready`), `workspacePath`, `helmRelease{name, revision,
chartVersion}`, `catalogEntry{version, lastHeartbeat}`.

### 3.3 Reconciling a Platform

Level-driven, one pass, each stage gated on the previous stage's condition.
The order is the dependency order and also the upgrade order.

1. **Preflight.** cert-manager CRDs present, the named issuer exists and is
   `Ready`, the Gateway API CRDs are present when `gatewayAPI` is set, the
   `PlatformRelease` named by `spec.version` is known. A failure is a
   `Degraded` condition with a reason a human can act on
   (`IssuerNotReady`, `GatewayAPIMissing`), and the reconcile stops here.
   Nothing is created on a failed preflight.
2. **Identity Secrets.** Generate what the user did not provide: the kcp
   static-token CSV from `hub.auth.staticTokensSecretRef` using
   `identity.NewStaticToken` (the shell copy in `lib.sh` is deleted in the
   same PR), and the hub serving TLS via a cert-manager `Certificate` for
   `<domain>`.
3. **kcp** (§3.4). Write the operator CRs, wait for `RootShard`, every
   `Shard` and the `FrontProxy` to report `Available`. Write a `Kubeconfig`
   named `<platform>-hub` targeting the front-proxy with group
   `system:kcp:admin`, and wait for its Secret.
4. **Hub** (§3.5). Install or upgrade the `railgrid-hub` chart with the
   computed values. Wait for the Deployment to be available and `/healthz`
   to answer through the in-cluster Service.
5. **Networking** (§3.7). Routes for the hub, front-proxy and shards.
6. **Providers** (§3.6). Ensure one `Provider` per `spec.providers[]`,
   owned by the Platform; `ProvidersReady` is the conjunction of their
   `Ready` conditions.

The Platform's `Ready` is true when steps 3 to 6 are; `observedVersion` moves
only then, which is what makes an upgrade observable as a single transition.

### 3.4 kcp: three modes

**`managed`** is the production mode and the reason this operator exists. The
Platform reconciler renders, into its own namespace, exactly what
`06-kcp-shards.sh` renders today, generalized:

- One `RootShard` named after `shards[0]`, N-1 `Shard`s, one `FrontProxy`,
  all with `spec.auth.serviceAccount.enabled`, `spec.auth.tokenAuthFile`
  pointing at the generated CSV Secret, and, when `hub.auth.oidc` is set,
  `spec.auth.oidc` with the same issuer, client ID, CA and claim settings the
  hub gets. kcp's own OIDC authenticator then accepts the same bearer tokens
  the hub does, which closes G6 and is what the embedded mode already does in
  `pkg/hub/kcp/embedded.go`.
- `spec.etcd` from `kcp.managed.etcd` with `prefix: /shard/<name>` per shard
  (the `EtcdConfig.Prefix` field, #262, replaces the `--etcd-prefix`
  `extraArgs` the script uses). `devSingleMember: true` renders the same
  single-member StatefulSet as `04-etcd.sh`, labelled dev-grade in status,
  for kind and CI. Production etcd is a prerequisite, not something the
  operator runs (§5).
- Hostnames from `domain`: `spec.external.hostname: kcp.<domain>` on the
  RootShard and FrontProxy, `shardBaseURL: https://<shard>.kcp.<domain>:<port>`
  and the matching `certificateTemplates.server.dnsNames`. Whether `hostAliases`
  are needed depends on `networking` (§3.7); the operator renders them into
  `deploymentTemplate` when they are.
- `spec.certificates.issuerRef` from `certificates.issuerRef`, and
  `spec.cache.embedded.enabled: true` on the root shard.
- The feature gates the hub requires (`WorkspaceMounts`, `CacheAPIs`) come
  from the release manifest, not the user.

The embedded kcp-operator controllers do the rest. The Platform owns every
object it renders, so `kubectl delete platform` tears kcp down through the
usual owner-reference cascade; the etcd data is not the operator's, so it is
left alone.

**`embedded`** keeps the single-binary shape (`kcp.embedded.enabled=true` in
the hub chart, one-replica StatefulSet, PVC). The operator still owns the hub
install, TLS, routes and providers, so a small install gets the same provider
plumbing and upgrade story. No kcp-operator objects are rendered. This is the
mode for edge boxes and CI, and it is what makes the operator usable before
`managed` is finished.

**`external`** takes `kubeconfigSecretRef` to a kcp the operator does not
own, skips step 3 entirely, and cannot wire static tokens or OIDC into kcp.
Status says so (`KCPReady` with reason `ExternalUnmanaged`). It exists for
people already running a kcp with upstream's operator.

### 3.5 The hub

The hub is installed with the Helm SDK, in-process, from the `railgrid-hub`
chart at the version the release manifest names. The chart stays the single
source of truth for the hub workload, the operator only computes values. The
alternative, rendering the hub Deployment in Go the way kcp-operator renders
shards, is discussed in §5; it is the cleaner end state and the worse first
step, because it means two descriptions of the hub pod until the chart is
retired.

Values the operator computes and refuses in `hub.values`:

| Chart value | Source |
|---|---|
| `kcp.embedded.enabled`, `kcp.external.enabled`, `kcp.external.existingSecret` | `kcp.mode`; the `Kubeconfig` Secret from §3.3 step 3 |
| `replicaCount` | `hub.replicas` (forced to 1 for `embedded`) |
| `hub.hubExternalURL`, `hub.internalURL` | `https://<domain>`, `https://<release>-railgrid-hub.<ns>.svc.cluster.local:9443` |
| `hub.staticAuthTokens`, `idp.*`, `hub.disableTokenLogin`, `hub.adminUsers` | `hub.auth`, `hub.adminUsers` |
| `hub.security.*` | `hub.security: hardened` sets `providerHeartbeatAuth: enforce`, `providerDelegatedTokens: platform`, `providerWorkspaceClusterAdmin: false`, `providerHubAccessPlatformDefault: false` |
| `hub.tls.existingSecret` | the cert-manager `Certificate` from §3.3 step 2 |
| `hub.trustedProxyCIDRs`, `hostAliases`, `ingress.*` | `networking` |
| `image.hub.tag`, `kcp.featureGates` | the release manifest |

Two things the chart cannot do today and the operator does:

- **Rotation restarts.** kcp-operator's `Kubeconfig` renews the hub's client
  certificate in place. The operator stamps the Secret's `resourceVersion` as
  a pod-template annotation (`platform.railgrid.ai/kcp-kubeconfig-revision`)
  through a `podAnnotations` value, so a renewal is a rolling restart. The
  same for the serving certificate.
- **No host RBAC for the hub.** The operator's chart is the only thing with
  cluster permissions. The `railgrid-hub-cluster-admin` binding and its stale
  comment go from `07-railgrid-hub-external.sh` in phase 0, before any of
  this, because §2.2 shows it is unused.

Drift: the operator re-runs `helm upgrade` when the hash of (chart version,
computed values) changes or on a resync interval, and reports the release
revision in status. Helm release history lives in the release Secrets as it
does today, so `helm history railgrid-hub -n railgrid-system` keeps working
for a human debugging it.

### 3.6 Providers: the Provider reconciler

This is G2 fixed. The reconciler performs §2.3 as one level-driven sequence,
with the operator holding both credentials the human holds today:

1. **Register.** Create the admin `Provider` (`admin.railgrid.ai`) and the
   `CatalogEntry` in `root:railgrid:system:providers` through the hub's kcp front-proxy
   kubeconfig (the same `Kubeconfig` Secret the hub mounts, or a second
   `Kubeconfig` scoped by `targetWorkspace` + `authorization` to that one
   workspace, §5). The `CatalogEntry` comes from the chart: the provider
   charts already render it (`catalogEntry.enabled`), and the
   authoring plan's phase 1 moves it into the binary and has `init` apply it.
   The operator therefore installs the chart with `catalogEntry.enabled=true`
   and creates only the admin `Provider`; the init container does the rest. Until a
   provider adopts the embedded manifest, the operator applies the
   `manifest.yaml` shipped in the chart's `files/`. Condition `Registered`.
2. **Wait for the minted credential.** Watch Secret `<name>-kubeconfig` in
   kcp `root:railgrid:system:providers/default`, written by the hub's
   `ProviderReconciler`. Copy it into the host cluster as Secret
   `providers.KubeconfigSecretName` (`railgrid-provider-kubeconfig`, key
   `kubeconfig`) in `spec.namespace`, owned by the Provider. Create the
   hub token Secret from the same minted token for `hub.tokenSecretRef`.
   Condition `Credentialed`. This replaces `provider_kubeconfig_sync`, the
   Makefile `printf`, and the infrastructure chart's imaginary
   `HostSecretWriter`; the chart comment is corrected to name the operator.
3. **Database**, when `spec.database` is set: run a Job against
   `Platform.spec.database.external.secretRef` that does `CREATE DATABASE IF
   NOT EXISTS <name>` and a per-database role, and write the resulting DSN as
   Secret `<name>-db` for the chart's `store.databaseURLSecretRef`. This is
   `Tiltfile.cluster`'s `providers-db-init` as a controller.
4. **Preflight `spec.requires`.** Missing CRDs are a `Degraded` reason, not a
   crash-loop of the provider pod.
5. **Install.** `helm upgrade --install` of `spec.chart` into
   `spec.namespace` with the computed values: `hub.url` and `hub.internalURL`
   from the Platform's internal URL, `hub.externalURL` from `domain`,
   `providerKubeconfig.secretName`, `hub.tokenSecretRef`, `service.port` from
   the release manifest, then `spec.values` merged over. Condition
   `Installed` when the Deployment is available.
6. **Catalog gate.** Read the `CatalogEntry` back from kcp: `Ready` in the
   hub's sense means valid endpoints and a fresh heartbeat. Condition
   `CatalogReady`, and `Ready` overall.

Deletion runs the sequence backwards: helm uninstall, delete the host
Secrets, delete the admin `Provider` (the hub's finalizer tears down the
workspace and its Secret). The `CatalogEntry` is deleted with the workspace.

First-party providers are named, not described: `- name: edges` resolves
chart repository, name, version, port and required values from the release
manifest. Anything else needs `spec.chart`. `spec.values` is the same
escape hatch as `hub.values`, with the same owned-key rejection.

The infrastructure provider's chart also supports
`bootstrap.kubeconfigSource: supplied`; the operator always uses `hubMinted`,
which is the flow above. Providers with special needs (`edges` is
single-replica; `infrastructure` wants kro and a gateway) declare them in the
release manifest, not in the reconciler.

### 3.7 Networking

`networking` is one of three shapes and decides how `domain` reaches Services:

- **`gatewayAPI.parentRef`.** The documented production shape. The operator
  renders `TLSRoute`s for the hub (`<domain>`), the front-proxy
  (`kcp.<domain>`) and every shard (`<shard>.kcp.<domain>`) attached to the
  named Gateway, exactly as the scripts do. Backends terminate their own TLS,
  which preserves kcp client certificates and the hub's WebSocket tunnels.
  DNS is external-dns's job; the routes carry the hostnames it needs.
- **`ingress.className`.** Hub only, via the chart's `ingress.*` values.
  kcp stays cluster-internal; the hub reaches it by Service DNS, and the
  `Kubeconfig` targets the in-cluster front-proxy Service name. Right for
  installs where nothing outside the cluster needs kcp directly, which is
  most of them; BYO providers that need the virtual-workspace URL dialable
  from outside (`docs/byo-providers.md`) need the gateway shape.
- **`none`.** Port-forward only, for CI and kind.

`hostAliases` are rendered only in the gateway shape and only when
`networking.gatewayAPI.clusterIP` is set, which is the kind case where the
public hostnames do not resolve in-cluster. On a real cluster with real DNS
nothing is rendered.

### 3.8 Releases and upgrades

A `PlatformRelease` is a YAML document, not a CRD: a list embedded in the
operator binary (`operator/releases/*.yaml`) naming, for one operator
release, the kcp image tag, the hub chart and image version, the kcp feature
gates, and for every first-party provider its chart coordinates, version,
port and the values it needs. The operator's own release is the default;
`Platform.spec.version` may name any release the binary knows. This closes
G3 and is generated, not hand-written: `cmd/release` learns a `platform`
component that snapshots the current tags of every component into a new
release file and tags the operator.

An upgrade is a change of `spec.version` or a new operator image. The
reconciler compares `observedVersion` to the target and rolls in §3.3 order:
kcp shards (kcp-operator's own rollout, gated on `Available`), then the hub
chart, then each provider, with `Ready` required between stages. A failure
leaves `observedVersion` at the old release and `Degraded` naming the stage.
There is no automatic rollback in v1; `spec.version` back to the old value
is the rollback, and it goes through the same gates.

Version skew rules the manifest enforces: the kcp image is the one the
embedded kcp-operator was released against (the operator's `go.mod` pin of
`kcp-dev/kcp-operator` decides both), the hub's `kcp-dev/kcp` dependency
must match that kcp minor, and a provider chart is only listed if the
monorepo CI built it against that hub. `make verify-release-manifest` fails
the build when the pins disagree.

### 3.9 Distribution and the CLI

- **Chart** `deploy/charts/railgrid-operator`, published with the platform
  charts by `helm-images.yaml`, CRDs included. `helm install railgrid-operator
  oci://ghcr.io/railgrid/charts/railgrid-operator -n railgrid-system
  --create-namespace` is the entire prerequisite before `kubectl apply` of a
  `Platform`.
- **Image** `ghcr.io/railgrid/railgrid-operator`, built by `images.yaml`
  with the others.
- **CLI**: a `railgrid platform` group with three commands, all thin:
  `install` (helm-installs the operator chart and applies a `Platform`
  rendered from flags), `status` (the conditions table with reasons, the same
  data as `kubectl get platform -o yaml`, readable), and `upgrade --version`
  (edits `spec.version` and waits). Nothing the CLI does is unavailable to
  `kubectl`; it exists so the first install is one command. It follows the
  `edge` command pattern in `pkg/cli/cmd`.

### 3.10 What happens to the scripts, Tilt and e2e

`hack/install/` stays, because `docs/install-*.md` are step-by-step
explanations and the e2e suites prove them. The scripts change meaning: they
become the reference for what the operator does, and each one gains a
one-line header pointing at the reconciler stage that replaces it. The
documented production path becomes a third page, `install-operator.md`, with
`hack/install/10-operator.sh` and a third e2e suite,
`test/e2e/suites/installoperator`, that applies a `Platform` in `managed`
mode with `devSingleMember` etcd and `networking.none` on kind and asserts
the same tenancy CRUD the other two do, plus one provider `Ready`.

`Tiltfile.cluster` is the biggest beneficiary and the last to move: its
`provider_pod`, `*-register`, `*-init` and `provider_kubeconfig_sync`
resources are exactly §3.6. Once `Provider` exists, the Tiltfile applies
`Provider`s with `image.repository` overrides and Tilt's live-update keeps
the inner loop. That is phase 5 and it deletes several hundred lines of
Starlark.

## 4. Phases

Each phase is one or two PRs and leaves `make verify` and the two existing
install suites green. Nothing before phase 3 touches kcp-operator code.

| Phase | Deliverable | Closes |
|---|---|---|
| 0 | Housekeeping the research found: delete the unused `railgrid-hub-cluster-admin` binding and its comment from `07-railgrid-hub-external.sh` and the doc; correct the infrastructure chart's `HostSecretWriter` comment; move `static_token_csv` behind a tiny `railgrid-hub print-static-token-csv` hidden command so the shell copy of the identity derivation goes. Half a day. | G6 (half), G7 (half) |
| 1 | `operator/` module, `cmd/railgrid-operator`, `platform.railgrid.ai` CRDs, the operator chart, and the `Platform` reconciler for `kcp.mode: embedded` only: TLS, hub chart via Helm SDK, `networking` (all three shapes), status conditions. e2e: `installoperator` suite on kind with embedded kcp. Ships an operator that is useful on day one for the small install. | G1 (embedded), G4 (hub) |
| 2 | `Provider` reconciler (§3.6) including the database Job and `requires` preflight; the first release manifest (§3.8) with every first-party provider; `Platform.spec.providers[]`. e2e adds `quickstart` and `edges` as `Provider`s. | G2, G3, G8 |
| 3 | `kcp.mode: managed`: bump `kcp-dev/kcp-operator` to the `v0.33.0` line in the operator module, embed the config and workload controller groups, render RootShard/Shard/FrontProxy/Kubeconfig with static tokens and OIDC, `devSingleMember` etcd, rotation restarts, `ForeignKCPOperator` detection. `05-kcp-operator.sh` and `06-kcp-shards.sh` become "what phase 3 renders". e2e `installoperator` switches to `managed`. | G1, G5, G6, G7 |
| 4 | Upgrades: `observedVersion`, ordered rollout with gates, `cmd/release platform`, `make verify-release-manifest`, `railgrid platform install/status/upgrade`, `docs/install-operator.md`. | G4 |
| 5 | `Tiltfile.cluster` on `Provider`; `kcp.mode: external`; CloudNativePG-backed `database.managed` (§5). | – |

Phases 1 and 2 need no kcp-operator change and deliver most of the
day-to-day value (one CR, providers plumbed, upgrades of hub and providers).
Phase 3 is the one that embeds kcp-operator and is where the title of this
document is earned.

## 5. Decisions to make

- **Helm SDK vs rendered objects for the hub and providers.** §3.5 and §3.6
  use the Helm SDK: the charts are the tested, published artifact, provider
  charts are heterogeneous, and `helm history` keeps working. The cost is a
  values-shaped seam inside the operator and a second description of the hub
  pod if the chart is ever retired. Rendering the hub in Go the way
  kcp-operator renders shards is the cleaner end state. Recommended: Helm SDK
  in v1 for both, revisit for the hub once `Platform` is stable.
- **Embed kcp-operator or depend on it being installed.** Embedding (§3.1)
  gives one install, one version pin and one thing to upgrade, at the cost of
  the exclusivity rule and a bigger operator binary. Depending on upstream's
  operator is what the scripts do today and leaves two operators to keep in
  step. Recommended: embed, with `kcp.managed.operator: external` as the
  escape hatch and `ForeignKCPOperator` detection.
- **Provider credentials: hub-minted ServiceAccount token vs operator
  `Kubeconfig`.** §3.6 keeps the hub minting (step 2 of §2.3), because the
  hub's rotation endpoint, TokenReview-based heartbeat auth and the
  provider-sdk's token resolution all assume it. A `Kubeconfig` with
  `targetWorkspace: root:railgrid:providers:<name>` and
  `authorization.clusterRoleBindings` would give client-cert credentials with
  operator-managed renewal and no Secret copy. Recommended: hub-minted in v1,
  and file the `Kubeconfig` path against the security remediation plan as a
  follow-up.
- **The operator's own kcp identity.** One `Kubeconfig` with
  `system:kcp:admin` shared with the hub is simplest. A second one scoped to
  `root:railgrid:system:providers` is what least privilege asks for and what
  `Kubeconfig.spec.authorization` was built for. Recommended: two
  `Kubeconfig`s from phase 3 (the operator's writes to kcp are only admin
  `Provider` objects and one Secret read), shared in phases 1 and 2 where there is no
  operator-managed kcp yet.
- **etcd.** The operator does not run production etcd; `kcp.managed.etcd`
  points at one. `devSingleMember` exists for kind and CI only and is
  labelled dev-grade in status. Whether to add an `etcd-druid` integration
  later depends on demand.
- **Postgres.** `database.external.secretRef` plus a `CREATE DATABASE` Job
  in phase 2. A `database.managed` mode that renders a CloudNativePG
  `Cluster` when its CRDs are present is phase 5. The operator never runs a
  Postgres StatefulSet itself.
- **Namespaced vs cluster-scoped `Platform`.** Namespaced, one per
  installation, so two Platforms can coexist on one cluster for staging and
  the operator's cache can be namespace-scoped. The kcp-operator objects it
  renders are namespaced too.
- **Where the CatalogEntry comes from.** §3.6 defers to the chart today and
  the embedded manifest after the authoring plan's phase 1. The operator
  should not grow its own copy; there are already three.

## 6. Out of scope

Multi-cluster kcp (config and workload groups in different clusters), a
tenant-visible surface of any kind, backup and restore of etcd, BYO-provider
edge transport, the audit sink (`audit-telemetry.md` adds a chart value the
operator will pass through, nothing more), and any change to how a provider
is written. The provider authoring plan's CLI (`railgrid provider install`)
and this plan's `Provider` install the same chart with the same values;
when both exist, the CLI's `--platform` path should create a `Provider`
rather than run Helm itself, and that is a one-line note in that plan, not a
dependency between them.
