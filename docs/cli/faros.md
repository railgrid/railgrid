## faros

faros: an open-source control plane for platform teams

### Synopsis

faros connects Kubernetes clusters and Linux servers behind NAT to one
hub, and gives every team an isolated workspace with its own APIs, RBAC and
providers on top.

Typical session:

  faros login --hub-url https://hub.example.com   # OIDC in the browser
  faros use                                        # pick an org and workspace
  faros edge list                                  # what is connected
  faros connect my-cluster                         # point kubectl at an edge
  faros ssh my-server                              # shell on a Linux edge
  faros whoami                                     # where am I, what can I do

Every command talks to the hub as you, with your workspace RBAC. Run
'faros <command> --help' for details and 'faros completion --help' for shell
completion.

### Options

```
  -h, --help                       help for faros
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros agent](faros_agent.md)	 - Run, install or upgrade the edge agent on a cluster or server
* [faros app](faros_app.md)	 - Manage App Studio projects: list, create, status, sync, promote, publish
* [faros commit](faros_commit.md)	 - Record local git commits through faros (code__commit_files)
* [faros completion](faros_completion.md)	 - Generate the autocompletion script for the specified shell
* [faros connect](faros_connect.md)	 - Point kubectl at a Kubernetes edge
* [faros dev](faros_dev.md)	 - Manage development environment for faros
* [faros disconnect](faros_disconnect.md)	 - Point kubectl back at the hub workspace
* [faros edge](faros_edge.md)	 - Create, list, inspect and remove edges (clusters and servers)
* [faros env](faros_env.md)	 - Print shell exports for calling the hub as you
* [faros init](faros_init.md)	 - Run a faros hub in-process (server side, not a client command)
* [faros install](faros_install.md)	 - Install the faros agent
* [faros login](faros_login.md)	 - Log in to a faros hub (browser OIDC flow, or a static token)
* [faros logout](faros_logout.md)	 - Forget the hub credentials on this machine
* [faros mcp](faros_mcp.md)	 - MCP endpoints for AI clients (Claude Code, Cursor, Codex)
* [faros org](faros_org.md)	 - Organizations you belong to, and who is in them
* [faros sandbox](faros_sandbox.md)	 - Drive a development-mode instance: sync, exec, logs, restart, status
* [faros ssh](faros_ssh.md)	 - Open an SSH session to a Linux server edge via the hub
* [faros token](faros_token.md)	 - Print a bearer token for the hub (refreshing it when needed)
* [faros use](faros_use.md)	 - Switch the active organization and workspace
* [faros version](faros_version.md)	 - Print version information
* [faros whoami](faros_whoami.md)	 - Show who you are logged in as, and where kubectl points
* [faros workspace](faros_workspace.md)	 - Workspaces of an organization, and who is in them

