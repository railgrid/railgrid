## faros edge

Create, list, inspect and remove edges (clusters and servers)

### Synopsis

An edge is a Kubernetes cluster or a Linux server that runs the faros agent
and dials out to the hub. Once connected, 'faros connect' points kubectl at a
cluster edge and 'faros ssh' opens a shell on a server edge.

  faros edge create my-cluster                  # prints the join command
  faros edge create my-vps --type server
  faros edge list
  faros edge get my-cluster -o yaml
  faros edge kubeconfig my-cluster -o ./my-cluster.kubeconfig
  faros edge delete my-vps

### Options

```
  -h, --help   help for edge
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams
* [faros edge create](faros_edge_create.md)	 - Create an edge and print its join command
* [faros edge delete](faros_edge_delete.md)	 - Delete an edge (the agent on it loses hub access)
* [faros edge get](faros_edge_get.md)	 - Show an edge's connection status and details
* [faros edge join-command](faros_edge_join-command.md)	 - Print the agent join command for an edge
* [faros edge kubeconfig](faros_edge_kubeconfig.md)	 - Print or merge a kubeconfig for a Kubernetes edge
* [faros edge list](faros_edge_list.md)	 - List edges
* [faros edge upgrade](faros_edge_upgrade.md)	 - Print upgrade instructions for an edge agent

