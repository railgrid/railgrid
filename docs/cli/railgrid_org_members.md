## railgrid org members

List and change who has access to the organization

### Synopsis

Membership is the RBAC unit of a railgrid organization: every member is either an
admin (may manage members and settings) or a member. Admins of an
organization can manage every workspace in it.

  railgrid org members                     # who has access, and as what
  railgrid org members add alice@example.com --role member
  railgrid org members add bob@example.com --role admin --invite   # not signed up yet
  railgrid org members set-role alice@example.com admin
  railgrid org members remove bob@example.com

```
railgrid org members [flags]
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
      --org string                 Organization display name or UUID (default: the one owning the current workspace)
```

### SEE ALSO

* [railgrid org](railgrid_org.md)	 - Organizations you belong to, and who is in them
* [railgrid org members add](railgrid_org_members_add.md)	 - Add a member to the organization (admin only)
* [railgrid org members list](railgrid_org_members_list.md)	 - List organization members
* [railgrid org members remove](railgrid_org_members_remove.md)	 - Remove a member from the organization (admin only)
* [railgrid org members set-role](railgrid_org_members_set-role.md)	 - Change a member's role in the organization (admin only)

