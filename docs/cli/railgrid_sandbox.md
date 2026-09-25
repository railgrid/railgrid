## railgrid sandbox

Drive a development-mode instance: sync, exec, logs, restart, status

### Synopsis

Drive a development-mode infrastructure Instance (for an App Studio project,
<project>-dev) through its data-plane verbs, as you. Each verb is a Kubernetes
custom subresource on the Instance, reached through the hub's kcp front door.

Component paths are relative to the component's workspacePath: for the
application template, sync api/ to component "api" and web/ to "web".
Production instances answer 409.

  railgrid sandbox sync    shop-dev api ./api
  railgrid sandbox exec    shop-dev api -- node -e 'console.log(1)'
  railgrid sandbox logs    shop-dev api -f
  railgrid sandbox restart shop-dev api
  railgrid sandbox env     shop-dev api PULSE_URL=https://… --restart
  railgrid sandbox status  shop-dev [api]

```
railgrid sandbox [flags]
```

### Options

```
  -h, --help               help for sandbox
      --org string         Organization display name or UUID (default: the org that owns the kubeconfig's workspace)
      --workspace string   Workspace display name or UUID (default: the workspace the kubeconfig points at)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid](railgrid.md)	 - railgrid: an open-source control plane for platform teams
* [railgrid sandbox env](railgrid_sandbox_env.md)	 - Set environment variables on the component's running dev process
* [railgrid sandbox exec](railgrid_sandbox_exec.md)	 - Run a command in the component and exit with its exit code
* [railgrid sandbox logs](railgrid_sandbox_logs.md)	 - Print the dev process log
* [railgrid sandbox restart](railgrid_sandbox_restart.md)	 - Restart the component's dev process
* [railgrid sandbox status](railgrid_sandbox_status.md)	 - Show the instance status, or a component's process state
* [railgrid sandbox sync](railgrid_sandbox_sync.md)	 - Push a directory into the component workspace (authoritative)

