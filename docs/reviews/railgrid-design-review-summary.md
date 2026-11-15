# Railgrid design work: review guide

Snapshot: **28ca321da**, compared with its merge base **538ffedd434bfe3e800d72ef64917e481a234169** (the local `main` at the time of this review). This snapshot contains **242 commits, 893 changed files, 36,905 insertions and 8,304 deletions**. It predates this review guide itself. Many changed files are generated copies of shared components, fixtures, or tests. Local `origin/main` was 27 commits ahead of the merge base when this guide was written; it has not been refreshed from the network.

This is a review map, not an assertion that all changes are approved or verified. [Every changed file](railgrid-design-review-files.txt) and [every commit subject](railgrid-design-review-commits.txt) are included in separate snapshot inventories. For the full patch, use `git diff 538ffedd434bfe3e800d72ef64917e481a234169 28ca321da`. For a specific area, append `-- <path>`.

## How approval is classified

- **Reviewed together** means the conversation contains your positive feedback on that component's design at the time. It does **not** mean you approved every later code change, provider adoption, edge case, or the final branch diff.
- **Needs your review** means the final behavior or scope has no clear approval in the conversation. It does not mean it is defective. Where a component was skipped, reverted, or only briefly discussed, that is called out.
- The book's `verified`, `partial`, and `unverified` metadata describe implementation evidence, not your approval. Of 53 entries whose kind is `component`, 3 are marked verified, 41 partial and 9 unverified. Three additional entries in the components directory are references. These states do not supply a count of components you have or have not personally approved.

## Components and patterns

### Reviewed together at the time

| Area | Decisions and scope of changes | What to check now |
|---|---|---|
| StatusBadge | Quieter surface and weight; animate the dot only for in-progress states. | Confirm the final status colors, motion and product mappings. |
| ResourceTable, filters, hidden columns, pagination | Quieter borders and spacing; redesigned filters; discoverable columns and mobile scrolling; compact pagination and accessible page size. | Check narrow and wide layouts, filter states and the later keyboard/focus/read-state changes. |
| Row actions and ActionMenu | Persistent transparent more trigger and contextual actions. | Check action density and focus/dismissal in real tables. |
| ConfirmDialog | Clear consequence copy and selective type-to-confirm; styling revised after your Vercel reference. | Native-dialog and focus-containment changes happened later; review them, including nested dialogs. |
| Toast | Quieter transient success; persistent actionable feedback; links, actions and undo variants. | Review final duration, action and error behavior in context. |
| FormSelect | Quieter closed/open states and errors. | Recheck later keyboard, native-label, disabled-option and modal-popup changes. |
| Tabs | Separate primary and smaller secondary tabs, violet active state, no primary hover fill, mobile spacing. | Verify hierarchy and final hover/focus across pages. |
| FirstRunGuide / empty states | Center first-use states, keep guided multi-step setup, distinguish no-results and error; dedicated four-state review with Models and Edge fixtures. | Review product-specific copy and later refresh/retry behavior. |
| LayoutSelector | Visible segmented grid/list switch and placement outside unrelated navigation. | Verify placement, touch targets and persisted selection. |
| ResourceSectionCard and ResourceStatCards | Quieter borders and icons, use dividers where appropriate, emphasize values, shorten rollout copy. | Check the deployment detail composition and responsive spacing. |
| Tooltip | Shorter delay, Escape, edge placement. | Later focus, hover and modal interactions need review. |
| ResourcePage | Clearer heading/metadata and skeleton matching the final layout. | Review loading, stale/error/retry and focus in product contexts. |
| CreateGuidance and FieldGroup | Concise pre-submit guidance, field help next to controls, streamlined toolset group. | Check small-screen spacing and actual creation flows. |
| Dashboard usage, Toggle and Progress | Clear quota labels and near/reached/over states; on/off and disabled context; task progress distinguished from quota. | Confirm thresholds, wording and mobile views. |
| Model connections | Clarify provider connections versus model bindings in Agents and App Studio. | Later connection validation, recovery and product behavior need review. |
| Buttons, Text input, Checkbox, Radio, Search, Textarea | Quieter secondary controls; input focus/error states; complete choice, search and textarea states; more instruction space and mobile resize. | Review later validation, native-control and focus changes. |
| Spinner and Copy control | Busy row action retains focus; quiet copy icon with local success and clear failure. | Review actual failure, busy and keyboard states. |
| File upload, Disclosure and Timestamp | Contextual composer and standalone uploads with types/errors; quieter disclosure; relative timestamps with exact time accessible, including run steps. | Check later product adoption and accessibility. |

### Needs your review or a fresh pass

| Area | What changed or why it is separate |
|---|---|
| Avatar | You rejected the attempted redesign and asked for the avatar to be restored to main. It should not be counted as an approved redesign; verify the final diff before any further avatar work. |
| Keyboard shortcut | You chose to wait for a real shortcut. No approval of a general shortcut component is implied. |
| ResourceBackLink | You explicitly skipped it. The branch later includes link/back-link and navigation-semantic changes; review those separately. |
| AI conversation components | Shared action rows, activity/feed/disclosures, execution details, interruption, message actions, workbench, composer command palette, attachment handling and AI timestamps changed. Your approval of individual foundational components does not approve these AI workflows as a whole. |
| Model and reasoning picker; ModelIDSelector | Picker behavior, touch, connection forms, retries and selection states have dedicated fixtures; the broader final interaction needs review in both Agents and App Studio. |
| Dashboard tiles; ResourceBoard; ConditionsPanel; InlineNotification; loading placeholder; Tokenized list input; Link; Skill catalog; Digest schedule; Terminal dock; Relationship graph; generic menu and supporting contracts | Added or revised documented components, states or product examples beyond the explicit component approvals above. Review their contracts and product use. |
| AgentKit and shared modal/popup infrastructure | Later changes add native confirmation dialogs and modal-aware popups. The fallback without Popover support can place a panel outside an ARIA modal; other browsers and assistive technology need review. |
| Preview annotation picker | Added keyboard target navigation and a comment-field focus outline after the walkthrough. A signed synthetic iframe fixture exercises it; exact host-toolbar focus restoration on cancel remains open. |
| Foundations | Dedicated materials, typography, surface/text roles, colors, spacing, geometry, layering, motion, recipes and provider integration. You requested materials and typography pages, but the conversation does not establish approval of the complete final foundations or every later revision. |

