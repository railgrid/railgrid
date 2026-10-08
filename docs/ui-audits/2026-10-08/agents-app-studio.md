# Agents and App Studio provider UI audit

Durable browser measurements and check results are in [browser evidence](browser-evidence.json). References below to `/tmp/railgrid-ui-audit/` are supplemental machine-local logs and captures.

Final Models collection captures are [light](models-light.png) and [dark](models-dark.png).

Scope: every provider-owned Vue component under `providers/agents/portal/src` and `providers/app-studio/portal/src`, their CSS, and the supporting presentation/read/focus modules. Canonical PortalKit/AgentKit and their synchronized copies are audited and maintained by the root agent, so they are not duplicated here. Inventory: **27 Agents Vue components, 34 App Studio Vue components, 83 supporting TS/CSS sources**. Test fixtures are validation, not shipped components.

Authority: `docs/design/README.md`, its normative foundations (geometry, color, typography, accessibility, interaction), the model-connections and resource-section-card component contracts, resource reads/creation/controls patterns, Agents autonomy/runs and App Studio conversation patterns, and the quality review checklist/known oddities. The Impeccable skill was used with Railgrid as the pinned visual authority.

Evidence boundary: the component table below is a **static source audit**, including template structure, shared primitive use, tokens, field names/descriptions, state branches, keyboard/focus behavior and responsive rules. Existing component/runtime regression suites and production builds supply additional evidence. The root agent owns live Tilt/browser coverage. This table does not claim every data-dependent branch was exercised against real provider resources: approval requests, active streams, runner credentials, chart output, preview runtimes, deployment convergence, consent grants and invited members need their corresponding resource states. The persisted workbenches and mutation handlers were preserved.

## Concrete fixes

