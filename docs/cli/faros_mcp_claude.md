## faros mcp claude

Add the workspace MCP server to Claude Code

### Synopsis

Registers the workspace's aggregate MCP server with Claude Code
(claude mcp add --transport http), authenticated with the workspace's
long-lived MCP token. An existing entry with the same name is replaced.

When the hub's certificate is not publicly trusted, the command prints how
to start Claude Code so it accepts it: with --ca-file, by trusting that CA
(NODE_EXTRA_CA_CERTS); without it, by turning certificate verification off
(NODE_TLS_REJECT_UNAUTHORIZED=0) for that session.

```
faros mcp claude [flags]
```

### Examples

```
  # Publicly trusted hub
  faros mcp claude

  # Local hub from faros dev init
  faros mcp claude --ca-file faros-hub-ca.crt
```

### Options

```
      --ca-file string          PEM CA that signs the hub's certificate, for a hub whose certificate is not publicly trusted (faros dev init writes <cluster>-ca.crt)
      --dry-run                 Print the commands instead of running them
  -h, --help                    help for claude
      --mcpserver-name string   Aggregate MCPServer to connect (default "default")
      --name string             Server name in the client's configuration (default: faros-<mcpserver-name>)
      --scope string            Claude Code configuration scope: user (every project), local (this project, private) or project (.mcp.json) (default "user")
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros mcp](faros_mcp.md)	 - MCP endpoints for AI clients (Claude Code, Cursor, Codex)

