## railgrid completion fish

Generate the autocompletion script for fish

### Synopsis

Generate the autocompletion script for the fish shell.

To load completions in your current shell session:

	railgrid completion fish | source

To load completions for every new session, execute once:

	railgrid completion fish > ~/.config/fish/completions/railgrid.fish

You will need to start a new shell for this setup to take effect.


```
railgrid completion fish [flags]
```

### Options

```
  -h, --help              help for fish
      --no-descriptions   disable completion descriptions
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid completion](railgrid_completion.md)	 - Generate the autocompletion script for the specified shell

