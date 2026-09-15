## railgrid edge kubeconfig

Print or merge a kubeconfig for a Kubernetes edge

### Synopsis

Produce a kubeconfig whose server is the edge's Kubernetes API, reached
through the hub's edge proxy with your own hub credentials (the hub checks
that you may 'proxy' to the edge and forwards requests as you).

By default the kubeconfig is printed; -o writes it to a file. --merge adds a
context named railgrid-<name> to your kubeconfig without switching to it — use
'railgrid connect <name>' to merge and switch in one step.

Examples:
  railgrid edge kubeconfig my-edge > my-edge.kubeconfig
  railgrid edge kubeconfig my-edge -o ~/.kube/my-edge.kubeconfig
  KUBECONFIG=my-edge.kubeconfig kubectl get nodes
  railgrid edge kubeconfig my-edge --merge && kubectl --context railgrid-my-edge get nodes

```
railgrid edge kubeconfig <name> [flags]
```

### Options

```
  -h, --help            help for kubeconfig
      --merge           Merge a railgrid-<name> context into your kubeconfig instead of printing
  -o, --output string   Write the kubeconfig to this file instead of stdout
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid edge](railgrid_edge.md)	 - Create, list, inspect and remove edges (clusters and servers)

