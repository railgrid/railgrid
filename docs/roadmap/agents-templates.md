# Agent templates: create an agent from a curated starting point

Status: **Phase 1 implemented** (1a–1d, 2026-10-20, one PR); phase 2 (tenant-owned templates) NOT IMPLEMENTED. Written 2026-10-20.

## Problem

Creating an agent today starts from a blank form (`providers/agents/portal/src/views/AgentCreate.vue`):
a name, a backend, a credential, an optional channel, three capability
checkboxes and an empty system prompt. Everything that makes an agent *a code
reviewer* or *a news reporter* — the prompt, the autonomy posture, the tool
families, the background grant, the schedule that actually makes it report —
is left to the user to discover across the Config, Tools and Automation tabs
afterwards. The MCP `create_agent` tool has the same gap: a caller has to
know what a good reviewer prompt and grant look like.

The portal already carries the seed of the idea: `AgentCreate.interactiveFamilies`
exists "so a preset hands over a usable agent instead of one the user still
has to wire up" (`portal/src/types.ts`), and `conn-defs.ts` has `PROVIDER_PRESETS`
for model credentials. There is no equivalent for the agent itself.

## Goal

A user picks a **template** — *Code reviewer*, *News reporter*, *Researcher* —
and **wires in** only what the template cannot know: which model credential,
which channel to post to, which GitHub connection, which machine (for a
harness-backed template). Everything else — prompt, description, autonomy,
tool grants, limits, and any schedule the template needs — is filled in. The
result is an ordinary `Agent` (plus ordinary `Schedule`s); nothing about the
runtime changes.

Templates must be usable from all three front doors that create agents today:
the portal, the MCP `create_agent` tool, and plain `kubectl apply` of the
resulting manifests.

## Non-goals (v1)

- Multi-agent bundles (a lead plus delegates). One template produces one agent;
  fan-out stays the `spawn` family inside that agent.
- A templating language. Substitution is literal `{{name}}` replacement of
  declared parameters, nothing conditional.
- Marketplace, versioning or upgrade of agents already created from a template.
  A template is a starting point; after creation the agent is the user's.
- Tenant- or org-authored templates are **phase 2**, not v1 (see below).

## Design

### 1. The `AgentTemplate` shape

A template is a Kubernetes-style manifest in the `agents.railgrid.ai/v1alpha1`
group. In phase 1 its Go type lives in `providers/agents/templates/types.go`,
**not** under `apis/v1alpha1/`: a type there that embeds `ObjectMeta` is picked
up by `controller-gen crd` and would become a CRD, an `APIResourceSchema` and a
served kind, which is exactly what phase 1 does not ship. The YAML shape is the
future kind's shape, and the type moves under `apis/` when phase 2 serves it.

```yaml
apiVersion: agents.railgrid.ai/v1alpha1
kind: AgentTemplate
metadata:
  name: news-reporter
spec:
  displayName: News reporter
  description: Watches topics you choose and posts a digest to a channel on a schedule.
  category: research              # groups cards in the picker: engineering | research | operations | assistant
  suggestedName: news-reporter    # the picker's default agent name; the user can change it

  # Declared inputs. The picker renders exactly these; the MCP tool accepts
  # exactly these. Literal {{key}} occurrences in agent.systemPrompt,
  # agent.description and schedules[].task are replaced before create.
  parameters:
    - key: topics
      label: Topics to follow
      type: list                  # string | text | list
      required: true
      description: One per line, e.g. "Kubernetes releases", "kcp".
    - key: digestTime
      label: When to post
      type: string
      default: "0 8 * * 1-5"
      description: 5-field cron, evaluated in your workspace timezone.

  # What has to be wired in from the workspace. Each slot maps 1:1 onto an
  # existing Agent spec field; the picker shows a dropdown per slot and the
  # MCP tool takes a name per slot. A slot is never free text.
  wiring:
    - slot: modelCredential       # -> spec.backend.model.credentials.chat   (model backend only)
      required: true
    - slot: channel               # -> spec.channels[primary]
      required: true
      description: Where the digest is posted.
    - slot: connection            # -> spec.tools.*.connections[]
      connectionType: websearch
      required: false
      description: A web search connection; without one the reporter can only fetch URLs you give it.
    # other slot kinds: edge (harness backend), harnessCredential (harness
    # backend), githubConnection (-> spec.backend.harness.githubConnectionRef)

  # The Agent spec minus the wired fields. Copied verbatim into the created
  # Agent; the wiring and parameters are applied on top. Validated at build
  # time against the Agent CRD so a template can never produce an agent the
  # reconciler would mark Validated=False.
  agent:
    autonomy: ask
    systemPrompt: |
      You are a news reporter for this workspace. You follow these topics:
      {{topics}}
      ...
    tools:
      interactive: { families: [core, web] }
      background:  { families: [core, web] }
    limits: { maxToolTurns: 40 }

  # Schedules created alongside the agent, agentRef filled in, names suffixed
  # with the agent name (news-reporter-daily-digest). Optional.
  schedules:
    - name: daily-digest
      type: cron
      schedule: "{{digestTime}}"
      channelRef: primary
      task: |
        Compile today's digest for {{topics}} ...
```