- Shared model cards: both providers already use the canonical `ModelConnectionCard` and `k-model-grid`. Agents' models wrapper already has an explicit padding reset, so no extra local wrapper was introduced. The screenshot's blended model-card surface is a **shared AgentKit finding**, reported to the root agent and fixed there with the raised-surface token.
- App Studio approval/response mode menus were teleported to `body`, outside its scoped Tailwind CSS, inherited color variables and semantic overlay layers. Both now target the existing provider-owned `#app-studio-overlay-root`, matching its other overlays. Their keyboard, outside-dismissal and focus-return handlers remain intact.
- App Studio attachment Add menus now use canonical `k-menu` and `k-menu-item` chrome. They retain their native menu semantics, existing dismissal/keyboard controller and coarse-pointer hit-target marker. Their container corners now match 6px menus and item corners match 4px controls.
- App Studio image/text attachment preview surfaces, model/production loading surfaces, production nested fieldsets, creation/prerequisite panels, warning/error/status surfaces, preview frame/empty surfaces, annotation tooltip and workbench loading panels now use the 6px display-panel radius. Attachment retry/remove controls and file-tree retry controls retain the 4px control radius. Skeletons representing controls and small chips retain their intended geometry.
- Clarification choices in both App Studio interrupt locations now announce selection with `aria-pressed` and share the 4px control radius. The checkpoint action now opts into the provider's 44px coarse/hybrid-pointer target rule, with its icon explicitly decorative.
- App Studio's new-project fields now use the normal 12px sentence-case field-label treatment; Project name and Template expose short accessible names while their existing detailed review copy remains `aria-describedby` help. File-path validation is associated with its input. A stale file-tree refresh and stale provider-catalog warning use polite status semantics while keeping the loaded content.
- App Studio's dashboard tile now announces initial loading, supplies Retry for an initial read failure, and uses the installed Lucide chevron at the Railgrid stroke width with decorative semantics. Its caller-scope guard and snapshot retention remain intact.
- Agents connection creation/editing, agent creation, assisted search, automation and harness credentials now separate native field names from nested helper/error text. Connection hints and Cron/channel help are associated explicitly. Budget field names remain short when errors appear.
- Agents chart canvases now expose `role="img"` with their existing chart title; the exact-data table, download and bounded fallbacks remain available. Decorative harness save/loading icons are hidden from assistive technology, and the missing-model warning in Agent chat announces as status.
- Root's live Agents Models capture found default 24px Lucide glyphs inflating micro-badges to about 31px tall. The provider's direct chip glyphs now use the normative 12px size, flex-none sizing and stroke width 2. Nested remove-action glyphs keep their canonical control styling; the source rule targets direct badge glyphs only. This visual finding came from root's live screenshot `/tmp/railgrid-ui-audit/live-first/agents-models-light-1440.png`.
- Both Models collection page headings now use canonical `k-resource-page__title` (18px Archivo Variable at 125% width). Agents applies it only to its route-owned collection and App Studio retains its existing `routePage` condition, so embedded model presentations do not gain an extra page heading.
- Final authority review found that Agents dashboard `authorityKey` treated every token change as a new caller and cleared the tile. Host-owned fetch now makes a renewed bearer irrelevant to caller/workspace identity. A runtime custom-element regression verifies the snapshot stays visible while renewal refresh is pending and after it fails; additional caller/workspace transitions still clear it. Legacy bearer-only hosts retain their authority boundary.
- App Studio's context watcher also called its full invalidation path on token renewal (disconnecting streams and clearing project/conversation/draft state). Its fingerprint now preserves host-managed renewal while caller/workspace/tenant transitions and legacy bearer changes still invalidate. Three tests execute the actual fingerprint and watcher with Vue reactivity to verify these boundaries.
- Root's model fixture comparison revealed another model-connections contract failure: changing App Studio's custom endpoint retained its typed replacement key. `updateLLMBaseURL` now clears that key only for an actual URL change, clears local validation, invalidates the verification generation/result synchronously, and clears discovery. Model/name drafts stay intact. A regression executes the actual setter and invalidation handlers, covering unchanged URL retention and changed URL credential clearance.
- App Studio model-read Retry actions now bind the existing loading state, announce `Retrying models…`, disable competing retries, and retain the prior initial/stale error while the read runs. The initial loading skeleton no longer replaces a pending initial-error retry. Tests exercise the real model-read function and render both initial and stale busy-retry states.
- Agents cards use a width-bounded minimum track (`min(100%, 20rem)`) so an embedded narrow surface does not overflow before the viewport media rule applies. The one-time secret handoff wraps safely. Usage KPI values now use Archivo Variable at 125% width and 26px, with tabular numeric figures; bar values are tabular too.

## Agents: complete local component coverage

All paths below are relative to `providers/agents/portal/src/`.