The [component directory](../design/components/) is the complete book inventory; [the PortalKit asset index](../design/components/portalkit-assets.md) maps distributed source files to their contracts. Several entries describe a supporting asset or product pattern rather than a standalone visual control.

## Product changes to review

| Product | Major changes in this branch | Review status |
|---|---|---|
| Edge Services and Edges | Services list/filter/row actions; clear service targets and connection status; creation/first-run guidance; URL, host, scheme, label and workload validation; detail refresh and recovery. See [Services.vue](../../providers/edges/portal/src/Services.vue) and [edge service journey](../design/journeys/edge-service-setup.md). | You reviewed the initial Edge example and several shared controls in context. Later validation, target labels, recovery and navigation changes need a fresh product pass. |
| Agents | Run investigation, run detail and Activity; approval and question focus; chat, toolsets, connections, models, skills, webhooks, automation and schedules; background refresh, validation and recovery. See [views](../../providers/agents/portal/src/views/) and [AgentKit](../../provider-sdk/agentkit-vue/). | Initial agent-run example and multiple controls were reviewed together. Later behavior and cross-flow changes need review. |
| Infrastructure | Deployment detail composition, section/stat cards, instance and catalog reads, provisioning and dynamic forms, typed enums, map fields and mobile tooltip. See [Infrastructure views](../../providers/infrastructure/portal/src/views/) and [DynamicForm.vue](../../providers/infrastructure/portal/src/components/DynamicForm.vue). | Deployment fixture and relevant cards were reviewed together. Later form, catalog, validation and recovery changes need review. |
| App Studio | Model/provider setup, project/workbench, sharing, integrations and preview, chat/composer and annotation keyboard interaction, focus and recovery. See [App.vue](../../providers/app-studio/portal/src/App.vue) and [ModelsSettings.vue](../../providers/app-studio/portal/src/ModelsSettings.vue). | Model relationship fixtures were discussed. Most later workspace, sharing, preview and recovery changes need review. |
| Host portal | Organization and workspace chooser/creation, navigation and dashboard, provider loading/recovery, MCP access, billing and administrative surfaces. See [portal pages](../../portal/src/pages/) and [journeys](../design/journeys/). | No blanket approval of the final host changes is evident. Review separately, especially tenancy and recovery. |
| Code | Connection, repository and package list/detail navigation, read state, failure and recovery. See [Code views](../../providers/code/portal/src/views/). | Needs product review. |
| Kuery | Topology and impact relationship presentation, list alternative, palette/legend and query/recovery states. See [graph.ts](../../providers/kuery/portal/src/graph.ts). | Needs product review, including perceptual distinction and assistive technology. |
| Quickstart | Shared control adoption, greeting actions and refresh/retry feedback. See [Quickstart portal](../../providers/quickstart/portal/src/). | Needs product review. |

## Nonvisual and cross-cutting changes requiring technical review

1. **Backend/API behavior:** [workspace REST handling](../../pkg/hub/restapi/workspaces.go) adds a recovery deadline under specific deletion conditions; [model catalog JSON rendering](../../providers/app-studio/api/modelcatalogjson/render.go) changes the portal projection and omits metadata. These are not just cosmetic changes; review compatibility and tests.
2. **Shared implementation and distribution:** Canonical PortalKit and AgentKit changes are copied across host/provider portals. Review the canonical files and sync manifest first, then verify copies; repeated files do not mean separate design decisions.
3. **Interaction and data lifecycle:** Many commits change focus restoration, keyboard handling, cancellation, stale-data retention, retry and serialization. These alter behavior and deserve more than a visual pass.
4. **Review system:** The branch adds the structured knowledge base, review catalog, component and product fixtures, source locators, conformance checks and design-review instructions. See [design review README](../../hack/design-review/README.md) and [design book index](../design/README.md).
5. **Tests and limits:** Fixture and mounted-test results do not prove live backend behavior, cross-browser behavior, assistive-technology support, or a production deployment. [Known oddities](../design/quality/known-oddities.md) records remaining boundaries. The App Studio bundle budget was adjusted during this work with your permission.

## Suggested review order

1. Read the [file inventory](railgrid-design-review-files.txt) and [commit inventory](railgrid-design-review-commits.txt); inspect backend/API changes first.
2. Review foundations and canonical shared controls, separating your earlier design approval from later behavioral changes.
3. Review Edge Services, Agents, Infrastructure and App Studio in their product fixtures, then host, Code, Kuery and Quickstart.
4. Review the remaining unapproved components and the explicit gaps above. Record new decisions and any changes separately; do not treat this document as a sign-off.

Run `make serve-design-review`, then open <http://127.0.0.1:5197/> for Fixture, Contract and Sources views. Some product fixtures require their own local servers; commands are in the [review setup](../../hack/design-review/README.md). All review fixtures use simulated data unless explicitly identified otherwise.
