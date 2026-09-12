## faros agent run

Run the agent as a foreground process (for containers/dev; use 'join' for persistent install)

### Synopsis

Run the faros agent as a blocking foreground process.

The agent connects to the hub, registers this edge, and maintains the reverse
tunnel until interrupted (SIGINT/SIGTERM). Suitable for containers, e2e tests,
and interactive development.

For production use on bare-metal or VM hosts, use "faros agent join" instead,
which installs the agent as a persistent systemd service.

```
faros agent run [flags]
```

### Options

```
      --cluster string                 kcp logical cluster name (e.g. '1tww43gelbj45g0k'); required when using static token auth without a cluster-scoped hub kubeconfig
      --context string                 Kubeconfig context to use
      --debug-addr string              Bind address for the debug HTTP server exposing /healthz and /debug/pprof/* (e.g. "127.0.0.1:6060"). Empty disables the server.
      --edge-name string               Name of this edge
  -h, --help                           help for run
      --hub-context string             Kubeconfig context for hub cluster
      --hub-insecure-skip-tls-verify   Skip TLS certificate verification for the hub connection (insecure, for development only)
      --hub-kubeconfig string          Kubeconfig for hub cluster
      --hub-url string                 Hub server URL
      --kubeconfig string              Path to target cluster kubeconfig
      --labels stringToString          Labels for this edge (default [])
      --ssh-password string            SSH password for password-based authentication (prefer --ssh-private-key for security)
      --ssh-private-key string         Path to SSH private key file for key-based authentication
      --ssh-proxy-port int             Local port of the SSH daemon to proxy connections to (default 22; set to a different port in test environments) (default 22)
      --ssh-user string                SSH username for server-type edges (default: current user)
      --svc-allow-cidr strings         CIDR the Service proxy may dial besides loopback (and cluster DNS in kubernetes mode), e.g. 192.168.1.0/24. Repeatable. Link-local, unspecified and multicast addresses are never allowed. Env: FAROS_AGENT_SVC_ALLOW_CIDR
      --svc-policy string              What the Service proxy does with a target outside loopback/--svc-allow-cidr: enforce (403, never dialed), warn (dialed but logged; response carries X-Faros-Svc-Policy: warn) or allow-any (allow list disabled; logged at startup). The default flips to enforce in the next release. Env: FAROS_AGENT_SVC_POLICY (default "warn")
      --token string                   Bootstrap token
      --tunnel-url string              Hub tunnel URL (defaults to hub URL)
      --type string                    Edge type: "kubernetes" (Kubernetes cluster), "server" (Linux host with SSH), or "macos" (macOS service host) (default "kubernetes")
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
```

### SEE ALSO

* [faros agent](faros_agent.md)	 - Run, install or upgrade the edge agent on a cluster or server

