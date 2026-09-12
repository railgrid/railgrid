## faros org members add

Add a member to the organization (admin only)

### Synopsis

Add an existing user to the organization by email, user id or RBAC identity.
With --invite an unknown email pre-provisions a pending account that the
first sign-in with that email adopts, so access is ready before they arrive.

```
faros org members add <user> [flags]
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
      --org string                 Organization display name or UUID (default: the one owning the current workspace)
```

### SEE ALSO

* [faros org members](faros_org_members.md)	 - List and change who has access to the organization

