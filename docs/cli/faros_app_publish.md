## faros app publish

Set production visibility: public, restricted or private

### Synopsis

Set who can open the production app.

  public      anyone with the URL
  restricted  signed-in users you grant (the default after the first promote)
  private     unpublish and drop every grant

```
faros app publish <name> [flags]
```

### Options

```
  -h, --help            help for publish
      --mode string     public, restricted or private (required)
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

* [faros app](faros_app.md)	 - Manage App Studio projects: list, create, status, sync, promote, publish

