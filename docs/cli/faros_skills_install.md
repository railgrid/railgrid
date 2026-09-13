## faros skills install

Install skills for Claude Code and Codex (all skills by default)

```
faros skills install [skill...] [flags]
```

### Examples

```
  faros skills install                          # every skill, for Claude Code and Codex, for this user
  faros skills install faros --target claude    # one skill, one client
  faros skills install --scope project          # into ./.claude/skills and ./.agents/skills
  faros skills install --dir ~/.cursor/skills   # any other directory
  faros skills install --ref v0.1.30            # pin to a tag
```

### Options

```
      --dir string      Install into this directory instead of the client locations (one <dir>/<skill> per skill)
      --force           Replace directories that were not installed by faros skills
  -h, --help            help for install
  -o, --output string   Output format: json, yaml
      --ref string      Branch, tag or commit to read (default "main")
      --repo string     GitHub repository (owner/name) whose skills/ directory to read (default "faroshq/faros")
      --scope string    user (home directory) or project (current directory) (default "user")
      --target string   Which client to install for: claude, codex or all (default "all")
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros skills](faros_skills.md)	 - Install agent skills from the faros repository into Claude Code and Codex

