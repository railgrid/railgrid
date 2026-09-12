## faros org members remove

Remove a member from the organization (admin only)

```
faros org members remove <user> [flags]
```

### Options

```
      --cascade   Also remove the user from every workspace of the organization
  -h, --help      help for remove
  -y, --yes       Do not ask for confirmation
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --org string                 Organization display name or UUID (default: the one owning the current workspace)
```

### SEE ALSO

* [faros org members](faros_org_members.md)	 - List and change who has access to the organization

