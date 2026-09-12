## faros app

Manage App Studio projects: list, create, status, sync, promote, publish

### Synopsis

Manage App Studio projects through the App Studio REST API, as you.

  faros app create shop --template application --display-name Shop --wait
  faros app status shop
  faros app sync shop
  faros app promote shop --hostname-prefix shop
  faros app publish shop --mode public

Develop with 'faros sandbox' against <project>-dev and record commits with
'faros commit <repository ref>' (the ref is shown by 'faros app status').

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

* [faros](faros.md)	 - faros: an open-source control plane for platform teams
* [faros app create](faros_app_create.md)	 - Create a project (repository, scaffold commit and dev instance)
* [faros app list](faros_app_list.md)	 - List App Studio projects
* [faros app promote](faros_app_promote.md)	 - Promote the latest built commit (or --commit) to production
* [faros app publish](faros_app_publish.md)	 - Set production visibility: public, restricted or private
* [faros app status](faros_app_status.md)	 - Show a project's repository, commits, promotion and publishing state
* [faros app sync](faros_app_sync.md)	 - Load the repository into the project workspace and sync it to <name>-dev

