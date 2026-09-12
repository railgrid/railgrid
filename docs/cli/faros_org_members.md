## faros org members

List and change who has access to the organization

### Synopsis

Membership is the RBAC unit of a faros organization: every member is either an
admin (may manage members and settings) or a member. Admins of an
organization can manage every workspace in it.

  faros org members                     # who has access, and as what
  faros org members add alice@example.com --role member
  faros org members add bob@example.com --role admin --invite   # not signed up yet
  faros org members set-role alice@example.com admin
  faros org members remove bob@example.com

```
faros org members [flags]
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

* [faros org](faros_org.md)	 - Organizations you belong to, and who is in them
* [faros org members add](faros_org_members_add.md)	 - Add a member to the organization (admin only)
* [faros org members list](faros_org_members_list.md)	 - List organization members
* [faros org members remove](faros_org_members_remove.md)	 - Remove a member from the organization (admin only)
* [faros org members set-role](faros_org_members_set-role.md)	 - Change a member's role in the organization (admin only)

