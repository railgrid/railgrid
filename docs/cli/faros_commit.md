## faros commit

Record local git commits through faros (code__commit_files)

### Synopsis

Send the commits on HEAD that are not yet on <remote>/<branch> to the
code provider's commit_files tool, so the commit is faros-recorded and App
Studio can build and promote it. Never 'git push' to a faros-managed repo.

Run inside a clone of the repository:

  git add -A && git commit -m "Add cart"     # commit locally, do not push
  faros commit shop                          # <repositoryRef> is the code Repository name

Every file that differs between <remote>/<branch> and HEAD is sent (deletions
as deletePaths), with the local commit subjects as the message (capped at 512
characters). Binary files are sent base64-encoded (at most 25 MiB each, 48 MiB
per commit) when the hub's code provider supports them; otherwise the command
refuses the change. When faros reports Succeeded the command
fetches, checks that <remote>/<branch> now has exactly your HEAD tree, and
resets the local branch onto it, so your clone carries the faros-recorded SHA.
The commit SHA is printed on stdout.

```
faros commit <repositoryRef> [flags]
```

### Options

```
      --branch string      Branch to commit to (default "main")
      --dry-run            Print what would be sent without calling faros
  -h, --help               help for commit
      --org string         Organization display name or UUID (default: the org that owns the kubeconfig's workspace)
      --remote string      Git remote that tracks the faros-managed repository (default "origin")
      --workspace string   Workspace display name or UUID (default: the workspace the kubeconfig points at)
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams

