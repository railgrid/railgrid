## railgrid app files

List, read, write and delete files in a project's workspace

### Synopsis

Work with the project workspace App Studio keeps between git and the dev
sandbox. A write marks the path uncommitted: the reconciler commits it and
syncs it to <name>-dev, the same as an assistant edit. Paths are relative to
the repository root (web/public/logo.png on the application template).

  railgrid app files ls shop
  railgrid app files get shop api/server.mjs > server.mjs
  railgrid app files put shop web/public/logo.png ./logo.png
  railgrid app files get shop web/public/old.png --version-only
  railgrid app files rm shop web/public/old.png --expected-version <version>

```
railgrid app files [flags]
```

### Options

```
  -h, --help   help for files
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
* [railgrid app files get](railgrid_app_files_get.md)	 - Print a workspace file (text or binary) to stdout or --out
* [railgrid app files ls](railgrid_app_files_ls.md)	 - List the workspace files with their sizes
* [railgrid app files put](railgrid_app_files_put.md)	 - Write a workspace file from a local file (or stdin); binaries are fine
* [railgrid app files rm](railgrid_app_files_rm.md)	 - Delete a workspace file (the reconciler commits the deletion)

