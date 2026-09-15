## railgrid workspace

Workspaces of an organization, and who is in them

### Synopsis

A workspace is the Kubernetes-style API you work in: edges, providers and
their resources live there, and access is per workspace.

  railgrid workspace list                      # workspaces in the current org
  railgrid workspace list --org acme
  railgrid workspace members                   # members of the current workspace
  railgrid workspace members --workspace platform
  railgrid workspace create "Platform"
  railgrid use --workspace platform            # make it the kubectl target

```
railgrid workspace [flags]
```

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

* [railgrid](railgrid.md)	 - railgrid: an open-source control plane for platform teams
* [railgrid workspace create](railgrid_workspace_create.md)	 - Create a workspace in an organization
* [railgrid workspace list](railgrid_workspace_list.md)	 - List the workspaces of an organization
* [railgrid workspace members](railgrid_workspace_members.md)	 - List and change who has access to the workspace

