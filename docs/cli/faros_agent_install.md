## faros agent install

Install faros agent as a systemd or launchd service

### Synopsis

Install the faros agent as a systemd service on Linux or a launchd
service on macOS.

This creates a systemd unit file, reloads the daemon, enables and starts the
service. The systemd unit runs "faros agent run" so you get both the agent
and the full faros CLI on the server.

Requires root privileges.

Example:
  sudo faros agent install \
    --hub-kubeconfig /etc/faros/hub.kubeconfig \
    --edge-name my-server \
    --type server

```
faros agent install [flags]
```

### Options

```
      --cluster string                 kcp logical cluster path
      --dry-run                        Print the macOS LaunchDaemon and skip installation (works on Linux)
      --edge-name string               Name of this edge (required)
  -h, --help                           help for install
      --hub-insecure-skip-tls-verify   Skip TLS verification
      --hub-kubeconfig string          Path to hub kubeconfig file (required except macOS token bootstrap)
      --hub-url string                 Hub server URL (required with --token for macOS)
      --launchd-plist string           LaunchDaemon plist path (default: /Library/LaunchDaemons/com.faros.agent.<edge>.plist)
      --ssh-private-key string         Path to SSH private key file
      --ssh-proxy-port int             Local SSH daemon port (default 22)
      --ssh-user string                SSH username
      --svc-allow-cidr strings         CIDR the Service proxy may dial besides loopback, e.g. 192.168.1.0/24 (repeatable; rendered into the unit)
      --svc-policy string              Service proxy policy for targets outside the allowed set: enforce, warn or allow-any (rendered into the unit only when not the default) (default "warn")
      --token string                   Bootstrap join token (macOS token bootstrap)
      --type string                    Edge type: kubernetes, server, or macos (default "server")
      --unit-name string               Systemd unit name (default: faros-agent-<edge-name>)
      --worker-user string             Existing non-root account for a macOS LaunchDaemon
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros agent](faros_agent.md)	 - Run, install or upgrade the edge agent on a cluster or server

