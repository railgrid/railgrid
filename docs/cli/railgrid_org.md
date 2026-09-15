## railgrid org

Organizations you belong to, and who is in them

### Synopsis

An organization owns workspaces and members. You get a personal
organization on first login; teams create shared ones.

  railgrid org list                      # your organizations and your role in each
  railgrid org members                   # members of the current organization
  railgrid org members --org acme        # …of another one you belong to
  railgrid org create "Acme"

```
railgrid org [flags]
```

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

* [railgrid](railgrid.md)	 - railgrid: an open-source control plane for platform teams
* [railgrid org create](railgrid_org_create.md)	 - Create an organization (you become its admin)
* [railgrid org list](railgrid_org_list.md)	 - List the organizations you belong to
* [railgrid org members](railgrid_org_members.md)	 - List and change who has access to the organization

