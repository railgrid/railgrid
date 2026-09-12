## faros connect

Point kubectl at a Kubernetes edge

### Synopsis

Add a kubeconfig context named faros-<edge> for the edge's Kubernetes API
(reached through the hub's edge proxy with your hub credentials) and make it
the current context, so plain kubectl talks to that cluster:

  faros connect my-cluster
  kubectl get nodes
  faros disconnect            # back to the hub workspace

Without an argument an interactive picker lists the connected clusters.
The context stays in your kubeconfig; switch between edges with
'kubectl config use-context faros-<edge>' or connect again.

```
faros connect [<edge>] [flags]
```

### Options

```
  -h, --help   help for connect
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams

