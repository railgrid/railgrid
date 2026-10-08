# Edges, Code, and core PortalKit audit

Durable browser measurements and check results are in [browser evidence](browser-evidence.json). References below to `/tmp/railgrid-ui-audit/` are supplemental machine-local logs and captures.

Audit owner: edges_code subagent. Source review performed 2026-10-07/08 against the active design book under `docs/design/`, especially foundation geometry/typography/recipes/provider integration, control and resource-creation/read patterns, component contracts, interaction accessibility policy, and review checklist. Read the Impeccable craft floor; pinned Railgrid standards govern this existing product.

This is a complete source inventory review of the two assigned provider portals and a second independent review of all 22 canonical core Vue controls and visual framework-neutral controls. Runtime evidence below covers specific mounted state/interaction contracts; the root agent owns authenticated browser/theme/mobile evidence. Source and unit checks alone do not establish a comprehensive screen-reader or rendered accessibility audit.

## Edges component coverage

All 11 provider-owned Vue components reviewed, including alternate read/mutation/empty/error states and supporting provider-local styles.

| Component | Review and outcome |
| --- | --- |
| App.vue | Reviewed route composition, tab navigation, resource refresh serialization, confirmation/deletion feedback, wizard routing and context changes. Fixed caller scope to use actual host `userId`, fallback `sub`/email; host fetch replacement updates client. Token renewal retains authoritative collection snapshot, identity change clears/fences it. |
| DashboardTile.vue | Fixed eager clearing on read failure and unsafe concurrent polling. Uses existing serialized/fenced adaptive refresh controller; retains populated AND authoritative empty snapshots through failure/credential renewal; clears on caller/tenant change. Added Retry, initial alert/cached polite status, off-layout updating status, aria-busy, non-color Connected/Offline text and object-specific accessible row names. |
| Detail.vue | Reviewed all Edge summary/status/labels, conditional SSH/services/configuration/technical panels, update/delete/reconnect behavior. Fixed credential input's missing persistent short Access token label and conditional description; disabled during connect; busy region. Existing ResourcePage/SectionCard/StatCards and masked secret paths preserved. |
| EdgeCollection.vue | Reviewed table/client cursor state, kind/status controls, first-run and zero-match distinction, queryable/simple table use, row navigation/delete. Uses canonical primitives and authoritative read lifecycle; no local visual change needed. |
| HarnessCard.vue | Reviewed enabled switch, permission radios, terminal workflow and advanced allowlist editor. Fixed concise title-based accessible names and separate help descriptions; local Save controls ghost so terminal remains the primary action. Native fieldsets/radios and existing mutation feedback preserved. |
| ServiceCreate.vue | Reviewed guided form, no-edge prerequisite, edge/type discovery, manual/preset fields, Kubernetes-vs-host targeting and create validation. Added recoverable prerequisite read error/Retry preserving selected edge and draft; native target fieldset/legend; concise host label plus description; aria-busy; blocks submit while discovery/saving. First-run appears only after authoritative no-edge read. |
| ServiceEdit.vue | Reviewed Configuration/Access/Health/Technical panels, secret rotation, delete and saved/error states. Target group now fieldset/legend; host and credential controls have short persistent label and helper descriptions. Existing reference-only credential disclosure maintained. |
| Services.vue | Reviewed server cursor paging/filter/search, first-run and no-match behavior, retry, selectable row actions and deletion lifecycle. Canonical table/read contracts already adopted; unchanged local source. |
| Wizard.vue | Fixed custom create geometry to canonical back/header/title/description, guided form body, single Create primary and footer spanning fields+help rail. Steps 2/3 also use create surface/footer geometry. Native submit, required name and disabled draft/type inputs during create. Reviewed focus movement, progress, token generation/expiry, masked snippets, copy retry and connected state; preserved these behaviors. |
| WorkloadCreate.vue | Reviewed manual/marketplace modes, installation package/version selection, target selector, namespaces, image pull secrets, create and parse errors. Added form busy states and short explicit labels with separate validation/help descriptions. |
| Workloads.vue | Reviewed workload/server paging, expanded edge status details, target namespace, first-use marketplace guidance, query/no-match distinction and refresh/delete behavior. Existing canonical primitives/read handling retained. |
| style.css | Full provider-local stylesheet reviewed. Fixed mobile one-column rules overwritten by later equal-specificity desktop definitions; corrected field min-width and helper wrapping; sentence-case form labels use local selectors; removed obsolete custom wizard header styles. Preserved canonical `.k-*` ownership without provider redeclarations. |

