## railgrid app files rm

Delete a workspace file (the reconciler commits the deletion)

```
railgrid app files rm <name> <path> [flags]
```

### Options

```
  -h, --help   help for rm
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --org string                 Organization display name or UUID (default: the org that owns the kubeconfig's workspace)
      --workspace string           Workspace display name or UUID (default: the workspace the kubeconfig points at)
```

### SEE ALSO

* [railgrid app files](railgrid_app_files.md)	 - List, read, write and delete files in a project's workspace

