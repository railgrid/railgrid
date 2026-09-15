## railgrid dev init

Initialize a local railgrid environment (one kind cluster: hub, providers and an edge)

### Synopsis

Initialize a local railgrid environment in a single kind cluster.

This command will:

- Create a hub kind cluster and install the railgrid-hub Helm chart (default:
  OCI chart from ghcr.io) with the static token dev-token, served at
  https://console.127.0.0.1.sslip.io:9443 (public DNS answers every
  *.127.0.0.1.sslip.io name with 127.0.0.1, so no /etc/hosts entry is needed)
- Install the providers named by --providers into that same cluster from
  their published charts (default: edges, infrastructure, code, agents,
  app-studio; quickstart is also supported), onboarding each one on the hub.
  A provider's requirements are added (app-studio needs infrastructure);
  agents and app-studio get their own Postgres; infrastructure runs in
  operator mode and installs kro into the cluster, with an Envoy Gateway
  serving published apps at https://<app>.apps.127.0.0.1.sslip.io:10443;
  code gets GitHub sign-in when GITHUB_OAUTH_CLIENT_ID and
  GITHUB_OAUTH_CLIENT_SECRET are set
- Enable every installed provider in the dev user's default workspace
  (--enable-providers, default on)
- Join the hub kind cluster itself as a KubernetesCluster edge (--with-edge,
  default on): the railgrid-agent runs in the cluster next to the hub, so
  "railgrid edge list" shows a Ready edge right after login
- Create N extra plain worker kind clusters when --worker-count > 0, for
  connecting more edges by hand
- Configure necessary port mappings (9443, 8080, 10443)

The provider and edge automation signs in with the static dev token, so
--with-dex (which disables token login) skips it.

The hub chart can be sourced from:

- OCI registry (default): oci://ghcr.io/railgrid/charts/railgrid-hub
- Local filesystem: --chart-path ./deploy/charts/railgrid-hub
- Custom OCI registry: --chart-path oci://custom.registry/charts/railgrid-hub

Provider charts come from --provider-chart-repo: an OCI base (default
oci://ghcr.io/railgrid/charts, latest published version of each chart) or a
railgrid checkout, which uses providers/<name>/deploy/chart.

```
railgrid dev init [flags]
```

### Examples

```
  # One kind cluster running the hub, the edges, infrastructure, code, agents
  # and App Studio providers, and an agent that joins that same cluster as
  # the edge "local" (default)
  railgrid dev init

  # Same, with GitHub sign-in for the code provider (register the callback
  # https://console.127.0.0.1.sslip.io:9443/services/providers/code/oauth/github/callback
  # on the GitHub OAuth App)
  GITHUB_OAUTH_CLIENT_ID=... GITHUB_OAUTH_CLIENT_SECRET=... railgrid dev init

  # Only edges, plus the quickstart provider
  railgrid dev init --providers edges,quickstart

  # App Studio (pulls in infrastructure, which it requires)
  railgrid dev init --providers app-studio

  # Hub only: no providers, no edge
  railgrid dev init --providers "" --with-edge=false

  # Extra plain worker kind clusters to connect by hand
  railgrid dev init --worker-count 1

  # Use local charts from a railgrid checkout for the hub and the providers
  railgrid dev init --chart-path deploy/charts/railgrid-hub --provider-chart-repo .

  # Pin chart versions
  railgrid dev init --chart-version 0.1.31 --provider-chart-version 0.1.19
```

### Options

```
      --agent-chart-path string           Helm chart path or OCI registry URL for agent (default "oci://ghcr.io/railgrid/charts/railgrid-agent")
      --agent-cluster-name string         Name of the agent cluster in dev mode (default "railgrid-agent")
      --api-server-port int               Kubernetes API server port for hub kind cluster (change if 6443 is already in use) (default 6443)
      --apps-https-port int               Host port published apps are served on, as https://<app>.apps.127.0.0.1.sslip.io:<port> (takes effect when the hub cluster is created) (default 10443)
      --chart-path string                 Helm chart path or OCI registry URL for hub (default "oci://ghcr.io/railgrid/charts/railgrid-hub")
      --chart-version string              Helm chart version (default "0.0.51")
      --dex-http-port int                 Host port for the Dex NodePort mapping (Dex serves HTTPS on this port; default 5554) (default 5554)
      --edge-name string                  Name of the edge created by --with-edge (default "local")
      --enable-providers                  Enable every installed provider in the dev user's default workspace (all declared claims accepted) (default true)
  -h, --help                              help for init
      --hub-cluster-name string           Name of the hub cluster in dev mode (default "railgrid-hub")
      --hub-http-port int                 HTTP port for railgrid hub (change if 8080 is already in use) (default 8080)
      --hub-https-port int                HTTPS port for railgrid hub (change if 9443 is already in use) (default 9443)
      --image string                      railgrid hub image to use in dev mode (default "ghcr.io/railgrid/railgrid-hub")
      --image-pull-policy string          Image pull policy for the hub (use Never when the image is pre-loaded into kind) (default "IfNotPresent")
      --kcp-https-port int                Host port for the kcp front-proxy NodePort mapping (default 7443) (default 7443)
      --kind-network string               kind network to use in dev mode (default "railgrid-dev")
      --provider-chart-repo string        OCI repository the provider charts are pulled from, or the path of a railgrid checkout to use providers/<name>/deploy/chart (default "oci://ghcr.io/railgrid/charts")
      --provider-chart-version string     Provider chart version for OCI charts (default: latest published version of each chart)
      --provider-image-tag string         Provider image tag (default: the chart's appVersion for OCI charts, the latest published release for charts from a checkout)
      --providers strings                 Providers to install into the hub kind cluster (supported: edges, infrastructure, code, agents, app-studio, quickstart). Pass an empty value to install none (default [edges,infrastructure,code,agents,app-studio])
      --tag string                        railgrid hub image tag to use in dev mode
      --wait-for-ready-timeout duration   Timeout for waiting for the cluster to be ready (default 2m0s)
      --with-dex                          Deploy Dex as OIDC identity provider into the hub kind cluster
      --with-edge                         Join the hub kind cluster itself as a KubernetesCluster edge and run the railgrid-agent in it (needs the edges provider) (default true)
      --with-external-kcp                 Deploy kcp via Helm into the hub kind cluster instead of using embedded kcp
      --worker-count int                  Number of worker (agent) kind clusters to create. Default 0 = hub-only (local user). Use 1+ for development/tests; >1 names clusters <agent-cluster-name>-1, -2, …
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid dev](railgrid_dev.md)	 - Manage development environment for railgrid

