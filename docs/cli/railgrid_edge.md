## railgrid edge

Create, list, inspect and remove edges (clusters and servers)

### Synopsis

An edge is a Kubernetes cluster or a Linux server that runs the railgrid agent
and dials out to the hub. Once connected, 'railgrid connect' points kubectl at a
cluster edge and 'railgrid ssh' opens a shell on a server edge.

  railgrid edge create my-cluster                  # prints the join command
  railgrid edge create my-vps --type server
  railgrid edge list
  railgrid edge get my-cluster -o yaml
  railgrid edge kubeconfig my-cluster -o ./my-cluster.kubeconfig
  railgrid edge delete my-vps

A cluster and a server may share a name. Commands that work on either kind then
ask for the type as a qualifier: 'railgrid edge get server/minis'.

```
railgrid edge [flags]
```

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

* [railgrid](railgrid.md)	 - railgrid: an open-source control plane for platform teams
* [railgrid edge create](railgrid_edge_create.md)	 - Create an edge and print its join command
* [railgrid edge delete](railgrid_edge_delete.md)	 - Delete an edge (the agent on it loses hub access)
* [railgrid edge get](railgrid_edge_get.md)	 - Show an edge's connection status and details
* [railgrid edge join-command](railgrid_edge_join-command.md)	 - Print the agent join command for an edge
* [railgrid edge kubeconfig](railgrid_edge_kubeconfig.md)	 - Print or merge a kubeconfig for a Kubernetes edge
* [railgrid edge list](railgrid_edge_list.md)	 - List edges
* [railgrid edge upgrade](railgrid_edge_upgrade.md)	 - Print upgrade instructions for an edge agent

