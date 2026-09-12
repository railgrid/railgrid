# Provider authoring: as smooth as `helm install`

Status: **NOT IMPLEMENTED.** Proposal written 12 September 2026; no phase has
started. Nothing in this document describes shipped behaviour: there is no
`faros provider` command group, no `provider-sdk/runtime` package, no
`faros-provider` library chart, and the CatalogEntry still exists in three
copies. Sections 2.1 to 2.3 describe the current state and are accurate as of
the date above; everything from section 3 on is a plan. When a phase lands,
update this line and move the document out of `docs/roadmap/`.

Companion to [providers.md](../providers.md), [byo-providers.md](../byo-providers.md)
and [provider-publishing.md](../provider-publishing.md).

## 1. The target

Someone who has never seen faros, using the SaaS hub with an ordinary org
account, should be able to go from nothing to a provider running and visible
in the portal with three commands, and never open a YAML file they did not
write:

```sh
faros login https://faros.example.com   # the SaaS hub, an ordinary org account
faros provider init acme                # scaffold ./acme: Go backend, portal, chart, manifest, CI
faros provider dev                      # ephemeral edge + this provider as a local process,
                                        # registered and enabled; change code, reload, repeat
faros provider install acme             # for real: register, mint credential, helm install
                                        # into a cluster connected as an edge, wait Ready
```

`faros provider quickstart` is `init` + `dev` in one command for the very
first run. No platform admin is involved at any step. The measure is the same
one `helm install` meets: one command, one credential the user never copies
by hand, idempotent, and a clear failure when a prerequisite is missing.

## 2. Where we are

### 2.1 The journey today

The quickstart is a good reference *binary*, but the journey around it is a
copy-and-edit exercise. Counting from `AGENTS.md` §5.6, `providers/quickstart/`,
the Makefile and the Tiltfile, a new provider today needs:

| # | Step | Where | Manual? |
|---|------|-------|---------|
| 1 | `cp -r providers/quickstart providers/acme` | AGENTS.md:393 | yes |
| 2 | Rename module, add to `go.work`, replace `quickstart` in 29 files (191 occurrences) | go.mod, main.go, chart, portal | yes |
| 3 | Write API types, run codegen, schemas land in `deploy/chart/files/schemas/` | Makefile:378-441 (per-provider target) | yes |
| 4 | Write `manifest.yaml` (CatalogEntry) | providers/quickstart/manifest.yaml | yes |
| 5 | Write `provider.yaml` (admin Provider record) | providers/quickstart/provider.yaml | yes |
| 6 | Copy the whole CatalogEntry spec into the chart ConfigMap | deploy/chart/templates/catalogentry.yaml | yes, drifts silently |
| 7 | Copy the permission-claim list a third time into `init_cmd.go` | providers/quickstart/init_cmd.go:53-56 | yes, drifts silently |
| 8 | Wire `main.go`: init/serve subcommands, healthz, heartbeat, portal embed | main.go (258 lines) | yes |
| 9 | Build the portal, copy-sync portalkit (11 files, ~5k lines) | hack/sync-portalkit.sh | yes |
| 10 | Write the Helm chart and the chart README (embedded as `valuesDoc`) | deploy/chart/ | yes |
| 11 | Add 8 Makefile targets and 3 Tilt resources | Makefile (70 quickstart lines), Tiltfile:155-203 | yes |
| 12 | Build and push the image and chart | provider-release.yaml, `cmd/release` | monorepo CI only |
| 13 | `kubectl apply` Provider + CatalogEntry into `root:faros:system:providers` with an admin kubeconfig | Makefile:1054-1064 | yes |
| 14 | Hub provisions workspace, SA, kubeconfig Secret | pkg/hub/providers/provider_controller.go:144-160 | automatic |
| 15 | Read the SA token out of kcp and hand-write a kubeconfig with `printf` | Makefile:1417-1426 | yes |
| 16 | `kubectl create secret` + `helm upgrade --install` | deploy/chart/README.md | yes |
| 17 | Init container bootstraps schemas, APIExport, slice, bind grant, CatalogEntry | provider-sdk/install/install.go:138 | automatic |
| 18 | Heartbeat, Ready condition | provider-sdk/hubclient | automatic |
| 19 | Enable per workspace | POST .../providers/{name}/enable | yes, by design |
| 20 | Publish a standalone mirror: deploy key, secret, splitsh workflow | provider-publishing.md:113-212 | yes |

Sixteen of twenty steps are manual. Steps 2, 6, 7, 11, 13 and 15 are pure
toil with no design content.

### 2.2 The pain, grouped

**P1. Three copies of one object.** The CatalogEntry lives in `manifest.yaml`,
in `deploy/chart/templates/catalogentry.yaml`, and its claims live a third time
in `init_cmd.go`. `AGENTS.md:173-203` documents the drift because it has already
bitten in production (missing sidebar items, stale claim sets).

**P2. Registration is not one action.** Platform onboarding is two kcp objects
applied with an admin kubeconfig, then a separate kubeconfig download
(`GET /api/admin/providers/{name}/kubeconfig`), then a Secret the user creates
by hand. The BYO path (`POST /api/orgs/{org}/providers`) already collapses
workspace + credential + rendered helm commands into one call
(`pkg/hub/restapi/org_providers.go:281`, `pkg/hub/providers/selfhosting.go:148`),
but only the portal calls it. The platform path has no equivalent, and neither
has a CLI verb. `docs/providers.md` literally says "There is no `faros` CLI
subcommand — use curl" for rotation.

**P3. No scaffold.** `faros provider init` does not exist. `faros init` runs a
hub, `faros install` installs an agent, `faros dev init` builds a kind cluster.
The root command tree (`pkg/cli/cmd/root.go:89-131`) has no provider group.

**P4. The runtime is boilerplate.** Every provider `main.go` re-implements the
same init/serve switch, healthz, log middleware, portal static serving with
index fallback, heartbeat wiring and graceful shutdown. Quickstart is 258 lines
of which about 30 are the provider's own logic.

**P5. Local dev needs the monorepo.** Running one provider needs `make
run-hub-embedded-static`, `make install-provider-X`, `make init-provider-X`,
`make run-provider-X`, plus Tilt. None of that exists for a provider that lives
in its own repo, which is the whole point of the mirror.

**P6. Publishing is monorepo-shaped.** Images and charts are only built by
`provider-release.yaml` on a `providers/<name>/vX.Y.Z` tag. The mirror carries
no workflows, so a third party who forks the quickstart cannot release without
reverse-engineering CI.

**P7. Stale guidance.** The quickstart README still lists heartbeat, the minted
kubeconfig Secret and the Helm chart as "not in this iteration" (all exist),
and the `manifest.yaml` header documents an inline `schemas[]` field that was
removed from `ProviderAPIExport`
(`apis/providers/v1alpha1/types_catalogentry.go:537-551`). First impressions
matter most in a quickstart.

### 2.3 Where a provider can run today

"Anybody can create a provider" has to include people who do not run a hub.
Three situations exist, and only the first is what the quickstart README
describes.

| Situation | Who | Provisioning | Data plane | Works today? |
|---|---|---|---|---|
| Local dev hub (embedded kcp) | you are admin | Provider + CatalogEntry into `root:faros:system:providers` | hub dials `localhost` | yes, via make targets |
| Self-hosted hub, platform provider | platform admin | same, or `/bonkers` | hub dials the in-cluster Service | yes, manual |
| SaaS org, custom provider (BYO) | org admin, no platform admin | `POST /api/orgs/{org}/providers` mints an org-scoped workspace + credential | only through a connected Kubernetes edge's tunnel | yes, with the caveats below |

The BYO path is the one that matters for "anybody", and it already does most
of the work: one call returns workspace, credential and, for platform recipes,
rendered helm steps (`pkg/hub/restapi/org_providers.go:281`). What it lacks is
ergonomics and a laptop story:

- **An edge is mandatory.** Registration refuses unless the caller names a
  connected Kubernetes cluster edge in a workspace they can see
  (`org_providers.go:466`), and the provider is reached only through that
  edge's tunnel via a hub-owned edges Service (`pkg/hub/providers/edgeroute.go:27`).
  A plain `go run .` on a laptop is unreachable. There is no macOS or server
  edge route for providers today.
- **No instructions for a custom provider.** `Instructions` is nil unless the
  name matches a platform provider with a `selfHosting` recipe. Someone who
  scaffolded their own provider gets a kubeconfig and silence.
- **A platform prerequisite is invisible until it bites.** A provider that
  reconciles tenant objects needs the shard's virtual-workspace URL to be
  publicly dialable (`docs/byo-providers.md:103`). Otherwise it comes up
  healthy and never sees a tenant resource.
- **Org policy and UI.** Registration is org-admin-only unless the org's
  `catalogEntryCreation` is `members`. Org-owned providers must not heartbeat,
  their readiness is endpoint validity, and their portal bundle is served
  through the ui-grant path (PR #696).

## 3. The design

Six changes, each independently shippable, ordered by leverage. The SaaS path
is a target of every one of them, not a special case at the end.

### 3.1 One manifest, embedded in the binary

`manifest.yaml` becomes the single CatalogEntry. It is `//go:embed`-ed into the
provider binary, and `init` applies it from there. The chart stops rendering
its own copy.

- The chart passes only what it knows and the manifest cannot: the in-cluster
  URLs and the version. Two env vars on the init container, `FAROS_UI_URL` and
  `FAROS_BACKEND_URL`, plus `FAROS_PROVIDER_VERSION` which already exists.
  `init` patches `spec.ui.url`, `spec.backend.url`, `spec.version` and
  `spec.selfHosting.chart.version` before applying.
- Claims come from the manifest. `sdkinstall.Bootstrap` grows a
  `ClaimsFromCatalogEntry` mode that maps `ProviderPermissionClaim` to
  `PermissionClaim`. Identity hashes for first-party claim groups, which the
  CatalogEntry type deliberately does not carry, come from env
  (`FAROS_CLAIM_IDENTITY_HASH_<GROUP>`), which the chart sets from values
  exactly as `selfHosting.requiredValues[].identityFor` already describes.
- GitOps users who manage the CatalogEntry separately keep
  `catalogEntry.enabled=false`; `FAROS_CATALOGENTRY_FILE` still overrides the
  embedded copy.

Result: P1 disappears. `deploy/chart/templates/catalogentry.yaml` is deleted,
`init_cmd.go` no longer lists claims, and `AGENTS.md:173-203` shrinks to one
sentence.

### 3.2 `provider-sdk/runtime`: the provider entrypoint

A new SDK package owns everything in `main.go` that is not the provider's own
logic:

```go
//go:embed manifest.yaml
var manifest []byte

//go:embed portal/dist
var portal embed.FS

func main() {
    runtime.Main(runtime.Provider{
        Manifest: manifest,          // name, version, export, claims all come from here
        Portal:   portal,            // optional; nil for a UI-less provider
        Schemas:  schemas,           // optional embed.FS of APIResourceSchemas
        Routes: func(mux *http.ServeMux, rt runtime.Context) {
            mux.HandleFunc("/api/hello", hello)
        },
        // escape hatches for the big providers
        BeforeInit: nil, AfterInit: nil, Subcommands: nil,
    })
}
```

`runtime.Main` provides: `init` and `serve` subcommands, `/healthz`, structured
request logging, portal static serving with index fallback and SRI-stable
`main.js`, heartbeat via `hubclient` (skipped automatically when the kubeconfig
path is under `:tenants:`, so a BYO copy never beats the platform endpoint),
graceful shutdown, and a `tenantaccess` dynamic-client factory on
`runtime.Context`. Schemas embed into the binary too, so the Dockerfile stops
copying `deploy/chart/files/schemas` and `FAROS_SCHEMAS_DIR` becomes an override.

Quickstart's `main.go` drops to roughly 40 lines and `init_cmd.go` is deleted.
The infrastructure provider's 279-line init keeps working via the hooks; nothing
forces the large providers to migrate.

### 3.3 A library chart: `faros-provider`

Every provider chart is the same Deployment, Service, ServiceAccount and Secret
mount with different names. Publish a Helm library chart
`oci://ghcr.io/faroshq/charts/faros-provider` and have provider charts depend on
it:

```
deploy/chart/
├── Chart.yaml          # dependencies: [{name: faros-provider, version: 0.x}]
├── values.yaml         # name, image, port, hub, resources — nothing else
├── templates/all.yaml  # {{ include "faros-provider.workload" . }}
└── README.md           # values reference, still embedded as valuesDoc
```

The library owns the init container contract (kubeconfig mount, env vars from
§3.1), probes, the hub token secret ref, and the standard labels. Bespoke
charts (infrastructure, edges) stay as they are; the library is opt-in and
quickstart migrates first. `hack/helm-build.sh` currently only walks
`deploy/charts/*/`; it gains the library and the provider charts.

### 3.4 `faros provider`: the CLI group

A new `groupProviders` in `pkg/cli/cmd/root.go`, files under
`pkg/cli/cmd/provider*.go`, following the `edge` pattern
(`pkg/cli/cmd/edge.go`) and reusing `hubSession` (`pkg/cli/cmd/hubclient.go`).

| Command | What it does | Hub surface |
|---|---|---|
| `init <name>` | Scaffold a provider directory from the embedded template. Flags: `--module`, `--ui vanilla\|vue\|none`, `--api-group`, `--port`. Prints next steps. | none |
| `dev` | The iteration loop, section 3.5. Default: ephemeral in-process edge against the hub you are logged into. `--local` runs an embedded hub instead. `--edge` reuses a persistent edge. | org register (default) or admin |
| `quickstart [name]` | `init` + `dev` in one command: scaffold, register, run, open the portal. The first-run experience. | as `dev` |
| `register <name>` | Create the provider on the hub and print the credential and instructions. Org-scoped by default (the SaaS case); `--platform` for hub admins. `--edge ws/name` picks the tunnel; with one connected edge it is chosen automatically. `-o kubeconfig-file`. | `POST /api/orgs/{org}/providers`; `POST /api/admin/providers` + `GET .../kubeconfig` |
| `install <name>` | `register`, then preflight (§3.6), then namespace + Secret + `helm upgrade --install` in-process (the CLI already links `helm.sh/helm/v3` for `faros dev`), then wait for `CatalogEntry` Ready. `--chart`, `--version`, `--set`, `--namespace`, `--kubeconfig`, `--enable` to enable in the current workspace. For a custom provider the instructions are rendered locally from the scaffolded manifest's `selfHosting` block (§3.6). | as above + enable |
| `list`, `get`, `status` | Catalog view, endpoints, last heartbeat, Ready conditions. | `GET /api/providers`, `GET /api/orgs/{org}/providers` |
| `enable`, `disable` | Per-workspace enable with claim consent shown in the terminal. | `POST .../workspaces/{ws}/providers/{name}/enable` |
| `credentials rotate` | Replaces the curl snippet in providers.md. | `POST .../credentials/rotate` |
| `delete` | Full teardown. | `DELETE` |
| `validate` | Lint `manifest.yaml` (required fields, `tenantScoped` on every claim, selfHosting coordinates, health path), `helm lint`, chart README vs values drift. Runs in the scaffolded CI. | none |

`install` is the `helm install` moment: one command, no credential copied by
hand, idempotent because both register endpoints already are.

**Template embedding.** `go:embed` cannot cross a module boundary, so the
scaffold template lives at `pkg/cli/scaffold/quickstart/` and is copy-synced
from `providers/quickstart/` by `make sync-scaffold`, with `make
verify-scaffold` in CI. This is exactly the portalkit pattern
(`hack/sync-portalkit.sh`), and it keeps `faros provider init` working offline.
Rendering is a token substitution (`quickstart` to the name, module path, port)
plus dropping the monorepo-only bits (`replace` directive, mirror notice).

**Hub change for the platform path.** `POST /api/admin/providers` returns only
201 today; `install --platform` would need a second call and a poll for the
Secret. Extend the admin service so the create response carries `kubeconfig`
and `instructions` once provisioned, rendered with the same
`RenderInstallInstructions` the BYO path uses. This also gives the portal's
`/bonkers` page the same one-click experience the org page has.

### 3.5 `faros provider dev`: the iteration loop

The loop is: change code, the provider restarts, reload the portal, repeat.
`dev` has two modes. The first is the default whenever the CLI is logged into
a remote hub, which is every custom-provider author on SaaS. The second is for
people developing the hub itself.

#### 3.5.1 Against a remote hub, with an ephemeral edge (default)

The person this is for writes a custom provider, uses the SaaS hub, is not a
platform admin, and does not want to install an agent on their machine.
`faros provider dev` therefore owns an edge that exists only while it runs.

The inner loop is: change code, the provider restarts, reload the portal,
repeat. Nothing in that loop talks to the hub except one cheap CatalogEntry
re-apply (below). Session start and end do.

*Session start:*

1. Resolve hub, org and workspace from the CLI login. Preflight (§3.6 above).
2. Create an ephemeral host edge in the current workspace:
   `faros edge create dev-<provider>-<user>-<rand> --type server` (or `macos`),
   labelled `faros.sh/ephemeral=true`, `faros.sh/owner=<user>`,
   `faros.sh/provider=<name>`. Mint its join token
   (`faros agent token create`) and run the agent **in-process**: the CLI
   already links the agent package for `faros agent run`
   (`pkg/cli/cmd/agent.go:222`), so no child binary and no launchd/systemd.
   The agent runs with a loopback-only service policy.
3. Register the provider bound to that edge, `POST /api/orgs/{org}/providers`
   with `edge: {workspace, name}`. This is idempotent across sessions, and
   `RecordProviderEdgeBinding` overwrites the previous session's edge
   (`pkg/hub/kcp/edgeroute.go:104-110`), so the provider workspace and
   credential persist while the edge is replaced each time. The credential is
   cached at `~/.faros/providers/<hub>/<org>/<name>.kubeconfig` and re-fetched
   from the kubeconfig endpoint if missing (same token).
4. The hub reconciles the hub-owned edges Service `provider-<name>` with
   `host: 127.0.0.1` and the declared port (needs the three hub changes
   below).
5. Run `init` once with the cached credential, then `serve` on the declared
   port. On the first session, enable the provider in the current workspace
   after showing the claim consent in the terminal. Print the portal URL and a
   curl for the backend.

*Inner loop:*

- Go changes: rebuild, restart `serve`. Portal changes: `vite build --watch`
  into `portal/dist`, and the SDK runtime serves the portal from disk when
  `FAROS_PORTAL_DIR` is set, so a UI change needs no Go rebuild.
- After every rebuild, re-apply the CatalogEntry with
  `spec.version: <base>-dev.<n>`. This matters: the ui-grant caches the
  bundle's SRI hash keyed by version and only re-hashes every ten minutes
  (`pkg/hub/providers/ui_grant.go:525-531`, `ui_integrity.go:53`). Without
  the bump the browser refuses the rebuilt `main.js` until the cache expires.
  With it, the next portal reload gets a fresh pin. One apply, no
  re-registration.

*Session end:* on Ctrl-C, stop `serve`, stop the agent, delete the ephemeral
edge. The provider workspace, credential and enablement stay, so the next
`dev` is instant. `dev --clean` also deletes the org provider. The route is
unusable between sessions and the portal shows the provider as unreachable,
which is correct.

*Orphans:* a crashed session leaves an edge behind. Two guards: `dev` deletes
any ephemeral edge with its own owner and provider labels on start, and the
edges provider sweeps ephemeral edges that have been disconnected for more
than an hour.

*Hub changes this needs, all small:*

- `install-targets` and the registration preflight list and accept only
  `KubernetesCluster` edges (`pkg/hub/kcp/edgetargets.go:78`,
  `pkg/hub/restapi/org_providers.go:466`). Accept `LinuxServer` and
  `MacOSServer` too.
- `EnsureProviderEdgeService` hardcodes `edgeRef.kind: KubernetesCluster` and a
  `targetRef` (`pkg/hub/kcp/edgeroute.go:264`). For a host edge, emit
  `host: 127.0.0.1` and `port` instead; the Service type already supports it
  (`providers/edges/apis/v1alpha1/types_service.go:155-181`).
- `ParseClusterServiceTarget` refuses anything but cluster DNS
  (`pkg/hub/kcp/edgeroute.go:221`). Allow a loopback backend URL only when
  the bound edge is a host edge.
- Ephemeral-edge sweeper in the edges provider, keyed on the label.

`faros provider quickstart` is `init` + `dev` in one command for the very
first run: scaffold, ephemeral edge, register, run, open the portal. A local
kind/k3d cluster as the edge remains the fallback for a provider that must
run in-cluster, with an image reload instead of a process restart.

#### 3.5.2 Against an embedded hub (`dev --local`)

For hub developers and CI. Replaces `make run-hub-embedded-static`,
`install-provider-X`, `init-provider-X`, `run-provider-X` and the three Tilt
resources:

1. Start an embedded-kcp hub in-process (`faros init` already does this) with a
   static dev token and `--hub-external-url` set.
2. Create the Provider and CatalogEntry through the admin API, not kubectl.
3. Wait for the minted Secret, write the runtime kubeconfig to the data dir.
   This replaces the `printf` kubeconfig in `Makefile:1417-1426`.
4. Same inner loop as 3.5.1, but the hub dials `localhost` directly; no edge.
5. Enable the provider in the default dev workspace and print the portal URL and
   a curl for the backend.

The existing `test/e2e/suites/provider` suite already drives this exact
sequence as a test harness; `dev --local` is the same sequence as a user
command and the suite should be refactored to call it.

### 3.6 The SaaS path: a custom provider with no platform admin

This is the path for everyone who is not a hub operator, so `register`,
`install` and `dev` default to it. Four pieces make it as smooth as the
platform path.

**Instructions for custom providers.** The hub can only render instructions
for a recipe it knows. The scaffold writes a `selfHosting` block into the
manifest (chart repository, name, namespace, release), so the CLI has the
recipe and the register response has the kubeconfig and hub URL. `install`
renders the steps locally with the same `RenderInstallInstructions` the hub
uses (same Go module, so no duplication), then executes them. Optionally the
register request grows an inline `selfHosting` field so the portal's
Self-Hosting panel can show the same steps for a custom provider; that is a
small hub change and not on the critical path.

**Edge selection.** `install` calls `GET /api/orgs/{org}/providers/install-targets`,
picks the only connected Kubernetes edge or asks with `--edge`, and passes it
to register. The helm install then targets that cluster's kubeconfig, which
`faros edge kubeconfig` already produces. The user never learns that an edges
Service exists.

**Preflight, before anything is minted.** `install` and `validate --hub` check
the things that fail silently today and refuse with a specific message:

- the org allows this caller to register (policy is `members`, or the caller
  is org admin);
- at least one connected Kubernetes edge is visible;
- the shard's virtual-workspace URL resolves and dials from the machine
  running the CLI, when the manifest declares an APIExport with resources
  (skipped for pure UI/backend providers);
- the ui-grant route is available on this hub when the manifest declares a UI.

**Iterating on a custom provider against SaaS** is section 3.5.1: an
ephemeral in-process edge, the provider as a local process, nothing installed.

**What the SaaS operator must provide.** None of this works if the platform is
not set up for BYO: a publicly dialable virtual-workspace URL on every shard,
the edges provider enabled, and the ui-grant route deployed. `validate --hub`
reports each of these so a tenant can tell "my provider is wrong" from "this
hub cannot host BYO providers", and the scaffolded README says which hub
settings to ask the operator for.

## 4. Phases

Each phase is one or two PRs and leaves `make e2e-provider` green.

| Phase | Deliverable | Removes |
|---|---|---|
| 0 | Fix stale text: quickstart README "not in this iteration" list, `manifest.yaml` header (`schemas[]`), chart README's `/bonkers` references. Half a day. | P7 |
| 1 | `provider-sdk/runtime` + `ClaimsFromCatalogEntry` + embedded manifest and schemas. Quickstart adopts; `init_cmd.go` and the chart ConfigMap go. | P1, P4 |
| 2 | `faros provider init` with embedded template and `make sync-scaffold`/`verify-scaffold`. CI job scaffolds `acme` into a temp dir, builds it, runs it under the provider e2e suite. `faros provider dev --local`. | P3, P5 |
| 3 | `faros provider register/install/list/status/enable/disable/delete/credentials rotate/validate`, org-scoped by default with edge selection, local instruction rendering and the §3.6 preflight. `dev` with an ephemeral in-process host edge (three small hub changes plus a sweeper, §3.5.1) and `faros provider quickstart`. Admin create returns kubeconfig and instructions; portal `/bonkers` uses it. Docs and `skills/faros` updated; `verify-docs-cli` regenerates the CLI reference. | P2, SaaS gap |
| 4 | `faros-provider` library chart published; quickstart chart shrinks to four files; `helm-build.sh` covers it. Optional: register request accepts an inline `selfHosting` recipe so the portal shows steps for custom providers. | P4 (chart half) |
| 5 | Standalone-repo story: `init` emits a GitHub workflow that builds image and chart on `v*` tags (a generalised `provider-release.yaml`); `faros provider publish` cuts the tag. | P6 |

Phases 1 and 2 are where most of the smoothness comes from and they do not
depend on any hub change. Phase 3 is the one that touches the hub and is the
"helm install" moment, for platform admins and SaaS tenants alike.

## 5. Decisions to make

- **Manifest in the binary vs the chart.** §3.1 puts it in the binary so there
  is exactly one copy. The cost is that changing display metadata means a new
  image. The chart override (`FAROS_CATALOGENTRY_FILE`) stays for anyone who
  needs a faster cadence. Recommended: binary.
- **Template source: embedded copy vs download.** Downloading the mirror tarball
  at a pinned tag avoids a sync step but breaks offline and adds a network
  dependency to `init`. Recommended: embedded copy with the portalkit-style
  verify.
- **Heartbeat for BYO providers.** The SDK will skip it by path detection.
  Whether the hub should instead accept org-scoped heartbeats is a separate
  question in byo-providers.md and is not blocked by this plan.
- **How far the big providers migrate.** None are forced. The runtime hooks
  exist so infrastructure and edges can adopt piecemeal; the library chart is
  opt-in.
- **Default scope for `register` and `install`.** Org-scoped unless
  `--platform`. A SaaS tenant can never take the platform path, and a hub
  admin who wants it says so. Recommended: org-scoped default.
- **Where custom-provider instructions are rendered.** CLI-side from the
  manifest's `selfHosting` block needs no hub change and ships in phase 3.
  Accepting the recipe in the register request additionally lights up the
  portal panel, and can follow in phase 4.
- **Laptop dev against SaaS.** An ephemeral host edge run in-process by the
  CLI, with the provider as a plain local process, needs three small hub
  changes plus a sweeper (§3.6) and gives the change-rerun-reload loop people
  expect with no agent installed. A local kind/k3d cluster works with today's
  hub but iterates through image builds. Recommended: the ephemeral-edge
  path, in phase 3.
- **Persistent or ephemeral edge for `dev`.** Ephemeral by default: it leaves
  nothing on the machine and nothing in the org after Ctrl-C. `--edge` reuses
  an existing edge for people who already run one.

## 6. Out of scope

Provider actions, assistant skills, the delegated-token design for BYO, and
the `--provider-workspace-cluster-admin` flip are unchanged by this plan. The
scaffold emits the fields for them but does not change their semantics.
