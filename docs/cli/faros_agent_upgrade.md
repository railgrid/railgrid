## faros agent upgrade

Upgrade the agent for an edge deployed via 'faros agent join'

### Synopsis

Upgrade the faros agent for a Kubernetes edge that was deployed using
"faros agent join". This patches the agent Deployment in the faros-agent
namespace with the new image tag.

For agents installed via Helm, use "helm upgrade" instead.
For server-type agents, this restarts the systemd service after you update
the binary.

```
faros agent upgrade <edge-name> [flags]
```

### Options

```
  -h, --help         help for upgrade
      --tag string   Image tag to upgrade to (default: CLI version)
      --wait         Wait for the rollout to complete (default true)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros agent](faros_agent.md)	 - Run, install or upgrade the edge agent on a cluster or server

