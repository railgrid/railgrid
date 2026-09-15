## railgrid login

Log in to a railgrid hub (browser OIDC flow, or a static token)

### Synopsis

Authenticate against a hub and write a kubeconfig context named "railgrid"
whose credentials refresh automatically (OIDC) or carry the static token.

  railgrid login --hub-url https://hub.example.com        # opens the browser
  railgrid login --hub-url https://hub.example.com -i     # …then pick org/workspace
  railgrid login --hub-url https://hub.example.com --token <token>
  export RAILGRID_HUB_URL=https://hub.example.com          # instead of --hub-url

On a self-signed hub add --insecure-skip-tls-verify. After login, 'railgrid use'
switches organization and workspace and 'railgrid whoami' shows the session.

```
railgrid login [flags]
```

### Options

```
  -h, --help             help for login
      --hub-url string   Hub server URL (or set RAILGRID_HUB_URL)
  -i, --interactive      After login, interactively pick the organization and workspace
      --token string     Static bearer token (skips OIDC browser flow)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid](railgrid.md)	 - railgrid: an open-source control plane for platform teams

