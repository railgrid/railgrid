## faros token

Print a bearer token for the hub (refreshing it when needed)

### Synopsis

Print the bearer token the kubeconfig credentials produce, for curl and
other tools that cannot run the kubectl exec plugin:

  curl -H "Authorization: Bearer $(faros token)" $HUB/api/orgs

With OIDC logins the cached ID token is returned and refreshed when expired.
--refresh forces a refresh now, which is also the quickest way to check that
the refresh-token flow works against your identity provider.

```
faros token [flags]
```

### Options

```
  -h, --help      help for token
      --refresh   Refresh the OIDC token now even if it has not expired
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams

