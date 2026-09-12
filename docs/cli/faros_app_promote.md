## faros app promote

Promote the latest built commit (or --commit) to production

### Synopsis

Create or update the <name>-prod instance from a built, faros-recorded commit.

The hostname prefix is locked after the first production deploy: pass
--hostname-prefix on the first promote, and later either the same value or
nothing. Each promote rolls pods, even for the same commit.

```
faros app promote <name> [flags]
```

### Options

```
      --commit string            Promote this faros-recorded commit instead of the latest
  -h, --help                     help for promote
      --hostname-prefix string   Production hostname prefix (locked after the first promote)
  -o, --output string            Output format: json
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

