## faros agent

Run, install or upgrade the edge agent on a cluster or server

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

* [faros](faros.md)	 - faros: an open-source control plane for platform teams
* [faros agent install](faros_agent_install.md)	 - Install faros agent as a systemd or launchd service
* [faros agent join](faros_agent_join.md)	 - Persistently join an edge to the hub (installs systemd, launchd, or Kubernetes deployment)
* [faros agent run](faros_agent_run.md)	 - Run the agent as a foreground process (for containers/dev; use 'join' for persistent install)
* [faros agent token](faros_agent_token.md)	 - Manage agent tokens
* [faros agent uninstall](faros_agent_uninstall.md)	 - Uninstall faros agent systemd or launchd service
* [faros agent upgrade](faros_agent_upgrade.md)	 - Upgrade the agent for an edge deployed via 'faros agent join'

