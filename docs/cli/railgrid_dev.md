## railgrid dev

Manage development environment for railgrid

### Synopsis

Manage a development environment for railgrid using kind clusters.

This command provides subcommands to initialize, update and delete kind
clusters configured for railgrid.

```
railgrid dev [flags]
```

### Options

```
  -h, --help   help for dev
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid](railgrid.md)	 - railgrid: an open-source control plane for platform teams
* [railgrid dev delete](railgrid_dev_delete.md)	 - Delete development environment
* [railgrid dev init](railgrid_dev_init.md)	 - Initialize a local railgrid environment (one kind cluster: hub, providers and an edge)
* [railgrid dev update](railgrid_dev_update.md)	 - Upgrade the railgrid-hub release on an existing local environment

