## railgrid app files get

Print a workspace file (text or binary) to stdout or --out

```
railgrid app files get <name> <path> [flags]
```

### Options

```
  -h, --help         help for get
      --out string   Write to this local file instead of stdout
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

