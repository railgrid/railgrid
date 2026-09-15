## railgrid disconnect

Point kubectl back at the hub workspace

### Synopsis

Make the railgrid hub context current again after 'railgrid connect'. The edge
contexts stay in your kubeconfig for 'kubectl --context railgrid-<edge>'.

```
railgrid disconnect [flags]
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

* [railgrid](railgrid.md)	 - railgrid: an open-source control plane for platform teams