| Component | Reviewed presentation and states | Result |
| --- | --- | --- |
| `App.vue` | Provider shell, primary tabs, route-owned creation/detail transitions, shared confirmation host, scoped context | Shared primitives and route/focus ownership retained |
| `components/AgentToolDetails.vue` | Execution details adapter, safe metadata and bounded diagnostic output | Uses canonical AgentKit execution disclosure; retained |
| `components/AgentVisualization.vue` | Vega canvas, title, parse/render error, download, table disclosure and missing-data fallback | Fixed named image semantics; retained accessible data alternative |
| `components/ApprovalDisclosure.vue` | Approval argument summary, sanitized values, invalid-disclosure alert and technical detail disclosure | Retained bounded disclosure and explicit unavailable state |
| `components/ConfigSaveFeedback.vue` | Idle/pending/saved/newer-edits/error notifications | Retained section-owned feedback and semantic announcements |
| `components/HarnessCredentialForm.vue` | Identity, provider, credential-type radio choices, secret validation, locked/saving states | Fixed short names and decorative icons; retained shared model-form sections and secret privacy |
| `components/RunFailureNotice.vue` | User-facing failure summary, recovery controls, optional diagnostic disclosure | Retained safe default copy and closed technical details |
| `components/SecretHandoff.vue` | Masked one-time secret, reveal-independent copy/dismiss, copy error/status | Fixed supporting wrapper so long values/actions wrap |
| `components/TechnicalDetails.vue` | Safe bounded technical disclosure with closed initial state | Retained canonical technical-detail presentation |
| `views/Activity.vue` | Filters/search/pagination, run table, approval cards, initial and stale read states | Retained ResourceTable and snapshot-preserving recovery |
| `views/AgentChat.vue` | Conversation header/rail/transcript/composer, mobile rail dismissal, sessions, streaming, orphan/read errors | Fixed missing-model status announcement and rail-control 4px geometry; retained canonical AI layout and session state |
| `views/AgentConfig.vue` | Persona/backend/model, fallback controls, autonomy/budgets/limits, tools, channels, delegates and per-section save feedback | Fixed budget names; shared ResourceSectionCard sections, constraints and save ownership retained |
| `views/AgentCreate.vue` | Guided creation, prerequisite snapshots, harness/model selection, linked helpers/errors, draft and busy states | Fixed native Name accessible name; shared creation surface retained |
| `views/AgentDetail.vue` | Workbench root, agent metadata, global actions, unavailable/error states and destructive action card | Retained AIWorkspace and section-owned controls |
| `views/AgentWorkbench.vue` | Five kept-alive, labelled tab panels and selected-panel visibility | Retained canonical AIWorkspace structure and persisted panel state |
| `views/AgentWorkbenchHeader.vue` | Tab/close actions, arrow/Home/End navigation, keyboard and drag reorder, focus restoration | Retained canonical AIWorkbenchTabs and interaction handling |
| `views/AgentWorkbenchLauncher.vue` | Searchable existing/suggested views and launcher selection | Retained canonical launcher adapter |
| `views/AgentsList.vue` | First-run guide, initial loading/error, stale snapshot, collection cards, own detail links/actions | Supporting grid fixed for narrow embeds; retained shared first-run/read states |
| `views/AssistedSearch.vue` | Optional infrastructure prerequisite states, guided connection/instance form, validation/busy and create guidance | Fixed short field names; retained existing described helpers |
| `views/Automation.vue` | Schedule/trigger tables, create/edit detail shell, identity/cron/channel controls, errors, run-now/pause/delete actions | Fixed Name/Cron and helper association; shared table/creation/confirmation retained |
| `views/ChatMessage.vue` | User/assistant turns, markdown, activity/progress, approval/follow-up, chart output, execution details and source receipts | Retained AgentKit message/activity/interrupt presentation and safe content projection |
| `views/Connections.vue` | Type/category picker, dynamic create/auth-mode/advanced fields, edit and credential rotation, resource table, OAuth/inbound/test actions | Fixed names/hints for native fields, auth-control 4px geometry and disclosure 44px touch targets; retained shared primitives and privacy behavior |
| `views/DashboardTile.vue` | Scoped polling, count/approval state, recent rows, initial failure/retry, stale snapshot and host-managed credential renewal | Fixed renewal snapshot retention with runtime regression coverage; caller/workspace and legacy authority boundaries retained |
| `views/ModelConnectionEditor.vue` | Canonical model form adapter, discovery/probe feedback and harness credential variant | Retained canonical form and test/format states; harness fixes owned in its child |
| `views/Models.vue` | Model collection/card action scope, empty/error/stale state, edit/create and usage panels | Already canonical independent cards; fixed route-owned title role, badge glyph sizing, usage-control 4px geometry and usage KPI typography; shared raised-card fix owned by root |
| `views/RunDetail.vue` | Owned detail read/error state, header metadata, safe failure/recovery, approval, steps/output/sources and child-run table | Retained ResourcePage/ResourceSectionCard and run-evidence hierarchy |
| `views/Toolsets.vue` | Guided create/edit, optional connection dependencies, tool assignments, snapshot/error recovery and resource table | Retained shared creation/table/notification/checkbox primitives |

Supporting Agents sources inventoried and scanned for presentation, state or provider integration contracts (not separate visual components):

