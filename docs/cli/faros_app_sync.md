## faros app sync

Load the repository into the project workspace and sync it to <name>-dev

### Synopsis

Hydrate the project workspace from its repository's default branch, then run
App Studio's authoritative development sync, which pushes the workspace to
every component of the <name>-dev instance. Afterwards 'faros sandbox exec'
works against <name>-dev.

Use this rather than 'faros sandbox sync' on an App Studio dev instance: App
Studio owns that instance's file set, and a sandbox sync replaces it. Files
the sync left out (binaries a component's dev agent cannot take, files over
the size limits) are listed per component.

```
faros app sync <name> [flags]
```

### Options

```
  -h, --help            help for sync
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

