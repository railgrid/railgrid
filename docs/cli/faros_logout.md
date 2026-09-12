## faros logout

Forget the hub credentials on this machine

### Synopsis

Delete the cached OIDC tokens for the hub and remove the faros kubeconfig
context (and every faros-<edge> context created by 'faros connect'). The
hub-side session is untouched; log in again with 'faros login'.

```
faros logout [flags]
```

### Options

```
  -h, --help                 help for logout
      --keep-edge-contexts   Keep the faros-<edge> contexts (they stop working until you log in again)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams

