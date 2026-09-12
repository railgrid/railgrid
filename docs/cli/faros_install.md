## faros install

Install the faros agent

### Synopsis

Install the faros agent on the current host (--type server) or on the
Kubernetes cluster addressed by the current kubeconfig context (--type kubernetes).

Examples:

  # Install as a systemd service on this server:
  faros install --type server \
    --hub-url https://faros.example.com \
    --edge-name my-edge \
    --token <join-token>

  # Generate and apply a Kubernetes Deployment for this cluster:
  faros install --type kubernetes \
    --hub-url https://faros.example.com \
    --edge-name my-edge \
    --token <join-token>

```
faros install [flags]
```

### Options

```
      --cluster string         kcp logical cluster name for host agents
      --dry-run                Print what would be done without applying it
      --edge-name string       Name of this edge
  -h, --help                   help for install
      --hub-url string         Hub server URL
      --kubeconfig string      Path to kubeconfig for --type=kubernetes (default: $KUBECONFIG or ~/.kube/config)
      --launchd-plist string   LaunchDaemon plist path (default: /Library/LaunchDaemons/com.faros.agent.<edge>.plist)
      --token string           Bootstrap join token
      --type string            Installation type: 'server' (systemd), 'macos' (launchd), or 'kubernetes' (kubectl apply) (default "kubernetes")
      --worker-user string     Existing non-root account for a macOS LaunchDaemon
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams

