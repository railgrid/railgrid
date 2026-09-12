## faros disconnect

Point kubectl back at the hub workspace

### Synopsis

Make the faros hub context current again after 'faros connect'. The edge
contexts stay in your kubeconfig for 'kubectl --context faros-<edge>'.

```
faros disconnect [flags]
```

### Options

```
  -h, --help   help for disconnect
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams

