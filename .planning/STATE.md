# STATE.md — Edge Refactor (Issue #72)

## Current Status

**Active Phase:** Phase 1 — API Foundation (not yet started)
**Last Action:** Initial planning complete (2026-02-25)
**Next Step:** `/gsd:plan-phase 1` — implement Edge CRD and client

## Phase Progress

| Phase | Status | Notes |
|-------|--------|-------|
| 1 — API Foundation | 🔲 Not started | |
| 2 — Hub Controllers | 🔲 Not started | Can start after Phase 1 |
| 3 — Virtual Workspaces | 🔲 Not started | Can start after Phase 1 |
| 4 — Agent + CLI | 🔲 Not started | Needs Phase 2 + 3 |
| 5 — e2e + Cleanup | 🔲 Not started | Needs Phase 4 |

## Key Context

- Branch: `ssh` — current working branch in `faroshq/faros`
- Module: `github.com/faroshq/faros`
- Existing CRDs: `Site` (types_site.go) and `Server` (types_server.go) — both to be deleted
- Connection pool: `pkg/util/connman/connman.go` — `ConnectionManager` is already in place
- Virtual workspace builders: 3 currently active — `edge-proxy`, `agent-proxy`, `cluster-proxy`
- Agent modes today: `AgentModeSite` / `AgentModeServer` in `pkg/agent/agent.go`

## Blockers

None currently.
