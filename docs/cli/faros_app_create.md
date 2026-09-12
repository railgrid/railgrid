## faros app create

Create a project (repository, scaffold commit and dev instance)

### Synopsis

Create an App Studio project. One call creates the code Repository, the
GitHub repo, the scaffold commit and the <name>-dev instance.

The name is used as given for the project and its code Repository; it is never
suffixed. If a Repository with that name already exists (often one left behind
by a deleted project, which keeps its repository), the hub answers 409 Conflict:
choose another name. Without a validated Git connection the project starts with
no repository, and one connected later gets a suffixed name, so read the
repository ref from 'faros app status'. With --wait the command returns
once the repository is ready and the scaffold commit has succeeded — the point
from which cloning and 'faros commit' work. Without --template, --prompt lets
App Studio infer the template. --existing-repository adopts a code Repository
you created first (one that names an existing GitHub repo): the project
hydrates from its default branch instead of getting a scaffold, and nothing in
that repository's history is promotable until the first 'faros commit'.

```
faros app create <name> [flags]
```

### Options

```
      --description string           Description
      --display-name string          Display name
      --existing-repository string   Adopt this code Repository instead of creating one
  -h, --help                         help for create
  -o, --output string                Output format: json
      --prompt string                What to build; does not start an assistant turn
      --template string              Development template (e.g. application)
      --timeout duration             How long --wait waits (default 5m0s)
      --wait                         Wait for the repository and the scaffold commit
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
      --org string                 Organization display name or UUID (default: the org that owns the kubeconfig's workspace)
      --workspace string           Workspace display name or UUID (default: the workspace the kubeconfig points at)
```

### SEE ALSO

* [faros app](faros_app.md)	 - Manage App Studio projects: list, create, status, sync, promote, publish

