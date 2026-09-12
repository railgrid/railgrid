## faros workspace members set-role

Change a member's role in the workspace (admin only)

```
faros workspace members set-role <user> <admin|member> [flags]
```

### Options

```
  -h, --help   help for set-role
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --org string                 Organization display name or UUID (default: the org that owns the kubeconfig's workspace)
      --workspace string           Workspace display name or UUID (default: the workspace the kubeconfig points at)
```

### SEE ALSO

* [faros workspace members](faros_workspace_members.md)	 - List and change who has access to the workspace