Rules that keep templates honest:

- `spec.agent` is a real `AgentSpec` (same Go type), so there is nothing to
  keep in step: a field added to `Agent` is immediately available to templates.
  The wired fields (`backend.model.credentials`, `backend.harness.edgeRef`,
  `backend.harness.credentialRef`, `backend.harness.githubConnectionRef`,
  `channels`, `tools.*.connections`) **must be empty** in a template; a
  build-time test fails otherwise, because the only legal source of those
  values is the user's workspace.
- A harness-backed template (`agent.backend.type: harness`) must declare the
  `edge` and `harnessCredential` slots and must not carry `tools` or
  `delegates` — the same rule the reconciler enforces on agents
  (`MeaninglessForHarness`).
- Every `{{key}}` in a template must be a declared parameter, and every
  declared parameter must be used; both directions are tested.
- `tools.*.families` must name families the provider actually serves, and
  must include `core`; the existing `UnknownToolFamily` / `MissingCoreFamily`
  validation runs against each template at test time.

### 2. Where templates live — phase 1 is a shipped catalog, not tenant state

The built-in catalog is **content the provider ships**, not durable tenant
state, so in v1 it is not stored in kcp at all. It follows the pattern
`model-catalog.json` already uses:

| Piece | Location | Role |
|---|---|---|
| Source of truth | `providers/agents/templates/*.yaml` | One `AgentTemplate` manifest per file, hand-written, `kubectl apply`-able as-is once phase 2 lands. |
| Go embed | `providers/agents/templates/catalog.go` (`//go:embed *.yaml`) | `templates.Builtin()` parses, validates and returns the catalog; used by MCP and by tests. |
| Portal asset | `portal/public/agent-templates.json`, generated by `internal/gentemplates` | The picker loads it the way `api.ts` loads `/model-catalog.json` (same-origin, no bearer). A test asserts the JSON is byte-identical to a fresh render of the YAML, exactly like `api/model_catalog_asset_test.go`. |
| Makefile | `make gen-agents-templates`, wired into `make codegen-agents-provider` | Regenerates the asset; `make verify-codegen` catches a stale copy. |

Why not a kcp kind projected with `ClusterCachedResource`, the way
`infrastructure` publishes its `Template` catalog? Two reasons, both
revisitable:

1. The thing the catalog needs in phase 2 is **tenant-authored** templates
   ("save this agent as a template"), and kcp cannot serve one resource name
   both as virtual read-only storage and as a tenant-writable CRD. Keeping
   the kcp kind for what tenants own avoids inventing a second kind later.
2. `CachedResource` discovery across shards is a known kcp bug
   (`AGENTS.md` §10); a new feature should not take that dependency for
   content that does not need to be an object.

If a platform operator wants to curate the catalog per deployment, that is a
chart value listing template names to hide, applied by `templates.Builtin()`,
not a second storage path.

### 3. Instantiation

