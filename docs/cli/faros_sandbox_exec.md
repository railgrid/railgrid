## faros sandbox exec

Run a command in the component and exit with its exit code

### Synopsis

Run argv (no shell) against the component's last authoritative sync, print its
stdout and stderr, and exit with its exit code. Run 'faros sandbox sync' first
(for an App Studio <project>-dev instance, 'faros app sync <project>').
The command gets PORT (the component's dev server port, so it can reach the
running app) and FAROS_COMPONENT, but not the app's own environment or
secrets (DATABASE_URL and the like).

```
faros sandbox exec <instance> <component> -- <argv...> [flags]
```

### Options

```
  -h, --help               help for exec
      --timeout duration   Command timeout (at most 120s) (default 2m0s)
      --workdir string     Working directory relative to the component workspace
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

