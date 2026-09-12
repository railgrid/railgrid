## faros edge kubeconfig

Print or merge a kubeconfig for a Kubernetes edge

### Synopsis

Produce a kubeconfig whose server is the edge's Kubernetes API, reached
through the hub's edge proxy with your own hub credentials (the hub checks
that you may 'proxy' to the edge and forwards requests as you).

By default the kubeconfig is printed; -o writes it to a file. --merge adds a
context named faros-<name> to your kubeconfig without switching to it — use
'faros connect <name>' to merge and switch in one step.

Examples:
  faros edge kubeconfig my-edge > my-edge.kubeconfig
  faros edge kubeconfig my-edge -o ~/.kube/my-edge.kubeconfig
  KUBECONFIG=my-edge.kubeconfig kubectl get nodes
  faros edge kubeconfig my-edge --merge && kubectl --context faros-my-edge get nodes

```
faros edge kubeconfig <name> [flags]
```

### Options

```
  -h, --help            help for kubeconfig
      --merge           Merge a faros-<name> context into your kubeconfig instead of printing
  -o, --output string   Write the kubeconfig to this file instead of stdout
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros edge](faros_edge.md)	 - Create, list, inspect and remove edges (clusters and servers)

