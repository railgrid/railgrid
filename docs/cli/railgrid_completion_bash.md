## railgrid completion bash

Generate the autocompletion script for bash

### Synopsis

Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:

	source <(railgrid completion bash)

To load completions for every new session, execute once:

#### Linux:

	railgrid completion bash > /etc/bash_completion.d/railgrid

#### macOS:

	railgrid completion bash > $(brew --prefix)/etc/bash_completion.d/railgrid

You will need to start a new shell for this setup to take effect.


```
railgrid completion bash
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

* [railgrid completion](railgrid_completion.md)	 - Generate the autocompletion script for the specified shell

