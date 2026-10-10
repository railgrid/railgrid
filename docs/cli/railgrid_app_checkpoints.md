## railgrid app checkpoints

Show each stage on the way to production and why it is not done yet

### Synopsis

List the project's checkpoints (template, git, source, production) with the
state of each and, for anything not done, the reason and the suggested fix.
This is where a blocked promotion explains itself; 'railgrid app status' prints
the same reasons under Blocked:.

```
railgrid app checkpoints <name> [flags]
```

### Options

```
  -h, --help            help for checkpoints
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

