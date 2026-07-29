# App Studio Work Item Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent an unrelated App Studio request from inheriting another task's messages, todos, checkpoints, or mutation authority by making user-selected WorkItems the durable execution boundary.

**Architecture:** App Studio owns Project UID scope, WorkItem lifecycle, run eligibility, context selection, and WorkItem-scoped grants. Eino continues to own execution, exact checkpoint resume, and graceful TurnLoop cancellation. Ask is the default read-only action; Build and Continue are explicit mutation actions; Resume targets one existing pending run.

**Tech Stack:** Go, PostgreSQL, Eino ADK v0.9.9, Vue 3, TypeScript, Node test runner.

## Global Constraints

- Implement the approved net-new schema; do not add migration compatibility for old App Studio conversation data.
- Keep all provider changes under `providers/app-studio`.
- Every production change starts with a focused failing test whose failure is observed.
- Keep exact permission/input approval in the existing Eino checkpoint and claimed-resume path.
- Do not introduce Eino Workflow, GraphTool, PlanTask persistence, a second approval ledger, or a generalized scheduler.
- Preserve one nonterminal run per Project UID and the single-provider-replica assumption.
- Run Go validation from `providers/app-studio`; run portal validation from `providers/app-studio/portal`.

---

## Task 1: Establish the durable WorkItem store contract

**Files:**

- Modify: `providers/app-studio/store/store.go`
- Modify: `providers/app-studio/store/memory.go`
- Modify: `providers/app-studio/store/postgres.go`
- Modify: `providers/app-studio/store/encryption.go`
- Modify: `providers/app-studio/store/assistant_run_contract_test.go`
- Create: `providers/app-studio/store/assistant_work_item_test.go`

- [x] Add failing contract tests proving Project UID isolation, one active WorkItem per Project UID, immutable message WorkItem membership, immutable run mode, and no grant use across WorkItems.
- [x] Add failing tests for atomic Build creation, WorkItem CAS, scoped message loading, WorkItem plan approval, and terminal transition clearing `active_run_id`, checkpoint, and grant.
- [x] Run `go test ./store -run 'Test.*(WorkItem|ProjectUID|RunMode)'` and confirm failures reflect missing behavior.
- [x] Add `ProjectUID` to `Scope`; add `ProjectUID`, `ActorID`, and `WorkItemID` to messages; add WorkItem ID, mode, expected grant revision, and `stopping` to runs.
- [x] Define `AssistantWorkItem`, lifecycle statuses, grant fields, revisions, and the minimum atomic Store operations from the design.
- [x] Implement the contract in `MemoryStore`, keeping all compound transitions under its mutex.
- [x] Replace the embedded PostgreSQL schema with the final net-new messages, WorkItems, and runs schema. Include Project UID in keys/indexes, a unique scoped root message, one active WorkItem, and one nonterminal run.
- [x] Implement PostgreSQL scans and operations transactionally; remove the reserved `approved-plan-grant` run convention.
- [x] Extend `EncryptedStore` so WorkItem grant blobs use AAD containing full scope, Project UID, and WorkItem ID.
- [x] Run `go test ./store` and confirm the store suite passes.
- [x] Commit: `feat(app-studio): add durable assistant work items`

## Task 2: Derive server scope and actor identity, then add explicit actions

**Files:**

- Modify: `providers/app-studio/api/projects.go`
- Modify: `providers/app-studio/api/assistant_supervisor_http.go`
- Modify: `providers/app-studio/api/assistant_run_manager.go`
- Modify: `providers/app-studio/api/server.go`
- Modify: `providers/app-studio/api/assistant_contract.go`
- Modify: `providers/app-studio/api/assistant_contract_test.go`
- Modify: `providers/app-studio/api/assistant_supervisor_http_test.go`
- Create: `providers/app-studio/api/assistant_work_item_http_test.go`

