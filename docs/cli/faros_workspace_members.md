## faros workspace members

List and change who has access to the workspace

### Synopsis

Membership is the RBAC unit of a faros workspace: every member is either an
admin (may manage members and settings) or a member. Admins of an
organization can manage every workspace in it.

  faros workspace members                     # who has access, and as what
  faros workspace members add alice@example.com --role member
  faros workspace members add bob@example.com --role admin --invite   # not signed up yet
  faros workspace members set-role alice@example.com admin
  faros workspace members remove bob@example.com

```
faros workspace members [flags]
```

### Options

```
  -h, --help            help for list
  -o, --output string   Output format: json, yaml, name
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --org string                 Organization display name or UUID (default: the org that owns the kubeconfig's workspace)
      --workspace string           Workspace display name or UUID (default: the workspace the kubeconfig points at)
```

### SEE ALSO

* [faros workspace](faros_workspace.md)	 - Workspaces of an organization, and who is in them
* [faros workspace members add](faros_workspace_members_add.md)	 - Add a member to the workspace (admin only)
* [faros workspace members list](faros_workspace_members_list.md)	 - List workspace members
* [faros workspace members remove](faros_workspace_members_remove.md)	 - Remove a member from the workspace (admin only)
* [faros workspace members set-role](faros_workspace_members_set-role.md)	 - Change a member's role in the workspace (admin only)

