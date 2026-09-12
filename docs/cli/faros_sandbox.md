## faros sandbox

Drive a development-mode instance: sync, exec, logs, restart, status

### Synopsis

Drive a development-mode infrastructure Instance (for an App Studio project,
<project>-dev) through the hub data plane, as you.

Component paths are relative to the component's workspacePath: for the
application template, sync api/ to component "api" and web/ to "web".
Production instances answer 409.

  faros sandbox sync    shop-dev api ./api
  faros sandbox exec    shop-dev api -- node -e 'console.log(1)'
  faros sandbox logs    shop-dev api -f
  faros sandbox restart shop-dev api
  faros sandbox env     shop-dev api PULSE_URL=https://… --restart
  faros sandbox status  shop-dev [api]

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

* [faros](faros.md)	 - faros: an open-source control plane for platform teams
* [faros sandbox env](faros_sandbox_env.md)	 - Set environment variables on the component's running dev process
* [faros sandbox exec](faros_sandbox_exec.md)	 - Run a command in the component and exit with its exit code
* [faros sandbox logs](faros_sandbox_logs.md)	 - Print the dev process log
* [faros sandbox restart](faros_sandbox_restart.md)	 - Restart the component's dev process
* [faros sandbox status](faros_sandbox_status.md)	 - Show the instance status, or a component's process state
* [faros sandbox sync](faros_sandbox_sync.md)	 - Push a directory into the component workspace (authoritative)