Supporting nonvisual inventory: api.ts, element.ts, main.ts, macos.ts, marketplace.ts, pagination.ts, refresh.ts, routes.ts, serviceValidation.ts, types.ts. UI entry/context/injection and read/mutation helper use reviewed; generated marketplace JSON is data, not a separate component. `types.ts` adds host userId to the documented context shape. Existing API/pagination/refresh/routing/validation tests run with the frontend gate.

## Code component coverage

All 9 provider-owned Vue components reviewed, including OAuth/token alternates, detail subsection forms and error/loading/empty states.

| Component | Review and outcome |
| --- | --- |
| App.vue | Reviewed all route/context/journey/confirmation states. Fixed actual host userId authority/journey keys and separated mutation generation from view generation: token renewal fences old mutations without remounting useful cached routes/drafts, caller/tenant change resets view identity. |
| DashboardTile.vue | Fixed caller-aware scope fences and credential renewal behavior; retains authoritative snapshots, cached failure is polite, empty state is live status. Added visible Ready/Not ready and resource-specific row names. Existing serialized refresh controller kept. |
| ConnectionCreateView.vue | Reviewed GitHub browser OAuth, manual token form, guided prerequisites, callback fallback, pending/failed/cancel states and masked credential rules. Canonical route-owned guided create surface and field labels already conform. Added revalidation on credential authority changes without clearing draft, and same mounted-operation busy recovery after a fenced result. |
| ConnectionDetailView.vue | Reviewed summary/status/scope, credential references, OAuth/update flow, deleting and failure states. Added actual mono owner/login treatment; sensitive token values never render. |
| ConnectionsView.vue | Reviewed authoritative empty first-run, table/search/filter/paging, row actions, deleting and retry behavior. Shared table/status primitives already conform; unchanged. |
| PackagesView.vue | Reviewed read/pagination modes, empty/no-match, version lists and external package destination. Version counts now mono/tabular; generic View links now carry package-specific accessible names. |
| RepoDetailView.vue | Reviewed repository details, managing connection switcher, GitHub access keys/collaborators, packages, all local save/delete/error states and raw technical disclosure. One primary Open repository action; secondary subsection actions ghost. Public-key control has short label plus helper; native checkbox recipe/hit area; decorative warning hidden; forms aria-busy; version counts mono/tabular and external View names include package object. |
| RepositoriesView.vue | Reviewed paginated/query table, first-run, stale retry, row action isolation and visible external destination. External Open links now name repository object. |
| RepositoryCreateView.vue | Reviewed prerequisite journey, guided name/connection/visibility form, pending create/error/cancel behavior. Native checkbox uses shared recipe/hit area; no custom control reinvention. Added credential authority read revalidation preserving draft and same mounted-operation busy recovery after fenced preflight. |
| style.css | Full stylesheet reviewed. Added missing `.mono` implementation used throughout technical labels; field/header/read error min-width and wrapping; mobile header/error/managing-connection switcher stacks. Preserved shared `.k-*` recipe authority. |

Supporting nonvisual inventory: api.ts, context.ts, element.ts, main.ts, hybridPagination.ts, packagesPagination.ts, repositoriesPagination.ts, refresh.ts, journey.ts, routes.ts, types.ts. API/authority, entry/context and read/paging/navigation helpers reviewed for visual state ownership; no independent rendered components. `types.ts` now accepts actual host userId. Full existing helper tests included in the frontend gate.

## Canonical core review coverage

All 22 core Vue components and their relevant support/visual helpers reviewed. Changes coordinated with root; canonical sources only, distribution sync owned by root.

