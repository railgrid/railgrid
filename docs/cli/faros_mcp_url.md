## faros mcp url

Print the MCP endpoint URL

### Synopsis

Prints the MCP endpoint URL derived from the current kubeconfig context.

Use --mcpserver-name to print the aggregate MCPServer endpoint URL — one
endpoint that exposes both kube and linux edges plus a list_targets tool the
AI uses to discover what's reachable.  This is the entry point for
Claude / Cursor / similar MCP clients:
  https://faros.example.com/services/mcpserver/root:faros:user-default/apis/faros.sh/v1alpha1/mcpservers/default/mcp
The configuration hints then carry the workspace's long-lived MCP token from
the hub's connect endpoint, which also works after an OIDC login.

Use --edge to print the per-edge MCP endpoint URL (single Kubernetes edge):
  https://faros.example.com/services/providers/edges/agent/root:faros:user-default/apis/edges.faros.sh/v1alpha1/kubernetesclusters/my-edge/mcp

The previous per-kind MCP endpoints (--name for KubernetesMCP,
--linux-name for LinuxMCP) were removed; their tools now appear on the
MCPServer aggregate via the in-binary ToolFamily registry.

Usage with Claude Desktop (claude_desktop_config.json):
  {
    "mcpServers": {
      "faros": {
        "url": "<output of this command>"
      }
    }
  }


```
faros mcp url [flags]
```

### Options

```
      --edge string             Name of the edge (for per-edge MCP endpoint)
  -h, --help                    help for url
      --mcpserver-name string   Name of the aggregate MCPServer object (kube + linux + list_targets)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros mcp](faros_mcp.md)	 - MCP endpoints for AI clients (Claude Code, Cursor, Codex)

