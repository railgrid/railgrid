# App Studio Replit Gap Roadmap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` or
> `superpowers:executing-plans` to implement this plan task-by-task. Keep this
> file updated as phases are accepted or completed. Steps use checkbox syntax for
> tracking.

**Goal:** Identify the principal experience gaps between Kedge App Studio and
Replit's Agent/project-editor experience, then sequence a phased roadmap that
adds the highest-value App Studio capabilities without violating Kedge provider
architecture.

**Architecture:** App Studio remains the tenant-facing orchestration provider.
Repository ownership, git-host interactions, and durable repository status stay
owned by provider-code. Checked-out project files, file inspection, and
workspace mutation stay owned by App Studio. App Studio consumes provider-code
git-source capabilities through tenant scoped APIs and MCP tools, persists
conversation state in its message store, and renders provider-owned artifacts in
the portal.

**Tech Stack:** Go standalone providers, kcp APIExports and CRDs, provider-code
`Repository`, `DeployKey`, and `RepositoryCommit` APIs, App Studio workspace
file store, aggregate MCP, Vue 3 provider portals, Tailwind design tokens,
Postgres-backed App Studio message store.

---

## Source Baseline

### Replit Experience Reviewed

The Replit docs describe a much broader project workspace than App Studio has
today:

- [Replit build overview](https://docs.replit.com/build/welcome): prompt-led app
  creation, imports from existing products, publishing, sharing, and deployment.
- [Your first app](https://docs.replit.com/build/your-first-app): prompt,
  optional plan, chat follow-along, preview, iterate, publish.
- [Prototype an idea](https://docs.replit.com/build/prototype-an-idea): plan
  mode, Canvas annotations, and parallel prototype directions.
- [Agent Skills](https://docs.replit.com/build/use-agent-skills): reusable
  project skills that customize agent behavior.
- [MCP integrations](https://docs.replit.com/build/connect-via-mcp): curated and
  custom external tools exposed to the agent with user approval.
- [Databases](https://docs.replit.com/build/add-database): managed Neon
  Postgres provisioning for development and production.
- [Publishing](https://docs.replit.com/build/publish-your-app): publish flow with
  provisioning, security scan, build, bundle, and promote stages.
- [Design Canvas](https://docs.replit.com/build/design-with-canvas): visual
  design variants, side-by-side comparison, and apply-to-app workflow.
- [Agent overview](https://docs.replit.com/references/agent/overview): planning,
  building, checking work, fixing problems, checkpoints, background tasks, and
  effort modes.
- [Task system](https://docs.replit.com/core-concepts/agent/task-system) and
  [task lifecycle](https://docs.replit.com/references/agent/task-lifecycle):
  isolated background tasks, review, apply, dismiss, and conflict resolution.
- [Project Editor](https://docs.replit.com/learn/projects-and-artifacts/project-editor):
  project editor as the home base, live preview, tools, files, direct edits,
  thread board, task board, and Canvas.
- [App Testing](https://docs.replit.com/references/agent/app-testing): browser
  testing, visual inspection, automatic issue detection, and replay.

### App Studio Experience Today

Local repo evidence:

- `providers/app-studio/README.md` defines App Studio as a persistent AI project
  workspace with tenant LLM credentials, optional MCP tool use, Project CRDs, and
  durable chat transcripts.
- `providers/app-studio/portal/src/types.ts` exposes streaming conversation
  events, tool call events, repository status, and repository commit summaries.
- `providers/app-studio/portal/src/App.vue` already supports immediate entry
  into the builder, streamed status rows, collapsed action bundles, bare
  assistant messages, user timestamps, a provider side pane, and project
  settings.
- `providers/app-studio/api/llm.go` currently teaches the LLM to write generated
  files with `commit_files` and allowlists only that project MCP tool.
- `providers/code/mcpserver/tools_write.go` implements provider-code write tools,
  including `commit_files`.
- `docs/code-provider-architecture.md` frames provider-code MCP as CRD-native:
  MCP tools create or read tenant resources and controllers perform durable side
  effects.
- `docs/mcp-architecture.md` documents Kedge's aggregate MCP route, where
  provider tools surface as provider-prefixed tool names.

## Principal Feature Differences And Gaps

| Area | Replit pattern | App Studio today | Gap |
|---|---|---|---|
| Project start | Immediate builder entry, visible "starting/working" states, optional plan mode before build | Immediate builder entry and streamed status exist | No first-class plan/review/approve loop for complex requests |
| Repository creation | Project workspace is already a live code workspace | Creates a provider-code repository when a project is created | Good foundation, but the repository is mostly opaque to the user and LLM after creation |
| Tool breadth | Agent can read, edit, search, run, test, preview, publish, use MCP, and manage app services | App Studio allowlists only `commit_files` for project MCP actions | LLM cannot inspect existing files, search code, make small edits, delete or rename files, run commands, or test results |
| File mutation model | Fine-grained file edits plus generated changes, with reviewable state | Whole-file bundle commit through `RepositoryCommit` | Large changes work, but incremental edits and safe patch review are missing |
| File workspace UI | File tree, editor, direct file edits, tools, preview | Conversation plus repository/commit metadata | No file tree, code viewer, diff viewer, or direct file edits |
| Runtime and preview | Live app preview, dev URLs, shell/processes, logs | Repository output only | No runnable sandbox, build logs, server lifecycle, or preview URL |
| Testing and repair | Browser testing, visual testing, automatic issue analysis, replay | No automated testing loop | No agent-visible validation or user-visible test artifacts |
| Publish/deploy | Publish dialog and staged deploy pipeline | No App Studio publish flow | No deployment target, URL, environment separation, deployment logs, or rollback |
| Checkpoints | Checkpoints, file history, review/apply/dismiss task results | Git commits are visible through provider-code status | No App Studio checkpoint concept, branch strategy, rollback UI, or diff review |
| Parallel/background tasks | Task board with isolated task copies and conflict handling | Single linear project conversation | No background tasks, isolated branches, task review, or conflict resolution |
| Integrations | Skills, connectors, MCP servers, imports, web search, databases, secrets | Aggregate MCP exists, but App Studio uses a narrow allowlist | No curated App Studio tool catalog, skills, imports, or service provisioning flow |
| Design workflow | Canvas, annotations, visual variants, apply design | Text conversation only | No visual prompt/annotation/variant workflow |
| Data services | Managed database provisioning and app wiring | App Studio itself uses Postgres for messages | Generated apps cannot request databases, secrets, storage, or auth through App Studio |
| Operational modes | Agent modes, task limits, app testing toggles, optimization | User chooses LLM provider/model | No effort/mode controls, cost signals, or capability toggles |

## Roadmap Principles

- Keep provider boundaries explicit. App Studio should orchestrate and present;
  provider-code should own repository APIs, host-specific behavior, controllers,
  and durable repository status.
- Do not put large file contents into long-lived CR specs or statuses. Use CRs
  for intent, pointers, metadata, and status. Use provider-owned bundle/blob
  storage for large generated payloads.
- Prefer CRD-native write flows for mutating operations. The user should be able
  to inspect the requested operation, phase, error, and resulting commit.
- App Studio owns checked-out project files and file inspection/mutation tools.
  Provider-code facilitates git source access through repository metadata,
  deploy keys, commit/push requests, and durable remote status.
- App Studio tools must be tenant scoped and caller scoped. Keep using the hub's
  forwarded bearer token and resolved tenant path.
- The UI should reuse shared portal tokens and components, especially for tables,
  modals, status badges, and provider-hosted panes.
- Optimize the roadmap around a tight agent loop: inspect, plan, edit, run,
  test, preview, publish.

## Phased Plan

### Phase 0: Roadmap And Product Baseline

**Outcome:** Agree on the gap inventory, priorities, and provider-boundary
constraints before adding more implementation surface.

- [x] Create this roadmap in an isolated worktree.
- [ ] Review the gap table with product and engineering owners.
- [x] Confirm that provider-code remains the source of truth for git source
  metadata, credentials, commit/push requests, and remote status.
- [x] Confirm that App Studio owns checked-out project files and workspace
  read/search/edit tools.
- [ ] Confirm that App Studio should not store generated file contents in the
  Project CR or message metadata beyond concise summaries.

**Acceptance criteria:**

- The team can name the first two capability gaps to close.
- The team agrees that App Studio owns workspace file reads/mutations while
  provider-code owns git source primitives and remote status.

### Phase 1: App Studio Workspace Awareness And Read Tools

**Why first:** Replit's agent is effective because it can inspect the workspace
before editing. App Studio currently writes but cannot read. This is the most
important gap behind the "more tools to read, edit, and mutate files" concern.

**Provider-code work:**

- [x] Keep provider-code focused on git source primitives already present:
  `Connection`, `Repository`, `DeployKey`, and `RepositoryCommit`.
- [x] Do not add provider-code live file tree/read/search APIs for App Studio
  workspace inspection.

**App Studio work:**

- [x] Add a provider-owned project workspace root for checked-out/generated
  files, configured by `APP_STUDIO_WORKSPACE_ROOT`.
- [x] Add a filesystem workspace store with safe path handling, bounded file
  reads, binary detection, result caps, and text search.
- [x] Add local LLM tools:
  - `list_project_files`
  - `read_project_file`
  - `search_project_files`
- [x] Keep `code__commit_files` as the provider-code git commit bridge for this
  slice, and mirror successful commit payloads into the App Studio workspace so
  later turns can inspect them.
- [x] Update the App Studio system prompt to require workspace
  inspect-before-edit for existing projects.
- [x] Render workspace read/search tool calls in the existing collapsed action
  bundle with concise summaries.

**Acceptance criteria:**

- The LLM can list files, read a file, and search App Studio workspace text
  before making changes.
- Tool results are summarized in the conversation without dumping large content
  into the visible feed.
- Oversized or binary files fail gracefully and guide the user.
- Provider-code remains the git source boundary rather than the project file
  browser.

**Verification:**

- `cd providers/app-studio && go test ./...`
- `cd providers/app-studio/portal && npm run build`

### Phase 2: File Browser, Commit Detail, And Diff Review

**Why next:** Once the agent can inspect files, users need to see the workspace
too. Replit's editor makes code and changes tangible; App Studio should expose a
read-only version first.

**Provider-code work:**

- [ ] Expose commit file summaries and changed paths from `RepositoryCommit`
  status if not already complete enough for the UI.
- [ ] Expose commit detail metadata, commit URLs, and git patch/diff artifacts
  where provider-code already owns the git-source interaction. App Studio
  remains responsible for workspace file contents.

**App Studio portal work:**

- [ ] Add a repository tab or side pane section that shows repository health,
  branch, last commit, and connection.
- [ ] Add a file tree backed by App Studio workspace read APIs.
- [ ] Add a read-only file viewer using mono text styling and bounded content.
- [ ] Add a commit detail view with file count, changed paths, status, error,
  commit URL, and generated summary.
- [ ] Add a diff viewer for committed changes when provider-code can provide
  patch text or before/after file snapshots.

**Acceptance criteria:**

- A user can open an App Studio project and inspect its generated files without
  leaving App Studio.
- A user can understand what each commit changed.
- Missing/deleted repositories and connections render as recoverable error
  states.

**Verification:**

- `cd providers/app-studio/portal && npm run build`
- Manual portal check through the provider route.

### Phase 3: Structured File Mutation Tools

**Why now:** Whole-bundle `commit_files` is useful for first generation, but
Replit-style iteration needs smaller, safer file operations.

**App Studio work:**

- [x] Add mutation tools against the App Studio workspace that are durable
  through today's provider-code write bundle model:
  - `write_file`
  - `apply_patch`
  - `mkdir`
- [x] Add `commit_project_files`, an App Studio bridge that reads selected
  workspace files and commits those file contents through provider-code's
  `code__commit_files` git-source boundary.
- [ ] Add delete/rename support once provider-code has a change-set or expanded
  commit bundle format that can represent git deletions:
  - `delete_file`
  - `rename_file`
- [ ] Add path validation that blocks absolute paths, parent traversal, and
  ambiguous Unicode lookalikes where feasible.
- [ ] Add idempotency keys or deterministic operation names to prevent duplicate
  file operations after retries.
- [x] Teach the system prompt to prefer small workspace edits for existing
  projects.
- [x] Render workspace mutation tool calls in the existing action summaries
  without exposing file contents.

**Provider-code work:**

- [ ] Keep commit/push as the git-source boundary through `RepositoryCommit` or
  a successor change-set resource.
- [ ] Add status that reports changed paths, operation count, validation errors,
  resulting commit SHA, and commit URL.
- [ ] Show commit progress states as provider-code status updates instead of
  leaving the UI parked on a generic "Writing" row.
- [ ] Add delete/rename operation support to provider-code if App Studio should
  persist those workspace operations to git without relying on a local clone.

**Acceptance criteria:**

- The LLM can modify one text file and commit only selected changed files,
  without recommitting the whole project.
- Users can see which files were touched before opening the resulting commit.
- Duplicate tool invocations do not produce duplicate commits for the same
  logical action once the idempotency item above is complete; the current slice
  does not claim duplicate-commit protection.

**Verification:**

- `cd providers/code && go test ./...`
- `cd providers/app-studio && go test ./...`
- `cd providers/app-studio/portal && npm run build`

### Phase 4: Runtime Sandbox And Preview

**Why this is the big unlock:** Replit closes the loop by running the app and
showing a preview. App Studio currently stops at code creation.

**Architecture direction:**

- Add a runtime owner instead of embedding arbitrary execution in App Studio.
  This could be a new provider or a provider-code sub-capability that clones a
  repository into an isolated workspace and exposes process/log/preview APIs.
- Keep process state, logs, URLs, and build phases in runtime-owned resources.
  App Studio should render them and grant the LLM tools to interact with them.

**Likely tools:**

- `create_workspace`
- `install_dependencies`
- `run_command`
- `start_dev_server`
- `read_logs`
- `stop_process`
- `get_preview_url`

**App Studio work:**

- [ ] Add runtime status to project state or a linked runtime resource.
- [ ] Add a Preview pane next to chat and repository.
- [ ] Stream setup, install, build, and run stages into the conversation.
- [ ] Teach the LLM to run and inspect the app after committing meaningful
  changes.

**Acceptance criteria:**

- A generated app can be run in an isolated environment.
- Users see logs and a preview URL.
- The LLM can use logs to diagnose and commit fixes.

**Verification:**

- Provider runtime tests for process lifecycle and isolation.
- App Studio portal build.
- Manual end-to-end flow: prompt, generate, commit, run, preview.

### Phase 5: Automated Testing And Repair

**Why after preview:** Browser and command testing require a runnable app.

**Runtime/testing provider work:**

- [ ] Add test execution tools for common project commands.
- [ ] Add browser automation against the preview URL.
- [ ] Capture structured results: pass/fail, logs, screenshots, and trace or
  replay links when available.
- [ ] Put large artifacts in provider-owned storage and expose references.

**App Studio work:**

- [ ] Show test runs as collapsed action groups with failure summaries.
- [ ] Let the LLM request browser checks and then repair based on evidence.
- [ ] Add user controls for whether testing is automatic, prompted, or disabled.

**Acceptance criteria:**

- The LLM can run a generated app, test it, observe failures, and produce a fix
  commit.
- Users can inspect the evidence without reading raw logs by default.

### Phase 6: Publish And Environment Services

**Why after test:** Publishing is valuable only after the app can run and be
validated.

**Provider integration direction:**

- Use existing provider boundaries where possible:
  - provider-code owns source.
  - infrastructure or a future deploy provider owns deployment resources.
  - a secrets/database provider owns app services.
  - App Studio coordinates the flow.

**Capabilities:**

- [ ] Add a publish wizard with domain, visibility, environment, and deploy
  target.
- [ ] Add environment variable and secret wiring through a provider-owned API.
- [ ] Add database provisioning flow, likely through infrastructure/provider
  APIs rather than App Studio-local state.
- [ ] Add deploy logs, deploy URL, health checks, and rollback.

**Acceptance criteria:**

- A user can move from generated app to reachable URL inside App Studio.
- Deployment status is durable and inspectable.
- App secrets are never sent through chat or MCP transcripts in plaintext.

### Phase 7: Tasks, Checkpoints, And Parallel Work

**Why later:** Replit's task board and background tasks are powerful, but they
need repository, runtime, and test foundations first.

**Architecture direction:**

- Add a ProjectTask API or App Studio task store model for isolated work.
- Represent each task as a branch, workspace, or runtime clone.
- Apply completed tasks through provider-code commits and merge/rebase flows.

**Capabilities:**

- [ ] Draft, active, ready, applying, done task states.
- [ ] Background task execution limits.
- [ ] Review summary, changed files, tests, preview, and apply/dismiss actions.
- [ ] Conflict detection and resolution flow.
- [ ] Checkpoint and rollback UI based on repository commits or tags.

**Acceptance criteria:**

- A user can ask App Studio to explore multiple changes in parallel.
- Ready tasks can be reviewed before they affect the main project branch.
- Conflicts are surfaced before apply.

### Phase 8: Skills, Imports, Canvas, And Design Workflows

**Why last:** These make the product feel richer, but they depend on the core
agent loop being reliable.

**Capabilities:**

- [ ] Project skills: reusable instruction packs stored with projects and
  offered in the create flow.
- [ ] Import flows: GitHub repository, ZIP, and possibly design-source imports.
- [ ] Curated MCP catalog for App Studio with user approval and per-project
  enablement.
- [ ] Canvas-like visual annotation and design variant workflow.
- [ ] Visual diff/apply flow for UI changes.

**Acceptance criteria:**

- Users can start from existing source or a reusable skill.
- Users can guide visual changes without only using text prompts.
- Additional MCP tools are explicit, reviewable, and scoped per project.

## Suggested First Implementation Slice

The smallest high-leverage next PR is Phase 1:

- App Studio workspace root and filesystem store.
- App Studio local tools to list files, read files, and search files.
- Prompt and action-summary updates for those tools.
- `code__commit_files` kept as the provider-code git commit bridge, with
  committed payloads mirrored into the App Studio workspace.

This gives the LLM visibility into App Studio's project workspace without
introducing runtime execution, deployment, Phase 2 file-browser UI, or
provider-code live file-read/search APIs.

## Risks And Mitigations

- **Large generated files overwhelm chat or CRs.** Keep contents out of CR
  status and use bounded tool responses with truncation metadata.
- **Provider boundary drift.** Keep checked-out files and file inspection in App
  Studio; keep git source metadata, deploy keys, commits, pushes, and remote
  status in provider-code.
- **Uncontrolled execution risk.** Defer shell/runtime tools until there is an
  isolated runtime provider with quotas, logs, and lifecycle controls.
- **Tool sprawl.** Add tools in families and update the system prompt, UI
  summaries, tests, and docs together.
- **User confusion during long actions.** Every long operation should stream a
  status row, expose provider status, and eventually resolve to a durable
  artifact or a clear error.

## Open Decisions After Current Slice

- Decide how App Studio should perform a true git checkout for existing remote
  repositories. Recommendation: use provider-code `DeployKey` as the
  cross-provider credential seam so App Studio can own the checkout without
  owning git-host credentials directly.
- For current write/patch flows, App Studio commits selected workspace files by
  handing bounded file contents to existing `code__commit_files` through
  `commit_project_files`. Decide whether longer-term checkout-based flows should
  keep that bundle bridge, add a provider-code change-set primitive, or let App
  Studio push through a provider-code-facilitated deploy key. Recommendation:
  keep provider-code as the durable commit/status owner either way.
- Decide how to persist delete/rename workspace operations to git. Today's
  `RepositoryCommit` bundle can write path/content entries only, so git
  deletion needs a provider-code change-set successor, expanded bundle format,
  or an App Studio-owned checkout/push flow.
- Decide the first user-facing file view once Phase 2 is resumed. Recommendation:
  project-specific App Studio workspace pane for common inspect/read flows,
  provider-code UI link for advanced repository management.