`activity.css`, `agent-create-draft.ts`, `agent-workbench.ts`, `api.ts`, `approval-disclosure.ts`, `assisted-setup.ts`, `budget-validation.ts`, `conn-defs.ts`, `element.ts`, `failure-presentation.ts`, `main.ts`, `model-probe-result.ts`, `mutate.ts`, `request-errors.ts`, `resources.ts`, `router.ts`, `store.ts`, `style.css`, `types.ts`, `ui/toast.ts`, `usage.ts`, `visualization.ts`, `vue/chat.ts`, `vue/config-save-queue.ts`, `vue/runtime.ts`.

## App Studio: complete local component coverage

All paths below are relative to `providers/app-studio/portal/src/`.

| Component | Reviewed presentation and states | Result |
| --- | --- | --- |
| `App.vue` | Project index/cards and filtering, import/create and setup prerequisites; conversation identity/rail/history/transcript/attachments/interrupts/composer; workbench launcher, resizing/tabs/mobile sheets, preview annotations, code/skills/review/providers/integrations/embedded provider panes; settings/models, production pipeline/deployment review/access/configuration/history | Fixed panel geometry, selected clarification semantics, stale catalog announcement, host-managed renewal snapshot retention, model-retry retention and endpoint credential invalidation; caller/workspace guards and shared AI primitives retained |
| `ApprovalModePicker.vue` | Trigger, selected choice, busy/disabled, responsive anchored menu and keyboard focus | Fixed teleport destination so menu keeps provider styling/layers |
| `AssistantActionLog.vue` | Bounded grouped activity, live/terminal status, failures/recovery, execution expansion | Retained canonical action row/disclosure presentation |
| `AssistantAttachmentPreview.vue` | Blob lifecycle, image/error preview, upload/delete progress, retry/remove actions and hit targets | Fixed 6px surface and 4px controls |
| `AssistantAttachmentTextPreview.vue` | Bounded escaped text, file read/error/loading states | Fixed display-preview radius |
| `AssistantCommandPalette.vue` | Slash/resource/skill/menu modes, search/combobox/listbox, initial/loading/partial failure, selected/limited states and mobile dismissal | Retained keyboard/focus ownership and responsive surface |
| `AssistantExecDetails.vue` | Approval/completed execution adapter and bounded output | Retained canonical AgentKit execution presentation |
| `AssistantMessageAnnotations.vue` | Annotation chips, disclosure, hover pin, keyboard Escape/Tab, focus return, unresolved/current-document states | Retained accessible scoped annotation disclosure and 6px popup |
| `AssistantMessageAttachments.vue` | Receipt-scoped image loading/error and compact text/file attachments, abort/revoke lifecycle | Fixed image display-panel radius |
| `AssistantMessageQueue.vue` | Queue enable control, rows/action menus, steering/removal, inline edit and keyboard completion/cancel | Retained shared action menus, native controls and bounded queue display |
| `AssistantPlanDisclosure.vue` | Collapsed plan history adapter | Retained canonical AgentKit plan disclosure |
| `AssistantPlanPopover.vue` | Live plan capsule, explicit disclosure, responsive mobile sheet, inert/focus handling, escape and desktop release | Retained existing provider-overlay target and plan hierarchy |
| `AssistantPlanSteps.vue` | Ordered status checklist adapter | Retained canonical AgentKit list and status text |
| `AssistantPreProjectComposer.vue` | Intake/attachment-only variants, image/text attachment actions, paste/file fallback, Add menu, editor/errors and slots | Fixed canonical menu chrome and attachment control geometry |
| `AssistantRichComposer.vue` | Contenteditable editor/IME/plain paste, atomic resource/skill tokens, file drag/paste/picker fallback, staged receipts/annotations, command palette and controls | Fixed canonical Add menu chrome and attachment control geometry; existing editor semantics and submission intents retained |
| `CodeExplorer.vue` | Responsive tree/editor panes, native keyboard tree, file/image/text states, initial/stale errors, refresh, new-file validation, file upload and drag fallback | Fixed stale warning semantics, file-error association, preview radius and retry control geometry |
| `DashboardTile.vue` | Scoped snapshot polling, recent project rows, environment readiness counts, initial load/failure and stale warning | Fixed loading announcement, initial Retry and canonical decorative icon |
| `DevelopmentPreviewToolbar.vue` | Status, annotate/sync/open controls, disabled/busy, container-aware action overflow and keyboard handling | Retained named compact controls and responsive action ordering |
| `FirstTimeSetup.vue` | Model/Git readiness guide and setup actions | Retained shared FirstRunGuide |
| `GitConnectionSettings.vue` | Repository connection, permission/empty/loading/error/retry states and change actions | Retained existing 6px section and native shared controls |
| `GitRecommendationBanner.vue` | Contextual recommendation, dismiss/action and busy state | Retained shared inline notification |
| `ModelIDSelector.vue` | Model identity selector re-export | Retained canonical AgentKit selector |
| `ModelPicker.vue` | Selected model trigger, native combobox/listbox, grouped entries, keyboard and dismissal | Retained model selector semantics and provider-local identity context |
| `ModelsSettings.vue` | Route/embedded model collection, shared independent cards and form, probe/discovery feedback, empty/initial/stale read, usage | Fixed loading panel radius, route-page title role and busy retry/error retention; canonical card surface fixed by root |
| `NewProjectWizard.vue` | Describe/prepare/confirm steps, one stable shell, attachment errors, skeleton and retry, name/template review, prerequisite recovery and submission | Fixed field typography/names and error/prerequisite panel geometry |
| `ProductionForm.vue` | Recursive object/map forms, typed native values, immutable/help/error IDs and unsupported schema states | Fixed nested fieldset and status-panel radius; preserved field validity and recursion |
| `ProductionSettingsLoadingShell.vue` | Matching production settings skeleton and loading announcement | Fixed panel radius |
| `ProjectHistory.vue` | Repository/unavailable/empty/error/retry, commit selection keyboard group, restore consequences/busy and status | Retained stable history structure, links outside select rows and named restore controls |
| `ProjectIntegrations.vue` | Discovery/load/partial state, resource list/disclosures, versioned grant state, digest changes, approval review and native consent checkbox | Retained shared notifications/badges and exact-resource consent hierarchy |
| `ProjectShareDialog.vue` | Labelled modal/focus trap and dismissal, production/preview cards, copied links, exclusive audience controls, members/email grants, draft/busy/unavailable/errors and explicit save consequences | Fixed warning/error/status panel geometry; separate channel save ownership retained |
| `ReleasePipeline.vue` | Ordered build/image/release/rollout state, attention/long-running indicators and refresh/retry | Retained readable status text and shared badges |
| `ResponseModePicker.vue` | Current response mode, choice menu, keyboard and responsive anchored placement | Fixed teleport destination so menu keeps provider styling/layers |
| `SkillsWorkbench.vue` | Searchable skill/source inventory, selected detail/editor, retry/warnings, responsive container layout and native dialog/focus lifecycle | Retained 6px panels, named controls and existing provider-overlay target |
| `components/CheckpointChip.vue` | Native checkpoint action, status-specific icon/copy, disabled/title | Fixed coarse-pointer hit target and decorative icon |

