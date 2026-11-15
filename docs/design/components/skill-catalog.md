---
{"schema":1,"id":"design.components.skill-catalog","title":"Skill catalog","kind":"component","status":"draft","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"AgentKit supplies searchable catalog and workbench list presentations; providers own data, errors, details, and activation."},"appliesTo":["agents","app-studio","agentkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/skill-catalog.md#skill-catalog","role":"design"},{"path":"provider-sdk/agentkit-vue/AISkillCatalogList.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/SkillsLibrary.vue","role":"reference"},{"path":"providers/app-studio/portal/src/SkillsWorkbench.vue","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Skill catalog fixture and product uses","status":"passing","evidence":"Agents fixture: selection and detail at 390px and 822px; App Studio: focused workbench tests, typecheck, production build and budget checks. Product fixture uses mocked data; screen-reader and live provider review remain open."}]},"relatedDocuments":[{"id":"design.components.ai-command-palette","relation":"see-also"},{"id":"design.components.search-input","relation":"see-also"},{"id":"design.components.ai-conversation","relation":"see-also"}]}
---

# Skill catalog

## Purpose

Help users find and inspect reusable skills in a workspace or workbench.

## Use when

The user is reviewing a collection of skills or selecting one to inspect. The caller supplies the collection, selection, loading state, and detail presentation.

## Avoid when

The user is adding context to a single turn: use the [composer command palette](ai-command-palette.md). Selecting a catalog row opens details; it does not itself enable a skill or grant execution authority.

## Anatomy and variants

Both variants contain a labeled search, a collection heading and count, and rows with a name, description, scope, and package identifier. The catalog variant uses a responsive grid; the workbench variant uses a divided list with an optional status and chevron. Callers can suppress status when the collection has no per-owner activation state.

## Behavior

Search filters the provided skills; clearing search restores the full collection and returns focus to search. Initial loading, refreshing existing results, an empty collection, and no search matches are distinct. No matches identify the trimmed query and point to the existing clear control; long queries are shortened in the message while the search value remains unchanged. Selecting a row emits the skill to the caller, which owns any inline detail or dialog and the result of subsequent actions. Failure and retry are caller-owned and must not be presented as an empty collection.

## Content

Use descriptive skill names and short explanations. Scope and package identify where the skill comes from. An absent description says so rather than implying a capability. Enabled and disabled labels describe the caller's activation state, not selection for the current turn.

## Layout and responsive behavior

Catalog rows arrange in two columns when the catalog itself is at least 32rem wide and a single column in narrower panels, independent of window width. Workbench rows can wrap longer text; catalog rows truncate. The current detail row uses a subtle accent surface. Keep the full skill name available to assistive technology and expose details through the row action.

## Accessibility

Search has an accessible label and a named clear control. Rows are native buttons with an accessible name, identify the current detail with `aria-current`, and advertise a dialog only when a dialog opens. During refresh, retained results are marked busy and a separate live status announces progress. Coarse-pointer controls use the shared touch-target recipe.

## Code and evidence

The canonical component is [`AISkillCatalogList.vue`](../../../provider-sdk/agentkit-vue/AISkillCatalogList.vue). The Agents [library](../../../providers/agents/portal/src/views/SkillsLibrary.vue) uses inline details without activation badges; App Studio's [workbench](../../../providers/app-studio/portal/src/SkillsWorkbench.vue) opens details in a dialog. The review fixture mounts the App Studio workbench with mocked detail reads and selectable activation outcomes, including a successful save followed by a failed refresh. The switch retains focus and displays the accepted state; the dialog offers a retry for the refresh. The [Agents library fixture](http://127.0.0.1:5198/?provider=agents&scenario=skill-catalog&route=%23%2Fskills) mounts the real library with two sample workspace skills and read-only mocked responses; selection and detail loading were checked at 390px. Neither fixture verifies writes or live provider packages.

## Related guidance

See [AI conversation](ai-conversation.md), [search input](search-input.md), and the [composer command palette](ai-command-palette.md).
