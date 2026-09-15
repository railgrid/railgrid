## railgrid mcp codex

Add the workspace MCP server to Codex

### Synopsis

Registers the workspace's aggregate MCP server with Codex
(codex mcp add --url), reading the workspace's long-lived MCP token from
RAILGRID_MCP_TOKEN. An existing entry with the same name is replaced.

Codex cannot skip certificate verification. For a hub whose certificate is
not publicly trusted, pass --ca-file: the command writes a bundle of your
system roots plus that CA and prints how to start Codex with it
(CODEX_CA_CERTIFICATE, Codex 0.129.0 or later).

```
railgrid mcp codex [flags]
```

### Examples

```
  # Publicly trusted hub
  railgrid mcp codex

  # Local hub from railgrid dev init
  railgrid mcp codex --ca-file railgrid-hub-ca.crt
```

### Options

```
      --ca-file string          PEM CA that signs the hub's certificate, for a hub whose certificate is not publicly trusted (railgrid dev init writes <cluster>-ca.crt)
      --dry-run                 Print the commands instead of running them
  -h, --help                    help for codex
      --mcpserver-name string   Aggregate MCPServer to connect (default "default")
      --name string             Server name in the client's configuration (default: railgrid-<mcpserver-name>)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid mcp](railgrid_mcp.md)	 - MCP endpoints for AI clients (Claude Code, Cursor, Codex)