Supporting App Studio sources inventoried and scanned for presentation, state or provider integration contracts (not separate visual components):

`api.ts`, `assistantActionFeed.ts`, `assistantAnnotationDraft.ts`, `assistantAttachments.ts`, `assistantCommandPalette.ts`, `assistantExecDisclosure.ts`, `assistantHistoryFocus.ts`, `assistantInterrupt.ts`, `assistantMessageQueue.ts`, `assistantPlan.ts`, `assistantProgress.ts`, `assistantResources.ts`, `assistantThreadFocus.ts`, `assistantThreadPinState.ts`, `assistantThreadProjection.ts`, `assistantThreadReadState.ts`, `assistantTrace.ts`, `assistantVerification.ts`, `clipboard.ts`, `composables/useEscapeKey.ts`, `conversationResilience.ts`, `createReadiness.ts`, `element.ts`, `lazyLoaderRegistry.ts`, `llmDiscovery.ts`, `llmRegistry.ts`, `llmSettingsValidation.ts`, `main.ts`, `modelIDSelection.ts`, `page-element.ts`, `previewBridge.ts`, `previewRefresh.ts`, `previewState.ts`, `previewToolbarLayout.ts`, `productionForm.ts`, `productionPane.ts`, `projectDeletion.ts`, `projectFiles.ts`, `projectIntegrations.ts`, `promotionState.ts`, `publishingState.ts`, `releaseSelection.ts`, `sharePreviewAccess.ts`, `skillsSearch.ts`, `sourceHistory.ts`, `style.css`, `styles.ts`, `tile-element.ts`, `toastPolicy.ts`, `types.ts`, `useAssistantFilePickerFocus.ts`, `useDismissibleAddMenu.ts`, `useGitOnboarding.ts`, `useModePickerMenu.ts`, `useProductionSettings.ts`, `useProjectCreationSubmit.ts`, `workbench.ts`, `workbenchPersistence.ts`.