Instantiation is a pure function and exists twice, once per language that
creates agents, with a parity test between them:

- **Go** — `providers/agents/templates.Instantiate(tmpl, Inputs{Name, Parameters, Wiring}) (*Agent, []*Schedule, error)`.
  Used by the MCP tool. Errors are the input problems a caller can fix:
  missing required parameter, unknown slot, wiring a `websearch` slot with a
  `github` connection, a harness slot on a model template.
- **TypeScript** — `portal/src/agent-template.ts` `instantiateTemplate(tmpl, inputs)`
  returning the `AgentCreate` body plus `ScheduleCreate` bodies the existing
  `resources.ts` writers already accept. The portal then creates the agent and
  its schedules through the `portalkit` kube client exactly as it does today —
  no new backend route, in keeping with "there is no `/api/*`".
- **Parity** — `internal/gentemplates` also renders, for every built-in
  template, a fixture of inputs and the expected `Agent` + `Schedule` YAML into
  `portal/src/test/fixtures/agent-templates/`; the Go test and the Vitest test
  both assert their instantiator reproduces the fixture. Drift between the two
  implementations fails the build.

Order of operations on create is Agent first, then schedules; a schedule
create failing after the agent exists surfaces as a partial-success notice
with the schedule's error, the same way a channel conflict is surfaced
today — it is not rolled back, because the agent is useful on its own.

### 4. Portal

Route stays `#/create/agent`. The form gains a first step, **Start from**:

- A card grid: *Blank* plus every template in the catalog, grouped by
  `category`, with `displayName`, `description` and the wiring it needs as
  small chips ("model · channel · web search"). Templates whose backend has no
  prerequisite met in this workspace (a harness template with no joined
  machine) stay visible but say what is missing, pointing at the same
  prerequisite links `AgentCreate` already renders.
- Picking a template prefills name, backend, prompt and capabilities, then
  renders the template's **parameters** and **wiring** slots as the form's
  fields. The backend selector is locked to the template's backend. The system
  prompt stays editable (it is a starting point, and the hint text says so).
- `AgentCreateDraft` grows `template`, `parameters` and `wiring`, so the
  "add a credential and come back" detour restores the template state.
- The guidance panel's summary shows the template name and the schedules that
  will be created.
- `AgentsList`'s empty state ("Create your first agent") offers the three
  starter templates directly.

Design-KB contracts to read first: `docs/design/patterns/` creation and form
entries, and `docs/design/components/` for the card/selection primitive. If a
selectable card grid does not exist in `portalkit-vue`, it is added canonical-
first and synced, not built locally in the agents portal. Gates:
`make verify-design-docs`, `make verify-portalkit`, `make verify-ui-conformance`,
plus a rendered check of the picker at phone and desktop widths in both themes.

### 5. MCP

- `list_agent_templates` — name, displayName, description, category, backend,
  parameters and wiring slots. No arguments.
- `get_agent_template(name)` — the full template including the prompt, so an
  assistant can show the user what they are about to get.
- `create_agent(..., template, parameters, wiring)` — when `template` is set,
  the request is instantiated first and the existing explicit fields
  (`systemPrompt`, `autonomy`, families, …) are applied **on top** as
  overrides, so "create a news reporter but with autonomy auto" is one call.
  `modelCredential`, `channels` and the harness fields are the same request
  fields as today and double as the wiring; `wiring` only adds the slots that
  have no field yet (`connection`). Implemented in `api/agents.go`
  `applyAgentCreate` by calling `templates.Instantiate` before
  `agentFromCreateRequest`.
- The server instructions string in `api/mcp.go` gains one sentence: *to
  create a well-configured agent, start from list_agent_templates.*

### 6. Starter catalog (v1 content)

