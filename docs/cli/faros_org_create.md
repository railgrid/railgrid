## faros org create

Create an organization (you become its admin)

```
faros org create <display-name> [flags]
```

### Options

```
      --catalog-entry-creation string   Who may publish catalog entries: members or admin (default)
  -h, --help                            help for create
      --workspace-creation string       Who may create workspaces: members (default) or admin
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --org string                 Organization display name or UUID (default: the one owning the current workspace)
```

### SEE ALSO

* [faros org](faros_org.md)	 - Organizations you belong to, and who is in them

