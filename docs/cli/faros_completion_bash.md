## faros completion bash

Generate the autocompletion script for bash

### Synopsis

Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:

	source <(faros completion bash)

To load completions for every new session, execute once:

#### Linux:

	faros completion bash > /etc/bash_completion.d/faros

#### macOS:

	faros completion bash > $(brew --prefix)/etc/bash_completion.d/faros

You will need to start a new shell for this setup to take effect.


```
faros completion bash
```

### Options

```
  -h, --help              help for bash
      --no-descriptions   disable completion descriptions
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros completion](faros_completion.md)	 - Generate the autocompletion script for the specified shell

