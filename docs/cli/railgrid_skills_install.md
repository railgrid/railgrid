## railgrid skills install

Install skills for Claude Code and Codex (all skills by default)

```
railgrid skills install [skill...] [flags]
```

### Examples

```
  railgrid skills install                          # every skill, for Claude Code and Codex, for this user
  railgrid skills install railgrid --target claude    # one skill, one client
  railgrid skills install --scope project          # into ./.claude/skills and ./.agents/skills
  railgrid skills install --dir ~/.cursor/skills   # any other directory
  railgrid skills install --ref v0.1.30            # pin to a tag
```

### Options

```
      --dir string      Install into this directory instead of the client locations (one <dir>/<skill> per skill)
      --force           Replace directories that were not installed by railgrid skills
  -h, --help            help for install
  -o, --output string   Output format: json, yaml
      --ref string      Branch, tag or commit to read (default "main")
      --repo string     GitHub repository (owner/name) whose skills/ directory to read (default "railgrid/railgrid")
      --scope string    user (home directory) or project (current directory) (default "user")
      --target string   Which client to install for: claude, codex or all (default "all")
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [railgrid skills](railgrid_skills.md)	 - Install agent skills from the railgrid repository into Claude Code and Codex

