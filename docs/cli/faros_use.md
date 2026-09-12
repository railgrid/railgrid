## faros use

Switch the active organization and workspace

### Synopsis

Switch the kubeconfig "faros" context between the organizations and
workspaces you belong to.

With no flags it opens an interactive picker — first an organization, then a
workspace within it. Pass --org and/or --workspace (display name or UUID) to
skip the picker, e.g. for scripts:

  faros use                                  # fully interactive
  faros use --org acme                       # pick a workspace in "acme"
  faros use --org acme --workspace platform  # non-interactive

```
faros use [flags]
```

### Options

```
  -h, --help               help for use
      --org string         Organization display name or UUID (skips the org picker)
      --workspace string   Workspace display name or UUID (skips the workspace picker)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams

