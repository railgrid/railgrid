## faros env

Print shell exports for calling the hub as you

### Synopsis

Resolve every identifier a shell session or AI agent needs to call the hub
REST and MCP APIs as you, and print them as shell exports:

  HUB        hub base URL
  CLUSTER    kcp cluster of the active workspace
  ORG, WS    org and workspace UUIDs (the X-Faros-Org / X-Faros-Workspace headers)
  TOKEN      your bearer token (OIDC tokens expire; re-run to refresh)
  AS         App Studio REST base ($HUB/services/providers/app-studio)
  MCP_URL    the workspace's aggregate MCP endpoint
  MCP_TOKEN  a long-lived token for MCP_URL

Load them with:

  eval "$(faros env)"
  curl -s -H "Authorization: Bearer $TOKEN" -H "X-Faros-Org: $ORG" \
    -H "X-Faros-Workspace: $WS" "$AS/api/projects"

```
faros env [flags]
```

### Options

```
  -h, --help               help for env
      --json               Print a JSON object instead of shell exports
      --no-mcp             Skip the MCP connect call (no MCP_URL / MCP_TOKEN)
      --org string         Organization display name or UUID (default: the org that owns the kubeconfig's workspace)
      --workspace string   Workspace display name or UUID (default: the workspace the kubeconfig points at)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams

