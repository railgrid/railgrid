## faros workspace list

List the workspaces of an organization

```
faros workspace list [flags]
```

### Options

```
  -h, --help            help for list
  -o, --output string   Output format: wide, json, yaml, name
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --org string                 Organization display name or UUID (default: the org that owns the kubeconfig's workspace)
      --workspace string           Workspace display name or UUID (default: the workspace the kubeconfig points at)
```

### SEE ALSO

* [faros workspace](faros_workspace.md)	 - Workspaces of an organization, and who is in them

