## faros mcp proxy

Serve the workspace MCP endpoint over stdio, authenticated as you

### Synopsis

Serves the workspace's aggregate MCP endpoint to an MCP client over stdio.

Every message the client writes is forwarded to the hub with your own faros
login — the same credentials kubectl uses, OIDC tokens refreshed as they
expire — and the hub's replies are written back. Nothing needs to be pasted
into the client's configuration, and the hub's CA from your kubeconfig is
trusted, so a local hub needs no extra certificate settings.

Because the calls are made as you rather than with the workspace's MCP
ServiceAccount token ('faros mcp url'), org-owned providers — for example
an organization's self-hosted infrastructure — are federated too.

The workspace is the one the faros context points at when the client starts
the proxy; after 'faros use', restart the client's MCP connection. Pass
--org / --workspace to pin one instead.

```
faros mcp proxy [flags]
```

### Examples

```
  # Claude Code
  claude mcp add faros -- faros mcp proxy

  # Codex
  codex mcp add faros -- faros mcp proxy

  # Claude Desktop, Cursor and other mcpServers configurations
  { "mcpServers": { "faros": { "command": "faros", "args": ["mcp", "proxy"] } } }
```

### Options

```
  -h, --help                    help for proxy
      --mcpserver-name string   Name of the aggregate MCPServer to serve (default "default")
      --org string              Organization display name or UUID (default: the org that owns the kubeconfig's workspace)
      --workspace string        Workspace display name or UUID (default: the workspace the kubeconfig points at)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros mcp](faros_mcp.md)	 - MCP endpoints for AI clients (Claude Code, Cursor, Codex)

