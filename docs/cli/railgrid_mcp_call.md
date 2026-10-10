## railgrid mcp call

Call one provider tool on the workspace MCP endpoint, as you

### Synopsis

Invoke a provider tool through the workspace's aggregate MCP endpoint with
your own railgrid login (the same path as 'railgrid mcp proxy'), and print
its result: the structured content when the tool returns one, otherwise the
text. A failing tool exits non-zero with the tool's message.

  railgrid mcp call --list                                         # every tool you can call
  railgrid mcp call infrastructure__list_instances
  railgrid mcp call code__build_status '{"repositoryRef":"shop"}'
  railgrid mcp call infrastructure__dev_sync --args-file sync.json

Arguments are a JSON object, inline or from --args-file ('-' reads stdin).

```
railgrid mcp call <tool> [json-arguments] [flags]
```

### Options

```
      --args-file string        Read the JSON arguments from this file ('-' for stdin)
  -h, --help                    help for call
      --list                    List the tools the endpoint federates for you instead of calling one
      --mcpserver-name string   Aggregate MCP server to call (default "default")
      --org string              Organization display name or UUID (default: the org that owns the kubeconfig's workspace)
      --workspace string        Workspace display name or UUID (default: the workspace the kubeconfig points at)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid mcp](railgrid_mcp.md)	 - MCP endpoints for AI clients (Claude Code, Cursor, Codex)

