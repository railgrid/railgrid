## railgrid app preview

Show or set who can open the development preview (Dev URL)

### Synopsis

Read or change the development preview's access mode.

  restricted  signed-in workspace members and people you grant (the default)
  public      anyone with the Dev URL, no sign-in
  private     back to restricted and drop every preview grant

Without --mode the current mode is printed. The preview is the live
development sandbox, not a built image: good for a demo, not a deployment.

```
railgrid app preview <name> [flags]
```

### Options

```
  -h, --help            help for preview
      --mode string     public, restricted or private; omit to show the current mode
  -o, --output string   Output format: json
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --org string                 Organization display name or UUID (default: the org that owns the kubeconfig's workspace)
      --workspace string           Workspace display name or UUID (default: the workspace the kubeconfig points at)
```

### SEE ALSO

* [railgrid app](railgrid_app.md)	 - Manage App Studio projects: list, create, status, sync, promote, publish

