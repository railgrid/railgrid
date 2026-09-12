## faros edge create

Create an edge and print its join command

```
faros edge create <name> [flags]
```

### Options

```
  -h, --help                    help for create
      --labels stringToString   Labels for this edge (key=value pairs) (default [])
      --type string             Edge type: kubernetes, server (Linux host with SSH) or macos (macOS service host) (default "kubernetes")
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros edge](faros_edge.md)	 - Create, list, inspect and remove edges (clusters and servers)

