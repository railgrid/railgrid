## faros whoami

Show who you are logged in as, and where kubectl points

### Synopsis

Print the hub, your identity and token state, the active organization
and workspace with your role in each, and which cluster the current kubectl
context targets (the hub workspace or a connected edge).

```
faros whoami [flags]
```

### Options

```
  -h, --help            help for whoami
  -o, --output string   Output format: json, yaml
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams

