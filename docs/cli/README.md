# railgrid CLI reference

Generated from the command tree with `make docs-cli`; do not edit by hand.
Every page lists the command's flags, examples and subcommands.

Global flags: `--kubeconfig` (default `$KUBECONFIG`, then `~/.kube/config`) and
`--insecure-skip-tls-verify`. Shell completion: `railgrid completion --help`.

## Getting started

- [railgrid login](railgrid_login.md) — Log in to a railgrid hub (browser OIDC flow, or a static token)
- [railgrid logout](railgrid_logout.md) — Forget the hub credentials on this machine
- [railgrid token](railgrid_token.md) — Print a bearer token for the hub (refreshing it when needed)
- [railgrid use](railgrid_use.md) — Switch the active organization and workspace
- [railgrid whoami](railgrid_whoami.md) — Show who you are logged in as, and where kubectl points

## Edges (clusters and servers)

- [railgrid connect](railgrid_connect.md) — Point kubectl at a Kubernetes edge
- [railgrid disconnect](railgrid_disconnect.md) — Point kubectl back at the hub workspace
- [railgrid edge](railgrid_edge.md) — Create, list, inspect and remove edges (clusters and servers)
  - [railgrid edge create](railgrid_edge_create.md) — Create an edge and print its join command
  - [railgrid edge delete](railgrid_edge_delete.md) — Delete an edge (the agent on it loses hub access)
  - [railgrid edge get](railgrid_edge_get.md) — Show an edge's connection status and details
  - [railgrid edge join-command](railgrid_edge_join-command.md) — Print the agent join command for an edge
  - [railgrid edge kubeconfig](railgrid_edge_kubeconfig.md) — Print or merge a kubeconfig for a Kubernetes edge
  - [railgrid edge list](railgrid_edge_list.md) — List edges
  - [railgrid edge upgrade](railgrid_edge_upgrade.md) — Print upgrade instructions for an edge agent
- [railgrid ssh](railgrid_ssh.md) — Open an SSH session to a Linux server edge via the hub

## Organizations and access

- [railgrid org](railgrid_org.md) — Organizations you belong to, and who is in them
  - [railgrid org create](railgrid_org_create.md) — Create an organization (you become its admin)
  - [railgrid org list](railgrid_org_list.md) — List the organizations you belong to
  - [railgrid org members](railgrid_org_members.md) — List and change who has access to the organization
    - [railgrid org members add](railgrid_org_members_add.md) — Add a member to the organization (admin only)
    - [railgrid org members list](railgrid_org_members_list.md) — List organization members
    - [railgrid org members remove](railgrid_org_members_remove.md) — Remove a member from the organization (admin only)
    - [railgrid org members set-role](railgrid_org_members_set-role.md) — Change a member's role in the organization (admin only)
- [railgrid workspace](railgrid_workspace.md) — Workspaces of an organization, and who is in them
  - [railgrid workspace create](railgrid_workspace_create.md) — Create a workspace in an organization
  - [railgrid workspace list](railgrid_workspace_list.md) — List the workspaces of an organization
  - [railgrid workspace members](railgrid_workspace_members.md) — List and change who has access to the workspace
    - [railgrid workspace members add](railgrid_workspace_members_add.md) — Add a member to the workspace (admin only)
    - [railgrid workspace members list](railgrid_workspace_members_list.md) — List workspace members
    - [railgrid workspace members remove](railgrid_workspace_members_remove.md) — Remove a member from the workspace (admin only)
    - [railgrid workspace members set-role](railgrid_workspace_members_set-role.md) — Change a member's role in the workspace (admin only)

## Developer workflow

- [railgrid app](railgrid_app.md) — Manage App Studio projects: list, create, status, sync, promote, publish
  - [railgrid app create](railgrid_app_create.md) — Create a project (repository, scaffold commit and dev instance)
  - [railgrid app list](railgrid_app_list.md) — List App Studio projects
  - [railgrid app promote](railgrid_app_promote.md) — Promote the latest built commit (or --commit) to production
  - [railgrid app publish](railgrid_app_publish.md) — Set production visibility: public, restricted or private
  - [railgrid app status](railgrid_app_status.md) — Show a project's repository, commits, promotion and publishing state
  - [railgrid app sync](railgrid_app_sync.md) — Load the repository into the project workspace and sync it to <name>-dev
- [railgrid commit](railgrid_commit.md) — Record local git commits through railgrid (code__commit_files)
- [railgrid env](railgrid_env.md) — Print shell exports for calling the hub as you
- [railgrid mcp](railgrid_mcp.md) — MCP endpoints for AI clients (Claude Code, Cursor, Codex)
  - [railgrid mcp claude](railgrid_mcp_claude.md) — Add the workspace MCP server to Claude Code
  - [railgrid mcp codex](railgrid_mcp_codex.md) — Add the workspace MCP server to Codex
  - [railgrid mcp proxy](railgrid_mcp_proxy.md) — Serve the workspace MCP endpoint over stdio, authenticated as you
  - [railgrid mcp url](railgrid_mcp_url.md) — Print the MCP endpoint URL
- [railgrid sandbox](railgrid_sandbox.md) — Drive a development-mode instance: sync, exec, logs, restart, status
  - [railgrid sandbox env](railgrid_sandbox_env.md) — Set environment variables on the component's running dev process
  - [railgrid sandbox exec](railgrid_sandbox_exec.md) — Run a command in the component and exit with its exit code
  - [railgrid sandbox logs](railgrid_sandbox_logs.md) — Print the dev process log
  - [railgrid sandbox restart](railgrid_sandbox_restart.md) — Restart the component's dev process
  - [railgrid sandbox status](railgrid_sandbox_status.md) — Show the instance status, or a component's process state
  - [railgrid sandbox sync](railgrid_sandbox_sync.md) — Push a directory into the component workspace (authoritative)
- [railgrid skills](railgrid_skills.md) — Install agent skills from the railgrid repository into Claude Code and Codex
  - [railgrid skills install](railgrid_skills_install.md) — Install skills for Claude Code and Codex (all skills by default)
  - [railgrid skills list](railgrid_skills_list.md) — List the skills available in the repository

## Agents, hub and local development

- [railgrid agent](railgrid_agent.md) — Run, install or upgrade the edge agent on a cluster or server
  - [railgrid agent install](railgrid_agent_install.md) — Install railgrid agent as a systemd or launchd service
  - [railgrid agent join](railgrid_agent_join.md) — Persistently join an edge to the hub (installs systemd, launchd, or Kubernetes deployment)
  - [railgrid agent run](railgrid_agent_run.md) — Run the agent as a foreground process (for containers/dev; use 'join' for persistent install)
  - [railgrid agent token](railgrid_agent_token.md) — Manage agent tokens
    - [railgrid agent token create](railgrid_agent_token_create.md) — Create a bootstrap token for an edge
  - [railgrid agent uninstall](railgrid_agent_uninstall.md) — Uninstall railgrid agent systemd or launchd service
  - [railgrid agent upgrade](railgrid_agent_upgrade.md) — Upgrade the agent for an edge deployed via 'railgrid agent join'
- [railgrid dev](railgrid_dev.md) — Manage development environment for railgrid
  - [railgrid dev delete](railgrid_dev_delete.md) — Delete development environment
  - [railgrid dev init](railgrid_dev_init.md) — Initialize a local railgrid environment (one kind cluster: hub, providers and an edge)
  - [railgrid dev update](railgrid_dev_update.md) — Upgrade the railgrid-hub release on an existing local environment
- [railgrid init](railgrid_init.md) — Run a railgrid hub in-process (server side, not a client command)
- [railgrid install](railgrid_install.md) — Install the railgrid agent

## Other commands

- [railgrid completion](railgrid_completion.md) — Generate the autocompletion script for the specified shell
  - [railgrid completion bash](railgrid_completion_bash.md) — Generate the autocompletion script for bash
  - [railgrid completion fish](railgrid_completion_fish.md) — Generate the autocompletion script for fish
  - [railgrid completion powershell](railgrid_completion_powershell.md) — Generate the autocompletion script for powershell
  - [railgrid completion zsh](railgrid_completion_zsh.md) — Generate the autocompletion script for zsh
- [railgrid version](railgrid_version.md) — Print version information

