## faros edge upgrade

Print upgrade instructions for an edge agent

### Synopsis

Print upgrade instructions for a named edge agent.

The command detects whether the edge is a Kubernetes (Helm) or server (binary)
deployment and prints the appropriate upgrade steps. If the agent is already
running the same version as this CLI binary, it reports that the agent is
up to date.

```
faros edge upgrade <name> [flags]
```

### Options

```
  -h, --help   help for upgrade
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros edge](faros_edge.md)	 - Create, list, inspect and remove edges (clusters and servers)