- [ ] Add failing API tests showing omitted action is Ask, Ask cannot become Build, Build creates a rooted WorkItem, Continue requires a selected same-actor WorkItem/revision, and same-name Project recreation cannot load the old Project UID.
- [ ] Add failing tests for one-time same-actor root-message activation and conflicts on relink/concurrent activation.
- [ ] Run the focused API tests and observe the expected contract failures.
- [ ] Add the `assistantAction` request shape (`ask`, `build`, `continue`) and server-derived run modes (`discussion`, `new`, `continue`).
- [ ] Derive actor and Project UID from authenticated/Kubernetes state; never accept either from the client.
- [ ] Route Ask through discussion-run creation and Build/Continue through the new atomic WorkItem operations.
- [ ] Add list/get/cancel WorkItem endpoints and validate scope, Project UID, creator, revision, lifecycle, and idempotency.
- [ ] Keep the initial-project prompt as an implicit Build.
- [ ] Run the focused API tests, then `go test ./api`.
- [ ] Commit: `feat(app-studio): add explicit work item actions`

## Task 3: Scope routing, model context, progress, and grants to one WorkItem

**Files:**

- Modify: `providers/app-studio/api/llm.go`
- Modify: `providers/app-studio/api/assistant_turn_profile.go`
- Modify: `providers/app-studio/api/assistant_eino_engine.go`
- Modify: `providers/app-studio/api/assistant_checkpoint.go`
- Modify: `providers/app-studio/api/assistant_approved_plan.go`
- Modify: `providers/app-studio/api/assistant_eino_tool.go`
- Modify: `providers/app-studio/api/assistant_turn_profile_test.go`
- Modify: `providers/app-studio/api/assistant_eino_engine_test.go`
- Modify: `providers/app-studio/api/assistant_approved_plan_test.go`
- Modify: `providers/app-studio/api/assistant_eino_tool_test.go`
- Create: `providers/app-studio/api/assistant_work_item_isolation_test.go`

- [ ] Add a regression test reproducing quote submission followed by a theme-status Ask and prove quote messages/todos/grants are absent from the later run.
- [ ] Add failing tests proving Ask gets no mutation tools or WorkItem grant, Continue receives only selected WorkItem messages, and a WorkItem cannot consume another item's grant.
- [ ] Add failing tests proving a terminal checkpoint/todos never enter a Continue run and that mutable Eino session values cannot widen authority.
- [ ] Run the focused tests and observe failures before implementation.
- [ ] Route using the current message plus declared action; remove historical escalate-only mutation routing.
- [ ] Assemble mutation context from root/current/WorkItem-linked messages only. Broader Ask history remains read-only and cannot select a task or supply authority.
- [ ] Replace project-wide reserved-run grant load/save/clear with WorkItem approval and expected grant revision.
- [ ] Reload and validate actor, Project UID, run mode/status, active WorkItem/run, grant revision, capability/path, repository HEAD, and target digest immediately before a mutation.
- [ ] Preserve exact Eino interrupt resume as the only run-local exact approval path.
- [ ] Scope plan/todo projection through the selected WorkItem's active or latest run; keep todos non-authoritative.
- [ ] Run focused tests, then `go test ./api`.
- [ ] Commit: `fix(app-studio): isolate assistant execution by work item`

## Task 4: Implement graceful Stop and mutation admission

**Files:**

- Modify: `providers/app-studio/api/assistant_supervisor.go`
- Modify: `providers/app-studio/api/assistant_supervisor_http.go`
- Modify: `providers/app-studio/api/assistant_eino_engine.go`
- Modify: `providers/app-studio/api/assistant_eino_interrupt.go`
- Modify: `providers/app-studio/api/assistant_eino_tool.go`
- Modify: `providers/app-studio/api/server.go`
- Modify: `providers/app-studio/api/assistant_supervisor_test.go`
- Modify: `providers/app-studio/api/assistant_eino_engine_test.go`
- Modify: `providers/app-studio/api/assistant_eino_tool_test.go`
- Create: `providers/app-studio/api/assistant_stop_test.go`

