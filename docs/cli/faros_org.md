## faros org

Organizations you belong to, and who is in them

### Synopsis

An organization owns workspaces and members. You get a personal
organization on first login; teams create shared ones.

  faros org list                      # your organizations and your role in each
  faros org members                   # members of the current organization
  faros org members --org acme        # …of another one you belong to
  faros org create "Acme"

### Options

```
  -h, --help         help for org
      --org string   Organization display name or UUID (default: the one owning the current workspace)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams
* [faros org create](faros_org_create.md)	 - Create an organization (you become its admin)
* [faros org list](faros_org_list.md)	 - List the organizations you belong to
* [faros org members](faros_org_members.md)	 - List and change who has access to the organization

