## faros sandbox logs

Print the dev process log

```
faros sandbox logs <instance> <component> [flags]
```

### Options

```
  -f, --follow   Keep polling and print new output (Ctrl-C to stop)
  -h, --help     help for logs
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --org string                 Organization display name or UUID (default: the org that owns the kubeconfig's workspace)
      --workspace string           Workspace display name or UUID (default: the workspace the kubeconfig points at)
```

### SEE ALSO

* [faros sandbox](faros_sandbox.md)	 - Drive a development-mode instance: sync, exec, logs, restart, status