- [ ] Add failing tests for `running -> stopping`, stop-time grant revocation, pending-run terminal stop, repeated Stop, Stop-vs-Resume, pre-start Stop, and Stop-vs-natural-completion.
- [ ] Add failing admission-gate tests proving an admitted mutation may return but no mutation is admitted after Stop closes the gate.
- [ ] Add failing Eino adapter tests for `WithGracefulTimeout`, `WithSkipCheckpoint`, `WithStopCause("user_stop")`, `CancelError` classification, and checkpoint deletion.
- [ ] Run focused tests and verify failures are behavior-specific.
- [ ] Retain the active Eino TurnLoop control handle in the supervised run and add a short-lived per-project mutation admission gate.
- [ ] Implement `POST /assistant/{run}/stop` with exact revision/idempotency semantics and `202 Accepted` while the loop is still exiting.
- [ ] Implement Eino graceful Stop plus background `Wait`; preserve natural terminal outcomes when they win the race.
- [ ] Make the checkpoint adapter implement `adk.CheckPointDeleter`; suppress/delete terminal checkpoints.
- [ ] Add server-owned mutating-tool deadlines and require cancellation-aware wrappers.
- [ ] Keep a non-cooperative execution fail-closed in `stopping`; update startup recovery for abandoned `stopping` runs.
- [ ] Run focused tests, then `go test ./api`.
- [ ] Commit: `feat(app-studio): add graceful assistant stop`

## Task 5: Add WorkItem-aware portal controls

**Files:**

- Modify: `providers/app-studio/portal/src/types.ts`
- Modify: `providers/app-studio/portal/src/api.ts`
- Modify: `providers/app-studio/portal/src/App.vue`
- Modify: `providers/app-studio/portal/src/AssistantPlanDock.vue`
- Modify: `providers/app-studio/portal/src/assistantPlan.ts`
- Create: `providers/app-studio/portal/src/assistantWorkItems.ts`
- Create: `providers/app-studio/portal/src/assistantWorkItems.test.mjs`
- Modify: `providers/app-studio/portal/src/assistantPlanDock.test.mjs`
- Modify: `providers/app-studio/portal/src/conversationResilience.test.mjs`

- [ ] Add failing portal tests for Ask default, explicit Build, selected Continue, Start task, exact Resume/Stop, suspended Continue/Discard, `stopping` action lockout, and WorkItem-scoped plan selection.
- [ ] Run the new Node test plus affected existing tests and observe expected failures.
- [ ] Extend API/types for actions, WorkItems, run mode/status, Stop, and Cancel.
- [ ] Add an Ask/Build composer intent control with Ask as the default; make initial-project submission Build.
- [ ] Render WorkItem lifecycle actions without inferring continuation from free-form text.
- [ ] Replace immediate abort with Stop for a live/pending run and render a noninteractive Stopping state.
- [ ] Select the plan dock only from the current WorkItem's active/latest run.
- [ ] Run all portal Node tests, `npm run typecheck`, and `npm run build`.
- [ ] Commit: `feat(app-studio): add work item conversation controls`

## Task 6: End-to-end regression and final verification

**Files:**

- Modify as needed: `providers/app-studio/api/assistant_work_item_isolation_test.go`
- Modify as needed: `providers/app-studio/README.md`

- [ ] Add/complete a single integration-style test for the incident sequence: partial quote task, unrelated theme Build, later theme-status Ask; assert no quote context, todo, grant, or mutation capability crosses the WorkItem boundary.
- [ ] Run `go test ./...` from `providers/app-studio`.
- [ ] Run every portal `test:*` script, `npm run typecheck`, and `npm run build`.
- [ ] Run `git diff --check` and inspect `git status --short`.
- [ ] Request an independent whole-branch review covering correctness, regressions, authorization, Eino semantics, and missing tests; address all valid findings.
- [ ] Re-run the full verification suite after review fixes.
- [ ] Commit any final focused fixes and prepare the branch for user review.