| Component | Review and outcome |
| --- | --- |
| ActionMenu.vue | Typed tones, native action controls, keyboard/disabled/busy/tooltip and bounded Teleport geometry reviewed. Existing complete source Tab path conforms. Cached deactivation previously left open state alive; fixed at its owning `useAnchoredPopover` primitive. |
| ConditionsPanel.vue | Sanitized conditions, stale health, shared bounded table reviewed. Found ISO Since timestamp in sans; root coordinates `k-cell-mono` correction. |
| ConfirmDialog.vue | Reviewed title/description, dangerous confirmation, native buttons, initial focus, Tab containment, Escape/backdrop, restore focus and bounded overflow. Existing shared contract preserved. |
| CreateGuidance.vue | Reviewed labeled aside/sections, native lists and definition values, technical code values, help rail DOM order/mobile recipe. Conforms to guided create contract. |
| FirstRunGuide.vue | Reviewed concise value proposition/action/journey roles and current/completed markers, native links/buttons, accessible names/status and responsive rail. Existing contract retained. |
| FormSelect.vue | Reviewed shared input/menu recipe, full name/describedby/required/invalid props, active descendants, disabled choices, keyboard and viewport placement. Added own deactivation close so cached routes cannot leave a body-teleported popup open. |
| InlineNotification.vue | Reviewed semantic tones, live channel, async action busy/failure behavior, plain text content, dismiss and coarse pointer targets. Existing canonical pattern retained. |
| LayoutSelector.vue | Reviewed controlled mode, preference behavior, keyboard roving/Home/End/Escape/Tab, viewport bounds. Already closes on deactivation and source-order Tab; generic primitive close remains idempotent. |
| ResourceBackLink.vue | Reviewed native navigation/click interception, disabled route, icon-only naming obligation, RTL arrow and 44px touch support. Existing contract retained. |
| ResourceBoard.vue | Reviewed named scroll regions, lane/empty-hidden/reveal controls, focus return and bounded overflow. Existing board geometry and discoverable empty columns retained. |
| ResourcePage.vue | Reviewed initial read/error, retry latch, cached failure, loading delay, header/body/section slots and out-of-layout refresh announcements. Root coordinates foreground-only retry progress refinement. |
| ResourceSectionCard.vue | Reviewed labeled header/eyebrow/title/actions/body, container-aware mobile/wrapping, no duplicated provider geometry. Conforms. |
| ResourceStatCards.vue | Reviewed count layouts, compact density, caller facts, decorative icons, technical mono values and wrapping. Existing shared contract retained; root coordinates shared title/display typography. |
| ResourceTable.vue | Fixed authoritative empty client toolbar instability under refresh; retry synchronous latch prevents repeat events and retains useful error; immediate busy/Retrying/Refreshing/Updating announcements distinguish user action from background timers. Empty/no-match copy now polite status. Reviewed native table semantics, nested-control isolation, selection/off-page/client pruning, bounded horizontal overflow and row identifier disclosure. |
| ResourceTableActionButton.vue | Reviewed action/resource name, busy/disabled label, native button, spinner/status and tooltip association. Conforms. |
| ResourceTableActionTooltip.vue | Reviewed body positioning, overflow bounds, hidden initial measure, keyboard/pointer visibility and cleanup. Existing behavior retained. |
| ResourceTableDeleteButton.vue | Reviewed common action delegation/destructive tone and resource labels. Conforms. |
| ResourceTableEditButton.vue | Reviewed common action delegation/neutral tone and resource labels. Conforms. |
| ResourceTableFilter.vue | Fixed searchable Teleport Tab to close and focus source anchor before native navigation; added cached-route deactivation close. Reviewed selected value label, empty/search options, active descendant, wrapping and viewport bounds. |
| StatusBadge.vue | Reviewed textual lifecycle state, native square semantic mono badge, disabled/stale statuses and decorative status dot. Conforms. |
| Tabs.vue | Reviewed ordinary route/section nav semantics, native button/current-page state, disabled/visible labels and count chip, mobile horizontal rail. Existing contract retained. |
| ToastHost.vue | Reviewed priority, persistent/action failures, hover/focus/document visibility timer pause, shared primary/fallback handoff, keyboard dismiss and separate mounted alert/status channels. Existing canonical contract retained. |

Additional coordinated AgentKit component: `ModelIDSelector.vue` had the same searched Teleport Tab/deactivation gap. Added source-order Tab return and own deactivation close. Its grouped/manual model option behavior remains unchanged.

