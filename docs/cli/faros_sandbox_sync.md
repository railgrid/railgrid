## faros sandbox sync

Push a directory into the component workspace (authoritative)

### Synopsis

Push every non-ignored file under dir (default ".") into the component
workspace. Inside a git repository the file list is 'git ls-files -co
--exclude-standard'; elsewhere every file minus node_modules/, dist/ and .git/.

The sync is authoritative: it carries a source revision and a digest over the
whole file set, replaces the managed file set, and is what exec verifies
against. Binary files (not UTF-8, or containing NUL) are sent base64-encoded
when the component's dev agent advertises base64 sync (at most 25 MiB per
binary file, 48 MiB per sync); against an older agent they are skipped with a
warning.

For an App Studio project's <project>-dev instance, 'faros app sync <project>'
pushes App Studio's own file set instead; a sandbox sync replaces that set.

```
faros sandbox sync <instance> <component> [dir] [flags]
```

### Options

```
  -h, --help             help for sync
  -o, --output string    Output format: json
      --restart string   Restart policy after sync: auto or always (default "auto")
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

