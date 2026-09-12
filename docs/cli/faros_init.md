## faros init

Run a faros hub in-process (server side, not a client command)

```
faros init [flags]
```

### Options

```
      --data-dir string         Data directory for state (default "/tmp/faros-data")
      --external-kcp string     Kubeconfig for external kcp
  -h, --help                    help for init
      --hub-kubeconfig string   Kubeconfig for hub cluster
      --listen-addr string      Address to listen on (default ":9443")
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams

