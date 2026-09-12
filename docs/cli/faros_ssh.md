## faros ssh

Open an SSH session to a Linux server edge via the hub

### Synopsis

Open an interactive SSH session (or run a single command) on an Edge
that is connected to the hub.

Examples:
  # Interactive session
  faros ssh my-server

  # Run a single command (non-interactive)
  faros ssh my-server -- echo hello


```
faros ssh <name> [-- command [args...]] [flags]
```

### Options

```
  -h, --help   help for ssh
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams

