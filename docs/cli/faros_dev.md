## faros dev

Manage development environment for faros

### Synopsis

Manage a development environment for faros using kind clusters.

This command provides subcommands to initialize, update and delete kind
clusters configured for faros.

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

* [faros](faros.md)	 - faros: an open-source control plane for platform teams
* [faros dev delete](faros_dev_delete.md)	 - Delete development environment
* [faros dev init](faros_dev_init.md)	 - Initialize a local faros environment (hub kind cluster + optional workers)
* [faros dev update](faros_dev_update.md)	 - Upgrade the faros-hub release on an existing local environment

