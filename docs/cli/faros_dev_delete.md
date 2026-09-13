## faros dev delete

Delete development environment

### Synopsis

Delete the development environment for faros.

This command will delete the kind cluster created for faros development.

```
faros dev delete [flags]
```

### Options

```
      --agent-chart-path string           Helm chart path or OCI registry URL for agent (default "oci://ghcr.io/faroshq/charts/faros-agent")
      --agent-cluster-name string         Name of the agent cluster in dev mode (default "faros-agent")
      --api-server-port int               Kubernetes API server port for hub kind cluster (change if 6443 is already in use) (default 6443)
      --apps-https-port int               Host port published apps are served on, as https://<app>.apps.127.0.0.1.sslip.io:<port> (takes effect when the hub cluster is created) (default 10443)
      --chart-path string                 Helm chart path or OCI registry URL for hub (default "oci://ghcr.io/faroshq/charts/faros-hub")
      --chart-version string              Helm chart version (default "0.0.51")
      --dex-http-port int                 Host port for the Dex NodePort mapping (Dex serves HTTPS on this port; default 5554) (default 5554)
      --edge-name string                  Name of the edge created by --with-edge (default "local")
      --enable-providers                  Enable every installed provider in the dev user's default workspace (all declared claims accepted) (default true)
  -h, --help                              help for delete
      --hub-cluster-name string           Name of the hub cluster in dev mode (default "faros-hub")
      --hub-http-port int                 HTTP port for faros hub (change if 8080 is already in use) (default 8080)
      --hub-https-port int                HTTPS port for faros hub (change if 9443 is already in use) (default 9443)
      --image string                      faros hub image to use in dev mode (default "ghcr.io/faroshq/faros-hub")
      --image-pull-policy string          Image pull policy for the hub (use Never when the image is pre-loaded into kind) (default "IfNotPresent")
      --kcp-https-port int                Host port for the kcp front-proxy NodePort mapping (default 7443) (default 7443)
      --kind-network string               kind network to use in dev mode (default "faros-dev")
      --provider-chart-repo string        OCI repository the provider charts are pulled from, or the path of a faros checkout to use providers/<name>/deploy/chart (default "oci://ghcr.io/faroshq/charts")
      --provider-chart-version string     Provider chart version for OCI charts (default: latest published version of each chart)
      --provider-image-tag string         Provider image tag (default: the chart's appVersion for OCI charts, the latest published release for charts from a checkout)
      --providers strings                 Providers to install into the hub kind cluster (supported: edges, infrastructure, code, agents, app-studio, quickstart). Pass an empty value to install none (default [edges,infrastructure,code,agents,app-studio])
      --tag string                        faros hub image tag to use in dev mode
      --wait-for-ready-timeout duration   Timeout for waiting for the cluster to be ready (default 2m0s)
      --with-dex                          Deploy Dex as OIDC identity provider into the hub kind cluster
      --with-edge                         Join the hub kind cluster itself as a KubernetesCluster edge and run the faros-agent in it (needs the edges provider) (default true)
      --with-external-kcp                 Deploy kcp via Helm into the hub kind cluster instead of using embedded kcp
      --worker-count int                  Number of worker (agent) kind clusters to create. Default 0 = hub-only (local user). Use 1+ for development/tests; >1 names clusters <agent-cluster-name>-1, -2, …
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros dev](faros_dev.md)	 - Manage development environment for faros

