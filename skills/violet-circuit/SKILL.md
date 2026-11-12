---
name: violet-circuit
description: Implement or review a Railgrid portal UI change against Violet Circuit. Use for changes to portal and provider portal presentation, interactions, UI copy, or shared PortalKit and AgentKit components that need a same-agent critique and repair pass.
---

# Violet Circuit self-review

The implementing agent runs both passes. Use the repository's design authority and the running change as evidence.

## Before editing

1. From the repository root, read `docs/design/README.md`, the relevant component or pattern, and `docs/design/quality/review-checklist.md`. Start with canonical shared code. Use a shipped screen as a comparison for composition, respecting the design entries' authority and implementation status.
2. Record a few observable expectations before writing code. Name the affected route or component, the shared primitive or precedent, the user's next action, and the state most likely to expose a mistake. Include a keyboard or narrow-screen expectation when the change affects it. For a review of an existing diff, record the expectations before opening the rendered result.

## Critique your implementation

1. Read the diff as a reviewer. Trace state transitions and compare markup, styles, and copy with the canonical implementation. Look for duplicate primitives, local overrides, competing recovery actions, and paths that confuse loading, failure, and settled empty results. Use the conformance gates in `AGENTS.md` for mechanical rules.
2. Try to falsify your expectations in the browser. Inspect the affected route using `docs/dev-browser.md`. Exercise the task and the chosen adverse state, checking what the user sees and where focus lands after the action. Batch relevant viewport and theme checks. For a concrete rule such as a coarse-pointer target size, measure it. Label fixture evidence as such and record any live check you could not run as unverified.
3. Record observed findings before editing again. For each one, include the applicable rule, route or file, state, expected behavior, and actual behavior. Include a measurement or screenshot when it clarifies the issue. Surface unsettled design choices separately from violations of an existing contract.
4. Fix the confirmed issues in one pass and rerun the affected checks. Report what you caught, what changed in response, and what is still unverified. A clean lint run covers source rules. Browser evidence supports claims about rendering and interaction.

For example, a resource list can use the shared table and still show an empty state after a failed background refresh. Load known rows, force the failure using a local test path, and check whether the rows stay visible with a stale-data message, as specified in `docs/design/patterns/resource-reads.md`.
