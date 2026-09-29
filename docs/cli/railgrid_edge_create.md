## railgrid edge create

Create an edge and print its join command

```
railgrid edge create <name> [flags]
```

### Options

```
      --harness string          Coding harnesses this machine offers, written to spec.harness on the edge: "auto" (default: every harness installed on the machine), "none", or a comma-separated list of claude,codex. Unlike the agent's --harness this STICKS: the agent applies it live and no agent flag can override it. Change it any time from the edge UI or with kubectl. (default "auto")
  -h, --help                    help for create
      --labels stringToString   Labels for this edge (key=value pairs) (default [])
      --type string             Edge type: kubernetes (Kubernetes), server (Linux host: SSH, host services, runner) or macos (MacOS host: host services, runner) (default "kubernetes")
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid edge](railgrid_edge.md)	 - Create, list, inspect and remove edges (clusters and servers)

