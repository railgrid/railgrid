# faros CLI reference

Generated from the command tree with `make docs-cli`; do not edit by hand.
Every page lists the command's flags, examples and subcommands.

Global flags: `--kubeconfig` (default `$KUBECONFIG`, then `~/.kube/config`) and
`--insecure-skip-tls-verify`. Shell completion: `faros completion --help`.

## Getting started

- [faros login](faros_login.md) — Log in to a faros hub (browser OIDC flow, or a static token)
- [faros logout](faros_logout.md) — Forget the hub credentials on this machine
- [faros token](faros_token.md) — Print a bearer token for the hub (refreshing it when needed)
- [faros use](faros_use.md) — Switch the active organization and workspace
- [faros whoami](faros_whoami.md) — Show who you are logged in as, and where kubectl points

## Edges (clusters and servers)

- [faros connect](faros_connect.md) — Point kubectl at a Kubernetes edge
- [faros disconnect](faros_disconnect.md) — Point kubectl back at the hub workspace
- [faros edge](faros_edge.md) — Create, list, inspect and remove edges (clusters and servers)
  - [faros edge create](faros_edge_create.md) — Create an edge and print its join command
  - [faros edge delete](faros_edge_delete.md) — Delete an edge (the agent on it loses hub access)
  - [faros edge get](faros_edge_get.md) — Show an edge's connection status and details
  - [faros edge join-command](faros_edge_join-command.md) — Print the agent join command for an edge
  - [faros edge kubeconfig](faros_edge_kubeconfig.md) — Print or merge a kubeconfig for a Kubernetes edge
  - [faros edge list](faros_edge_list.md) — List edges
  - [faros edge upgrade](faros_edge_upgrade.md) — Print upgrade instructions for an edge agent
- [faros ssh](faros_ssh.md) — Open an SSH session to a Linux server edge via the hub

## Organizations and access

- [faros org](faros_org.md) — Organizations you belong to, and who is in them
  - [faros org create](faros_org_create.md) — Create an organization (you become its admin)
  - [faros org list](faros_org_list.md) — List the organizations you belong to
  - [faros org members](faros_org_members.md) — List and change who has access to the organization
    - [faros org members add](faros_org_members_add.md) — Add a member to the organization (admin only)
    - [faros org members list](faros_org_members_list.md) — List organization members
    - [faros org members remove](faros_org_members_remove.md) — Remove a member from the organization (admin only)
    - [faros org members set-role](faros_org_members_set-role.md) — Change a member's role in the organization (admin only)
- [faros workspace](faros_workspace.md) — Workspaces of an organization, and who is in them
  - [faros workspace create](faros_workspace_create.md) — Create a workspace in an organization
  - [faros workspace list](faros_workspace_list.md) — List the workspaces of an organization
  - [faros workspace members](faros_workspace_members.md) — List and change who has access to the workspace
    - [faros workspace members add](faros_workspace_members_add.md) — Add a member to the workspace (admin only)
    - [faros workspace members list](faros_workspace_members_list.md) — List workspace members
    - [faros workspace members remove](faros_workspace_members_remove.md) — Remove a member from the workspace (admin only)
    - [faros workspace members set-role](faros_workspace_members_set-role.md) — Change a member's role in the workspace (admin only)

## Developer workflow

- [faros app](faros_app.md) — Manage App Studio projects: list, create, status, sync, promote, publish
  - [faros app create](faros_app_create.md) — Create a project (repository, scaffold commit and dev instance)
  - [faros app list](faros_app_list.md) — List App Studio projects
  - [faros app promote](faros_app_promote.md) — Promote the latest built commit (or --commit) to production
  - [faros app publish](faros_app_publish.md) — Set production visibility: public, restricted or private
  - [faros app status](faros_app_status.md) — Show a project's repository, commits, promotion and publishing state
  - [faros app sync](faros_app_sync.md) — Load the repository into the project workspace and sync it to <name>-dev
- [faros commit](faros_commit.md) — Record local git commits through faros (code__commit_files)
- [faros env](faros_env.md) — Print shell exports for calling the hub as you
- [faros mcp](faros_mcp.md) — MCP endpoints for AI clients (Claude Code, Cursor, Codex)
  - [faros mcp url](faros_mcp_url.md) — Print the MCP endpoint URL
- [faros sandbox](faros_sandbox.md) — Drive a development-mode instance: sync, exec, logs, restart, status
  - [faros sandbox env](faros_sandbox_env.md) — Set environment variables on the component's running dev process
  - [faros sandbox exec](faros_sandbox_exec.md) — Run a command in the component and exit with its exit code
  - [faros sandbox logs](faros_sandbox_logs.md) — Print the dev process log
  - [faros sandbox restart](faros_sandbox_restart.md) — Restart the component's dev process
  - [faros sandbox status](faros_sandbox_status.md) — Show the instance status, or a component's process state
  - [faros sandbox sync](faros_sandbox_sync.md) — Push a directory into the component workspace (authoritative)
- [faros skills](faros_skills.md) — Install agent skills from the faros repository into Claude Code and Codex
  - [faros skills install](faros_skills_install.md) — Install skills for Claude Code and Codex (all skills by default)
  - [faros skills list](faros_skills_list.md) — List the skills available in the repository

## Agents, hub and local development

- [faros agent](faros_agent.md) — Run, install or upgrade the edge agent on a cluster or server
  - [faros agent install](faros_agent_install.md) — Install faros agent as a systemd or launchd service
  - [faros agent join](faros_agent_join.md) — Persistently join an edge to the hub (installs systemd, launchd, or Kubernetes deployment)
  - [faros agent run](faros_agent_run.md) — Run the agent as a foreground process (for containers/dev; use 'join' for persistent install)
  - [faros agent token](faros_agent_token.md) — Manage agent tokens
    - [faros agent token create](faros_agent_token_create.md) — Create a bootstrap token for an edge
  - [faros agent uninstall](faros_agent_uninstall.md) — Uninstall faros agent systemd or launchd service
  - [faros agent upgrade](faros_agent_upgrade.md) — Upgrade the agent for an edge deployed via 'faros agent join'
- [faros dev](faros_dev.md) — Manage development environment for faros
  - [faros dev delete](faros_dev_delete.md) — Delete development environment
  - [faros dev init](faros_dev_init.md) — Initialize a local faros environment (hub kind cluster + optional workers)
  - [faros dev update](faros_dev_update.md) — Upgrade the faros-hub release on an existing local environment
- [faros init](faros_init.md) — Run a faros hub in-process (server side, not a client command)
- [faros install](faros_install.md) — Install the faros agent

## Other commands

- [faros completion](faros_completion.md) — Generate the autocompletion script for the specified shell
  - [faros completion bash](faros_completion_bash.md) — Generate the autocompletion script for bash
  - [faros completion fish](faros_completion_fish.md) — Generate the autocompletion script for fish
  - [faros completion powershell](faros_completion_powershell.md) — Generate the autocompletion script for powershell
  - [faros completion zsh](faros_completion_zsh.md) — Generate the autocompletion script for zsh
- [faros version](faros_version.md) — Print version information