| Template | Backend | Wiring | Grants | Schedule |
|---|---|---|---|---|
| `code-reviewer` | harness (Claude Code / Codex on an edge) | edge, harness credential, GitHub connection; parameters: `repositories` (list), `reviewFocus` (text, default provided) | none (harness brings its own) | none; reviewed on demand and via a trigger the user adds (a GitHub webhook trigger template is a follow-up) |
| `news-reporter` | model | model credential, channel; optional websearch connection; parameters: `topics` (list), `digestTime` (cron, default weekdays 08:00) | interactive `core,web`; background `core,web` | `daily-digest` cron posting to `primary` |
| `researcher` | model | model credential; optional channel, optional websearch connection; parameters: `domain` (text) | interactive `core,web,spawn,visualization`; background `core,web` | none |
| `workspace-assistant` | model | model credential | interactive `core`; background `core` | none; the agent the Workspace Assistant card expects |

Prompts live in the YAML, are reviewed like code, and say what the agent does
*not* do (a reviewer does not merge; a reporter does not editorialise beyond
its topics). A model-backed `code-reviewer-lite` (github family, no edge) is
a follow-up once the `github` family lands (architecture doc item 9).

### 7. Phase 2 — tenant-owned templates

Once phase 1 is in use:

- `AgentTemplate` becomes a bound kind in the agents APIExport
  (`make codegen-agents-provider`; no verbs, no manifest entry needed). The
  YAML files from phase 1 are already valid instances of it.
- A reconciler sets `Validated` on tenant templates using the same checks the
  build-time test runs on built-ins (unused parameter, wired field set, unknown
  family).
- Portal: **Save as template** on an agent's Config page strips the wired
  fields and parameters nothing, writing an `AgentTemplate` in the workspace;
  the picker lists workspace templates under *Yours* above the built-ins.
- MCP: `save_agent_template(agent, name)`, and `list_agent_templates` returns
  both sources with a `source: builtin | workspace` field.
- Org-curated catalogs (an org admin publishing templates to every workspace)
  are where a `ClusterCachedResource` projection would earn its place; not
  before.

## Implementation phases

| Phase | Scope | Touches | Verification |
|---|---|---|---|
| 1a | `AgentTemplate` Go type, `templates/` package with embed + validation + `Instantiate`, the four starter YAMLs, `internal/gentemplates`, Makefile target | `providers/agents/apis/v1alpha1/types_agenttemplate.go`, `providers/agents/templates/`, `providers/agents/internal/gentemplates/`, `Makefile` | `go test ./templates/...` (every built-in validates and instantiates; wired fields empty; parameters round-trip), asset freshness test |
| 1b | MCP tools + `create_agent` template path | `providers/agents/api/agents.go`, `api/mcp.go`, `api/mcp_config.go`, tests beside `agents_create_test.go` | unit tests; `make e2e-agents-provider` gains "create news-reporter from template via MCP, assert Agent Validated=True and Schedule exists" |
| 1c | Portal picker, parameters, wiring, draft round-trip, empty-state cards | `portal/src/views/AgentCreate.vue`, `agent-create-draft.ts`, new `agent-template.ts`, `AgentsList.vue`, fixtures under `portal/src/test/` | Vitest parity + form tests; design gates; rendered check both themes |
| 1d | Docs | `docs/agents-provider-architecture.md` (new "Templates" section + checklist item), `providers/agents/README.md`, this file's status line | `make verify-provider-contract`, `make verify-design-docs` |
| 2 | Tenant-owned `AgentTemplate` kind, Save as template, picker *Yours* section | codegen, reconciler, portal, MCP | e2e: save in workspace A, not visible in workspace B |

Phases 1a–1c can be three PRs or one; 1a has no UI and is the natural first
PR. Phase 2 is a separate plan line in this document when it starts.

## Open questions

1. Should the `channel` slot accept a connection that already backs another
   agent? Today a Connection may back at most one agent's channels; the picker
   should filter those out rather than let the create fail. Default: filter.
2. Timezone for template cron defaults: the `Schedule.timeZone` field exists
   but the create form has no workspace-level default. Default for v1: leave
   empty (UTC) and show the schedule's next-run in the summary so it is
   visible; a workspace timezone setting is its own feature.
3. Whether `workspace-assistant` belongs in the catalog or should be created
   automatically when the Workspace Assistant card is first enabled. Default:
   catalog only; automatic creation is a separate decision about default spend.
