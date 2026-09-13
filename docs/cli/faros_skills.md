## faros skills

Install agent skills from the faros repository into Claude Code and Codex

### Synopsis

Fetch the skills/ directory of github.com/faroshq/faros (or another
repository with the same layout) and install each skill where Claude Code
and Codex discover them:

  Claude Code   ~/.claude/skills/<name>   (--scope user, the default)
                .claude/skills/<name>    (--scope project)
  Codex         ~/.agents/skills/<name>
                .agents/skills/<name>

The skills are read from GitHub at install time, so you always get the
current main branch (or the --ref you name) regardless of the CLI version.
Re-run install to update. Directories that faros installed are replaced;
directories you wrote yourself are left alone unless you pass --force.

Skills load when an agent session starts, so restart Claude Code or Codex
after installing.

### Options

```
  -h, --help   help for skills
```

### Options inherited from parent commands

```
      --insecure-skip-tls-verify   Skip TLS certificate verification when talking to the hub
      --kubeconfig string          Path to the kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)
```

### SEE ALSO

* [faros](faros.md)	 - faros: an open-source control plane for platform teams
* [faros skills install](faros_skills_install.md)	 - Install skills for Claude Code and Codex (all skills by default)
* [faros skills list](faros_skills_list.md)	 - List the skills available in the repository

