## railgrid

railgrid: an open-source control plane for platform teams

### Synopsis

railgrid connects Kubernetes clusters and Linux servers behind NAT to one
hub, and gives every team an isolated workspace with its own APIs, RBAC and
providers on top.

Typical session:

  railgrid login --hub-url https://hub.example.com   # OIDC in the browser
  railgrid use                                        # pick an org and workspace
  railgrid edge list                                  # what is connected
  railgrid connect my-cluster                         # point kubectl at an edge
  railgrid ssh my-server                              # shell on a Linux edge
  railgrid whoami                                     # where am I, what can I do

Every command talks to the hub as you, with your workspace RBAC. Run
'railgrid <command> --help' for details and 'railgrid completion --help' for shell
completion.

### Options

```
  -h, --help                       help for railgrid
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid agent](railgrid_agent.md)	 - Run, install or upgrade the edge agent on a cluster or server
* [railgrid app](railgrid_app.md)	 - Manage App Studio projects: list, create, status, sync, promote, publish
* [railgrid commit](railgrid_commit.md)	 - Record local git commits through railgrid (code__commit_files)
* [railgrid completion](railgrid_completion.md)	 - Generate the autocompletion script for the specified shell
* [railgrid connect](railgrid_connect.md)	 - Point kubectl at a Kubernetes edge
* [railgrid dev](railgrid_dev.md)	 - Manage development environment for railgrid
* [railgrid disconnect](railgrid_disconnect.md)	 - Point kubectl back at the hub workspace
* [railgrid edge](railgrid_edge.md)	 - Create, list, inspect and remove edges (clusters and servers)
* [railgrid env](railgrid_env.md)	 - Print shell exports for calling the hub as you
* [railgrid init](railgrid_init.md)	 - Run a railgrid hub in-process (server side, not a client command)
* [railgrid install](railgrid_install.md)	 - Install the railgrid agent
* [railgrid login](railgrid_login.md)	 - Log in to a railgrid hub (browser OIDC flow, or a static token)
* [railgrid logout](railgrid_logout.md)	 - Forget the hub credentials on this machine
* [railgrid mcp](railgrid_mcp.md)	 - MCP endpoints for AI clients (Claude Code, Cursor, Codex)
* [railgrid org](railgrid_org.md)	 - Organizations you belong to, and who is in them
* [railgrid sandbox](railgrid_sandbox.md)	 - Drive a development-mode instance: sync, exec, logs, restart, status
* [railgrid skills](railgrid_skills.md)	 - Install agent skills from the railgrid repository into Claude Code and Codex
* [railgrid ssh](railgrid_ssh.md)	 - Open an SSH session to a Linux server edge via the hub
* [railgrid token](railgrid_token.md)	 - Print a bearer token for the hub (refreshing it when needed)
* [railgrid use](railgrid_use.md)	 - Switch the active organization and workspace
* [railgrid version](railgrid_version.md)	 - Print version information
* [railgrid whoami](railgrid_whoami.md)	 - Show who you are logged in as, and where kubectl points
* [railgrid workspace](railgrid_workspace.md)	 - Workspaces of an organization, and who is in them

