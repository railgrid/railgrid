## railgrid completion zsh

Generate the autocompletion script for zsh

### Synopsis

Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:

	source <(railgrid completion zsh)

To load completions for every new session, execute once:

#### Linux:

	railgrid completion zsh > "${fpath[1]}/_railgrid"

#### macOS:

	railgrid completion zsh > $(brew --prefix)/share/zsh/site-functions/_railgrid

You will need to start a new shell for this setup to take effect.


```
railgrid completion zsh [flags]
```

### Options

```
  -h, --help              help for zsh
      --no-descriptions   disable completion descriptions
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid completion](railgrid_completion.md)	 - Generate the autocompletion script for the specified shell

