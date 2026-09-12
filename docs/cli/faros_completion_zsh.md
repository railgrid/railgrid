## faros completion zsh

Generate the autocompletion script for zsh

### Synopsis

Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:

	source <(faros completion zsh)

To load completions for every new session, execute once:

#### Linux:

	faros completion zsh > "${fpath[1]}/_faros"

#### macOS:

	faros completion zsh > $(brew --prefix)/share/zsh/site-functions/_faros

You will need to start a new shell for this setup to take effect.


```
faros completion zsh [flags]
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

* [faros completion](faros_completion.md)	 - Generate the autocompletion script for the specified shell

