## railgrid agent uninstall

Uninstall railgrid agent systemd or launchd service

```
railgrid agent uninstall [flags]
```

### Options

```
      --dry-run                Print what would be removed without changing the host
      --edge-name string       Edge name (used to derive unit name)
  -h, --help                   help for uninstall
      --launchd-plist string   LaunchDaemon plist path (default: /Library/LaunchDaemons/com.railgrid.agent.<edge>.plist)
      --type string            Installation type: server or macos (default "server")
      --unit-name string       Systemd unit name (default: railgrid-agent-<edge-name>)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid agent](railgrid_agent.md)	 - Run, install or upgrade the edge agent on a cluster or server