## Validation

- Final `make build-agents-provider-portal` passed **37 test files / 646 tests**, `vue-tsc --noEmit`, and the production Vite build after all provider-local edits. This includes the existing route-focus regression updated to select the canonical page-title element, and meaningful dashboard authority-renewal/reset tests. Final log: `/tmp/railgrid-ui-audit/agents-control-final-build.log`.
- App Studio's final `npm run typecheck` passed after the settled credential-boundary fix (`/tmp/railgrid-ui-audit/app-studio-ultimate-typecheck.log`). Its final production build and unchanged bundle-budget check passed (`/tmp/railgrid-ui-audit/app-studio-credential-final-build.log`). Sizes: bootstrap **5,437 raw / 2,231 gzip**, dashboard **287,559 raw / 90,807 gzip**, page **1,220,059 raw / 339,587 gzip** below **1,221,000 / 340,000**, and total **1,295,085 raw / 364,863 gzip** below **1,296,000 / 365,000**. No budget limit was edited by this subtask.
- App Studio's full `npm test` exercised **70 test files**: 69 passed and `assistantRichComposer.test.mjs` failed on an existing literal class-prefix assertion. The canonical menu items were corrected to retain the expected `app-studio-touch-target flex w-full` prefix; the unchanged focused suite then passed **15/15** (`/tmp/railgrid-ui-audit/app-studio-composer-tests.log`). Every file has a passing run, while the full runner's initial exit was 1 and was resolved by this targeted rerun rather than a second full run.
- Meaningful App Studio conversation/context and model-settings regressions passed **81/81** (`/tmp/railgrid-ui-audit/app-studio-final-regressions.log`); after the endpoint invalidation fix the model-settings suite passed **23/23** (`/tmp/railgrid-ui-audit/app-studio-credential-boundary-tests.log`). These execute the actual context fingerprint/watcher and endpoint setter, verify token renewal retention and caller/tenant reset, and prevent a typed replacement key following a changed endpoint. Agents' focused dashboard runtime suite passed **9/9** (`/tmp/railgrid-ui-audit/agents-tile-rotation-tests.log`), also covered by its final full runner.
- `git diff --check` passed for both provider-local source scopes and the design hook configuration. No Go code changed in this subtask; Go checks are not applicable.

## Design hook triage

A `broken-image` hook finding at `CodeExplorer.vue:688` was a false positive on a script comment mentioning an image tag. The actual rendered image requires `preview.status === 'ready'` and matching path, and receives a blob URL only after `api.fetchProjectFileRaw` succeeds. Its loading/error fallback and URL revocation were inspected. Per the skill's instructions, the narrow `broken-image=*` ignore was persisted **only for this file** in `.impeccable/config.json`; no global rule or whole-file ignore was added. No substantive finding was left unresolved by that suppression.

## Final live corrections

The live title measurement found that `.agents-panel h3` reduced the route-owned Models title to 15px despite its canonical page-title class. `Models.vue` now renders an `h2` for a route-owned page and preserves the embedded `h3`, allowing the shared 18px Archivo/125% recipe to apply. The existing route-focus assertion selects `.k-resource-page__title` instead of assuming a heading tag.

