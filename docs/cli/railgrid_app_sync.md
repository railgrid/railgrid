## railgrid app sync

Load the repository into the project workspace and sync it to <name>-dev

### Synopsis

Hydrate the project workspace from its repository's default branch, then run
App Studio's authoritative development sync, which pushes the workspace to
every component of the <name>-dev instance. Afterwards 'railgrid sandbox exec'
works against <name>-dev.

Use this rather than 'railgrid sandbox sync' on an App Studio dev instance: App
Studio owns that instance's file set, and a sandbox sync replaces it. Files
the sync left out (binaries a component's dev agent cannot take, files over
the size limits) are listed per component.

With --from <dir> the local files under <dir> (a clone, or any tree laid out
like the repository) are pushed straight into <name>-dev instead: an additive
sync that leaves the workspace store and git alone, so nothing is committed
and nothing is replaced. Use it to try a change before 'railgrid commit', or
when loading the workspace from git is not working.

```
railgrid app sync <name> [flags]
```

### Options

```
      --from string     Push the files under this local directory into <name>-dev (additive; skips git and the workspace store)
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

* [railgrid app](railgrid_app.md)	 - Manage App Studio projects: list, create, status, sync, promote, publish

