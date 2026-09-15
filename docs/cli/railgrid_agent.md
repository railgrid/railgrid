## railgrid agent

Run, install or upgrade the edge agent on a cluster or server

```
railgrid agent [flags]
```

### Options

```
  -h, --help   help for agent
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid](railgrid.md)	 - railgrid: an open-source control plane for platform teams
* [railgrid agent install](railgrid_agent_install.md)	 - Install railgrid agent as a systemd or launchd service
* [railgrid agent join](railgrid_agent_join.md)	 - Persistently join an edge to the hub (installs systemd, launchd, or Kubernetes deployment)
* [railgrid agent run](railgrid_agent_run.md)	 - Run the agent as a foreground process (for containers/dev; use 'join' for persistent install)
* [railgrid agent token](railgrid_agent_token.md)	 - Manage agent tokens
* [railgrid agent uninstall](railgrid_agent_uninstall.md)	 - Uninstall railgrid agent systemd or launchd service
* [railgrid agent upgrade](railgrid_agent_upgrade.md)	 - Upgrade the agent for an edge deployed via 'railgrid agent join'

