## faros workspace members add

Add a member to the workspace (admin only)

### Synopsis

Add an existing user to the workspace by email, user id or RBAC identity.
With --invite an unknown email pre-provisions a pending account that the
first sign-in with that email adopts, so access is ready before they arrive.

```
faros workspace members add <user> [flags]
```

### Options

```
  -h, --help          help for add
      --invite        Pre-provision an account for an email that has not signed in yet
      --role string   Role to grant: admin or member (default "member")
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

