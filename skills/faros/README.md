# faros skill

An agent skill that teaches Claude Code, Codex, Cursor, or any SKILL.md-aware
assistant how to **use** a faros hub from a laptop or CI: log in, pick a
workspace, wire MCP, ship an App Studio project, deploy with the
infrastructure provider, run hosted agents, and reach edges.

It is a user-facing skill. For developing faros itself, read
[`AGENTS.md`](../../AGENTS.md) at the repo root instead.

## Layout

```
skills/faros/
  SKILL.md                     orientation, rules, playbooks (start here)
  references/access.md         CLI, auth, org/workspace IDs, hub REST, URL grammar, kube REST by cluster
  references/app-studio.md     App Studio CRDs and every REST route
  references/code.md           GitHub connections, repositories, commits, CI status
  references/infrastructure.md templates, instances, URLs, access gate, dev sandboxes
  references/agents.md         agents, runs, channels, schedules, deep research
  references/mcp-and-edges.md  aggregate MCP, MCPServer, tool inventory, edges, kuery
```

The frontmatter uses only `name` and `description`, which is the subset
App Studio's own skill parser accepts, so the same package can later be
published as a provider skill.

## Install

Claude Code (per project; `.claude/` is gitignored in this repo, so link it):

```bash
mkdir -p .claude/skills && ln -s ../../skills/faros .claude/skills/faros
# or globally
mkdir -p ~/.claude/skills && ln -s "$PWD/skills/faros" ~/.claude/skills/faros
```

Codex: reference it from your `AGENTS.md`, or copy the directory into
`~/.codex/skills/faros` if your Codex build loads skills from there.

Cursor and others: point a rule at `skills/faros/SKILL.md`, or paste it into
the project instructions. The references are plain markdown.

Inside App Studio: a project can ship it as
`.agents/skills/faros/SKILL.md`; the references directory travels as
package resources.

## Keeping it honest

Everything in the skill was read from the faros source and docs on
2026-09-09, then corrected against a live hub the same day. Route tables,
CRD fields, and MCP tool names are the parts most likely to drift. When you
change one of those in the repo, update the matching reference file in the
same PR. Section 11 of `SKILL.md` lists claims from older docs that are
already wrong; grow that list rather than letting agents rediscover them.

Two kinds of content have different shelf lives, and the skill now says so
in rule 6. **Shapes and mechanisms** — the URL grammar, what an APIBinding
is, why a digest is required for promotion — age well. **Enumerations** —
which providers exist, which are built in, which MCP tools you have — are
per-hub and per-org and were already wrong once. Prefer teaching an agent
the runtime query (`GET /api/providers`, MCP `tools/list`) over adding
another list it will trust for too long.

Section 9 collects what only shows up when you actually drive a hub:
Cloudflare blocking non-browser HTTP clients, calling MCP tools without an
MCP client, org-scoped providers missing from the aggregate, and the states
that look like failures but are only latency. Add to it whenever a session
loses time to something that was not in the code.
