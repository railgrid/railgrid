## railgrid app

Manage App Studio projects: list, create, status, sync, promote, publish

### Synopsis

Manage App Studio projects through the App Studio REST API, as you.

  railgrid app create shop --template application --display-name Shop --wait
  railgrid app status shop
  railgrid app sync shop
  railgrid app preview shop --mode public      # share the Dev URL before production exists
  railgrid app promote shop --hostname-prefix shop
  railgrid app publish shop --mode public

Develop with 'railgrid sandbox' against <project>-dev and record commits with
'railgrid commit <repository ref>' (the ref is shown by 'railgrid app status').
'railgrid app checkpoints' explains a blocked promotion; 'railgrid app files'
reads and writes the project workspace directly.

```
railgrid app [flags]
```

### Options

```
  -h, --help               help for app
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
* [railgrid app checkpoints](railgrid_app_checkpoints.md)	 - Show each stage on the way to production and why it is not done yet
* [railgrid app create](railgrid_app_create.md)	 - Create a project (repository, scaffold commit and dev instance)
* [railgrid app files](railgrid_app_files.md)	 - List, read, write and delete files in a project's workspace
* [railgrid app list](railgrid_app_list.md)	 - List App Studio projects
* [railgrid app preview](railgrid_app_preview.md)	 - Show or set who can open the development preview (Dev URL)
* [railgrid app promote](railgrid_app_promote.md)	 - Promote the latest built commit (or --commit) to production
* [railgrid app publish](railgrid_app_publish.md)	 - Set production visibility: public, restricted or private
* [railgrid app status](railgrid_app_status.md)	 - Show a project's repository, commits, promotion and publishing state
* [railgrid app sync](railgrid_app_sync.md)	 - Load the repository into the project workspace and sync it to <name>-dev

