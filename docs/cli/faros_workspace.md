## faros workspace

Workspaces of an organization, and who is in them

### Synopsis

A workspace is the Kubernetes-style API you work in: edges, providers and
their resources live there, and access is per workspace.

  faros workspace list                      # workspaces in the current org
  faros workspace list --org acme
  faros workspace members                   # members of the current workspace
  faros workspace members --workspace platform
  faros workspace create "Platform"
  faros use --workspace platform            # make it the kubectl target

### Options

```
  -h, --help               help for workspace
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
* [faros workspace create](faros_workspace_create.md)	 - Create a workspace in an organization
* [faros workspace list](faros_workspace_list.md)	 - List the workspaces of an organization
* [faros workspace members](faros_workspace_members.md)	 - List and change who has access to the workspace