Canonical support coverage: confirm.ts, layoutPreference.ts, table.ts, toast.ts, useAnchoredPopover.ts and useDelayedLoading.ts; framework-neutral form-select.ts, resource-table-filter.ts, modal.ts, icons.ts, tabs.ts, dashboardtile.ts and toast.ts; style injection, page-state and navigation helpers; full railgrid-ui.css. Reviewed native/vanilla disabled, keyboard, focus, document host and recipe ownership paths. The plain toast renderer is the explicitly documented Agents/Quickstart compatibility exception, not the Vue toast contract. Toggle geometry remains compliant in the enabled App Studio consumer because a 44×44 switch button owns the decorative span's hit target.

Core CSS findings reported and root coordinates fixes: display-page title face; readable dashboard secondary foreground; coarse table targets must also apply to any-pointer coarse; searchable filter input/options need 44px minimum. All other visual recipe blocks reviewed for semantic tokens, documented geometry, recipe ownership, font roles, bounded mobile surfaces, light/dark selectors, focus and reduced motion.

## Validation

- `make build-edges-provider-portal`: final pass 186 tests across 13 test files, vue-tsc and Vite production build, including actual host caller refinement and root-shell credential snapshot regression.
- `make build-code-provider-portal`: final pass 148 tests across 16 files, vue-tsc and Vite production build, including actual host caller/view generation refinement and create-form credential renewal recovery.
- New provider mounted regressions cover populated/empty dashboard snapshot preservation through failures, credential renewal and identity change stale-result fences; recoverable discovery preserving service drafts; Code route view identity vs mutation fencing.
- Canonical table/selection/overflow/ActionMenu core regressions final pass 24 tests, including actual Vue-mounted KeepAlive production setup state and model Tab regressions.
- `git diff --check` passed for final assigned local and coordinated core sources; broad source/browser gates belong to root after sync.

No commits, SDK vendored hand-edits, cluster object mutations, or destructive resources were created by this subagent. Root agent supplies enabled-provider catalog and live browser matrix evidence separately.


## Live capture review

Reviewed all 60 completed Code/Edges captures in `/tmp/railgrid-ui-audit/live-first/results.json`: 15 routes in each of light/dark at 1440px and 390px. No provider capture failed, reported an unnamed control, produced an uncaught runtime error, or leaked horizontal overflow outside a bounded ancestor. Code routes include connection lists/detail, repository list, packages, manual-token creation and repository creation. Edges routes include collection, services, workloads, connect wizard, service creation, manual workload creation, server/cluster details and service detail.

Visual review covered the saved Edges index/connect-wizard screenshots in both themes and sizes. The new create header, guided form card, help content and spanning action footer remain consistent at narrow width; the resource table remains inside its scroll surface. The sweep intentionally did not save Code route screenshots, so their live evidence is DOM geometry, control naming and runtime output, plus the exhaustive source review above.

The only mobile geometry flags are native radio/checkbox glyphs. RepositoryCreate README, ServiceCreate/ServiceEdit target selection and HarnessCard enable controls use associated full-row `.k-checkbox-hit` labels, with canonical 44px coarse/hybrid pointer minima. Wizard type radios are enclosed by padded `.type` label cards. The second-round runtime measurement below confirms every associated label exceeds the touch minimum; these are glyph-box heuristic exceptions and the visible glyph should not be enlarged.


## Targeted mobile confirmation

The authorized second browser round completed all 10 cases (5 targets × light/dark at 390px), recorded in `/tmp/railgrid-ui-audit/live-edges-code-confirmation.json`. All associated label hit areas meet or exceed 44×44 CSS pixels in both themes: README checkbox 264×44; wizard cards 264×69 / 264×91 / 264×69; service-create targets 93×44 / 143×44; service-edit targets 66×44 / 128×44; harness switch 268×81. Screenshots are saved beside that JSON and were visually checked for Code repository creation and Edges service creation.

The wizard's native keyboard behavior passed in both themes: ArrowDown moves selected state and focus from Kubernetes to Linux to MacOS; Space selects the focused Kubernetes radio. No form submit occurred, no provider mutation request was issued or blocked, and no runtime error occurred. The harness switch was only measured because its production change handler writes immediately. No additional source fixes were necessary.