Fresh coarse-pointer measurements found the connection form's Advanced disclosure was 29px. Native Advanced and setup summaries now provide 44px touch targets with their native markers retained. Explicit provider-local control overrides were aligned to 4px: authentication mode buttons, usage-window controls, conversation rail toggles, and the retained discovered-model filter rule. Badges remain 3px and usage micro-bars keep their appropriate 2px geometry.

## Final targeted live confirmation

Final Playwright evidence contains **59 valid records: 51 rendered states and 8 model-card check records**, all without page errors, horizontal overflow, unnamed native controls, failed check booleans, or remaining measured undersized touch targets. Tests used the actual authenticated development workspace, both light/dark themes, 1440px desktop and 390px touch viewports. Agents readiness and registration integrity were checked against the final local `dist/main.js` SHA384 before this last proof; no SRI or readiness check was bypassed.

| Live coverage | Valid states/checks | Confirmed behavior |
| --- | --- | --- |
| Agents + App Studio model collections | 8 states + 8 check records | Independent raised cards (`#fff` / `#111320`), 6px radius, 360px desktop width within the 280–360px recipe, fitting the mobile content width; 18px Archivo/125% route titles; Agents badge glyphs 12px/stroke2; final Agents usage controls 4px |
| Both model-create selectors | 8 states | Manual-ID search option appears; Tab closes the popup and advances to the next source-order control (Cancel); Escape closes and returns focus to the trigger |
| App Studio project Response/Approval menus | 4 states / 8 opened menus | Menus render inside `#app-studio-overlay-root`, inherit the raised theme surface and 6px frame, and Escape closes with focus returned to the originating trigger |
| App Studio workbench launcher | 4 states | Search input 4px radius, 36px desktop and 44px touch height |
| Agents index/activity/connections dark desktop | 3 states | Previously interrupted routes render successfully with no page errors, unnamed controls or horizontal overflow |
| Actual Agents creation flows | 24 states | Agent form, connection picker, GitHub access-token form, GitHub OAuth form, MCP setup form and toolset form, each in both themes/viewports; native setup/Advanced disclosures and all applicable connection controls have 44px touch targets; auth controls have 4px radius |

The actual Agents creation forms were reached through their collection buttons, because direct host `create/*` subPaths do not select the provider's hash-backed creation route. The original broad sweep's direct-create cases therefore only observed the default collection; these final navigation-driven cases provide the creation-form evidence. This audit did not broaden the route parser. All interactions were read-only or draft-only: no Test, Create, connection authorization, project mode selection, or resource-save action was executed.

An isolated browser probe found that a tall **element** screenshot reset Chromium touch emulation (`maxTouchPoints` 1→0 and coarse media true→false), making subsequent same-page button measurements appear to use desktop geometry. The final focused connection proof uses viewport screenshots and explicit CDP touch enforcement. All **six mobile connection branch states** record coarse/any-coarse true and `maxTouchPoints: 1`, with successful 44px disclosure/control checks. Earlier routing-locator, rebuild, tile-label and screenshot-emulation attempts are archived separately and are excluded from final valid cases.

Artifacts:

- Final measurements: `/tmp/railgrid-ui-audit/live-ai-confirmation/results.json`; compact counts: `summary.json` in the same directory.
- Attempt archive: `/tmp/railgrid-ui-audit/live-ai-confirmation/attempts.json` (with the retained `routing-attempt.json` and `touch-artifact-attempt.json` originals).
- Final provider-root Models captures: `docs/ui-audits/2026-10-08/models-light.png` and `models-dark.png`.
- Final focused geometry script/log: `/tmp/railgrid-ui-audit/live-agents-connection-final.mjs` and `.log`; prior App Studio/menu/selector evidence is preserved in the combined results.

This live evidence supplements the exhaustive static component/source inventory. It does not claim runtime coverage of every resource-dependent conversation, agent workbench, approval, deployment, attachment failure, provider outage or other conditional state absent from the current workspace; those paths were inspected in source and relevant existing regression suites, with fixture/conditional checks coordinated by the root audit.
