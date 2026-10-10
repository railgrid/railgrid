## railgrid app files put

Write a workspace file from a local file (or stdin); binaries are fine

### Synopsis

Upload one file into the project workspace at <path>. The content comes from
<local-file>, or from stdin when it is omitted or '-'. Limits: 256 KiB for
text, 25 MiB for binary. The reconciler commits the file and syncs it to the
dev sandbox; on the application template only files under web/ and api/
are served.

```
railgrid app files put <name> <path> [local-file] [flags]
```

### Options

```
      --create-only     Fail (412) when the file already exists
  -h, --help            help for put
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

* [railgrid app files](railgrid_app_files.md)	 - List, read, write and delete files in a project's workspace

