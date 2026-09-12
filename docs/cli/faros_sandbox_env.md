## faros sandbox env

Set environment variables on the component's running dev process

### Synopsis

Set environment variables on a development-mode component through the data
plane's env verb. This changes the live process only: a running pod reads its
env at start, so changing the Instance's values.env with kubectl is not seen
until the pod is re-rendered, and 'faros sandbox restart' restarts the process
with the env it already has. Pass --restart to restart right after applying so
the new values take effect; keep the Instance's values.env in sync yourself if
the change must survive a re-render.

Secrets do not belong here: the values travel in the request body and land in
the process environment in clear.

```
faros sandbox env <instance> <component> KEY=value [KEY=value...] [flags]
```

### Options

```
  -h, --help      help for env
      --restart   Restart the component's process after applying
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

