# railgrid skill

An agent skill that teaches Claude Code, Codex, Cursor, or any SKILL.md-aware
assistant how to **use** a railgrid hub from a laptop or CI: log in, pick a
workspace, wire MCP, ship an App Studio project, deploy with the
infrastructure provider, run hosted agents, and reach edges.

It is a user-facing skill. For developing railgrid itself, read
[`AGENTS.md`](../../AGENTS.md) at the repo root instead.

## Layout

```
skills/railgrid/
  SKILL.md                     orientation, rules, playbooks (start here)
  .claude-plugin/plugin.json   Claude Code plugin: this skill plus the `railgrid mcp proxy` MCP server
  references/cli.md            env, app, commit, sandbox, mcp (proxy, url, claude, codex)
  references/access.md         CLI, auth, org/workspace IDs, hub REST, URL grammar, kube REST by cluster
  references/app-studio.md     App Studio CRDs and every REST route
  references/code.md           GitHub connections, repositories, commits, CI status
  references/infrastructure.md templates, instances, URLs, access gate, dev sandboxes
  references/agents.md         agents, runs, channels, schedules, deep research
  references/mcp-and-edges.md  aggregate MCP, connecting, MCPServer, tool inventory, edges, kuery
  references/troubleshooting.md error strings and measured latencies
```

The frontmatter uses only `name` and `description`, which is the subset
App Studio's own skill parser accepts, so the same package can later be
published as a provider skill.

## Install

The skill is this directory: `SKILL.md` plus `references/`. Keep the two
together; `SKILL.md` links into `references/`. Nothing is hosted anywhere
else: every install path below reads this repository.

### With the railgrid CLI (Claude Code and Codex at once)

```bash
railgrid skills install
```

fetches `skills/` from this repository on GitHub at that moment (always the
current `main`, or `--ref <tag|branch|sha>`) and writes every skill to
`~/.claude/skills/<name>` and `~/.agents/skills/<name>`. Nothing is baked
into the CLI, so an old binary still installs the latest skill. `--target
claude|codex` picks one client, `--scope project` writes to `./.claude/skills`
and `./.agents/skills` instead, `--dir <path>` targets any other directory
(Cursor, a CI image), and `railgrid skills list` shows what is available.
Re-run to update: directories the CLI installed carry a `.railgrid-skill.json`
marker (source, commit, file list) and are replaced; anything else is left
alone unless you pass `--force`. Skills load at session start, so restart
the client afterwards.

### Claude Code

The repository is a plugin marketplace (`.claude-plugin/marketplace.json`)
with one plugin, `railgrid`, whose root is this directory:

```
/plugin marketplace add railgrid/railgrid
/plugin install railgrid@railgrid
```

or from a shell, `claude plugin marketplace add railgrid/railgrid && claude plugin install railgrid@railgrid`.
Update later with `/plugin update railgrid@railgrid`. The plugin also registers
the MCP server `railgrid` (`railgrid mcp proxy`), so the provider tools appear as
soon as the `railgrid` CLI is on `PATH` and logged in; the skill's playbooks use
them where they exist. To load it without the
plugin system, copy the directory to `.claude/skills/railgrid` (one project) or
`~/.claude/skills/railgrid` (every project); `.claude/` is gitignored here, so
inside this repo use a symlink: `ln -s ../../skills/railgrid .claude/skills/railgrid`.

### Codex

Codex reads `.agents/skills/<name>/SKILL.md` at the repository root and
`~/.agents/skills/<name>/SKILL.md` for the user. This repository ships
`.agents/skills/railgrid` as a symlink to this directory, so Codex picks the
skill up as soon as you open the repo. For another project or for every
project:

```bash
git clone --depth 1 https://github.com/railgrid/railgrid /tmp/railgrid
cp -r /tmp/railgrid/skills/railgrid .agents/skills/railgrid      # this project
cp -r /tmp/railgrid/skills/railgrid ~/.agents/skills/railgrid    # all projects
```

Codex's bundled `$skill-installer` can also fetch it; give it
`https://github.com/railgrid/railgrid/tree/main/skills/railgrid`.

### Cursor and others

Point a rule at `skills/railgrid/SKILL.md`, or paste it into the project
instructions. The references are plain markdown.

### Inside App Studio

A project can ship it as `.agents/skills/railgrid/SKILL.md`; the references
directory travels as package resources.

## Keeping it honest

Everything in the skill was read from the railgrid source and docs on
2026-09-09, then corrected against a live hub the same day. The MCP-first
guidance (`railgrid mcp proxy`, `fmcp`) was checked against a local hub on
2026-09-13. Route tables,
CRD fields, and MCP tool names are the parts most likely to drift. When you
change one of those in the repo, update the matching reference file in the
same PR. `references/troubleshooting.md` is the list of error strings and
latencies agents actually hit; grow it rather than letting agents rediscover
them.

Two kinds of content have different shelf lives, and the skill now says so
in rule 6. **Shapes and mechanisms** — the URL grammar, what an APIBinding
is, why a digest is required for promotion — age well. **Enumerations** —
which providers exist, which are built in, which MCP tools you have — are
per-hub and per-org and were already wrong once. Prefer teaching an agent
the runtime query (`GET /api/providers`, MCP `tools/list`) over adding
another list it will trust for too long.

Section 8 of `SKILL.md` and `references/troubleshooting.md` collect what only
shows up when you actually drive a hub:
Cloudflare blocking non-browser HTTP clients, calling MCP tools from a shell
(`fmcp`), org-scoped providers missing for token-based MCP clients, and the
states that look like failures but are only latency. Add to it whenever a session
loses time to something that was not in the code.
