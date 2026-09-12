## faros login

Log in to a faros hub (browser OIDC flow, or a static token)

### Synopsis

Authenticate against a hub and write a kubeconfig context named "faros"
whose credentials refresh automatically (OIDC) or carry the static token.

  faros login --hub-url https://hub.example.com        # opens the browser
  faros login --hub-url https://hub.example.com -i     # …then pick org/workspace
  faros login --hub-url https://hub.example.com --token <token>
  export FAROS_HUB_URL=https://hub.example.com          # instead of --hub-url

On a self-signed hub add --insecure-skip-tls-verify. After login, 'faros use'
switches organization and workspace and 'faros whoami' shows the session.

```
faros login [flags]
```

### Options

```
  -h, --help             help for login
      --hub-url string   Hub server URL (or set FAROS_HUB_URL)
  -i, --interactive      After login, interactively pick the organization and workspace
      --token string     Static bearer token (skips OIDC browser flow)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams

