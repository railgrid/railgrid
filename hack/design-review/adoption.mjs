// Observed consumers, not a complete adoption census. Each locator must resolve
// to current source when the catalog is built; update it if the use moves.
export const adoption = {
  'components.link': [
    ['Edges · service identity', 'providers/edges/portal/src/Services.vue', 'function openServiceLink'],
    ['Edges · edge identity', 'providers/edges/portal/src/EdgeCollection.vue', 'function openEdgeLink'],
    ['Infrastructure · template identity', 'providers/infrastructure/portal/src/views/CatalogPage.vue', 'function followTemplateLink'],
    ['Agents · webhook identity', 'providers/agents/portal/src/views/Webhooks.vue', 'function followLink'],
    ['Agents · agent identity', 'providers/agents/portal/src/views/AgentsList.vue', 'tab: \'chat\' })">{{ row.name }}</a>'],
    ['Agents · trigger webhook links', 'providers/agents/portal/src/views/Automation.vue', 'function followWebhookLink'],
    ['Code · repository identity', 'providers/code/portal/src/views/RepositoriesView.vue', 'function openRepositoryLink'],
    ['Code · connection identity', 'providers/code/portal/src/views/ConnectionsView.vue', 'function openConnectionLink'],
    ['Code · package repository', 'providers/code/portal/src/views/PackagesView.vue', 'function openRepositoryLink'],
    ['Code · package host', 'providers/code/portal/src/views/PackagesView.vue', 'class="k-table-resource-link"'],
  ],
  'components.digest-schedule': [
    ['Agents · news reporter creation', 'providers/agents/portal/src/views/AgentCreate.vue', '<AgentDigestSchedule'],
  ],
  'components.terminal-dock': [
    ['Portal · persistent terminal dock', 'portal/src/App.vue', '<TerminalDock'],
    ['Edges · machine detail', 'providers/edges/portal/src/Detail.vue', 'function openTerminal()'],
  ],
  'components.dashboard-tile': [
    ['Agents · dashboard summary', 'providers/agents/portal/src/views/DashboardTile.vue', 'const hasSnapshot = ref(false)'],
    ['Edges · dashboard summary', 'providers/edges/portal/src/DashboardTile.vue', 'tileClass'],
    ['Code · dashboard summary', 'providers/code/portal/src/DashboardTile.vue', 'tileClass'],
    ['Infrastructure · dashboard summary', 'providers/infrastructure/portal/src/DashboardTile.vue', 'tileClass'],
    ['App Studio · dashboard summary', 'providers/app-studio/portal/src/DashboardTile.vue', 'tileClass'],
    ['Quickstart · dashboard summary', 'providers/quickstart/portal/src/tile.ts', 'class QuickstartDashboardTileElement'],
    ['Kuery · dashboard summary', 'providers/kuery/portal/src/dashboard-tile.ts', 'class KueryDashboardTile'],
  ],
  'foundations.colors': [
    ['Edges · service table surface', 'providers/edges/portal/src/Services.vue', '<ResourceTable'],
    ['Edges · service action menu', 'providers/edges/portal/src/Services.vue', '<ActionMenu :label="`More actions for service'],
    ['Infrastructure · child resources section', 'providers/infrastructure/portal/src/views/InstanceDetailPage.vue', '<ResourceSectionCard id="instance-children"'],
  ],
  'foundations.typography': [
    ['Agents · collection heading', 'providers/agents/portal/src/views/Connections.vue', '<ResourcePage :title="COLLECTION_TITLE" variant="collection"'],
    ['Agents · run detail heading', 'providers/agents/portal/src/views/RunDetail.vue', '<ResourcePage'],
    ['Infrastructure · summary values', 'providers/infrastructure/portal/src/views/InstanceDetailPage.vue', '<ResourceStatCards :cards="statCards"'],
  ],
  'components.ai-conversation': [
    ['Agents · conversation workspace', 'providers/agents/portal/src/views/AgentChat.vue', '<AIConversationLayout'],
    ['App Studio · project conversation', 'providers/app-studio/portal/src/App.vue', '<AIConversationLayout'],
  ],
  'components.dashboard-usage': [
    ['Dashboard · usage tile', 'portal/src/components/DashboardUsageTile.vue', '<MeteringUsageList class="mt-4"'],
    ['Admin · organization usage', 'portal/src/pages/bonkers/MeteringSection.vue', '<MeteringUsageList v-if="entitlementFor(selectedOrg.name)"'],
  ],
  'components.portalkit-support': [
    ['Agents · scoped provider requests', 'providers/agents/portal/src/api.ts', 'return providerFetch(this.ctx)(input, init)'],
    ['Portal · page context normalization', 'portal/src/stores/pageResource.ts', 'normalizeProviderAgentContext(value)'],
  ],
  'components.provider-agent-context': [
    ['Portal · provider publication boundary', 'portal/src/pages/ProviderFrame.vue', 'publishAgentContext: createAgentContextPublisher'],
    ['Portal · page context storage', 'portal/src/stores/pageResource.ts', 'function publishAgentContext(source: object'],
  ],
  'components.progress': [
    ['Organization settings · workspace deletion requests', 'portal/src/components/WorkspaceDeletionProgress.vue', 'aria-label="Workspace deletion requests finished"'],
  ],
  'components.model-connections': [
    ['Agents · provider editor', 'providers/agents/portal/src/views/ModelConnectionEditor.vue', '<ModelConnectionForm'],
    ['App Studio · model collection', 'providers/app-studio/portal/src/ModelsSettings.vue', '<ModelCollection v-if="modelRows.length"'],
  ],
  'components.resource-back-link': [
    ['Edges · edge detail', 'providers/edges/portal/src/Detail.vue', '<ResourceBackLink class="edge-detail__back"'],
    ['Infrastructure · instance detail', 'providers/infrastructure/portal/src/views/InstanceDetailPage.vue', '<ResourceBackLink'],
  ],
  'components.resource-table-actions': [
    ['Edges · service deletion', 'providers/edges/portal/src/Detail.vue', '<ResourceTableDeleteButton'],
    ['App Studio · model actions', 'providers/app-studio/portal/src/ModelsSettings.vue', '<ResourceTableEditButton :label="`Edit ${saved.name}`"'],
  ],
  'components.visual-primitives': [
    ['Portal · MCP create form', 'portal/src/pages/MCPPage.vue', 'placeholder="ops" class="k-input'],
    ['Portal · MCP form action', 'portal/src/pages/MCPPage.vue', 'class="k-btn k-btn--ghost k-back-action"'],
  ],
  'components.first-run-guide': [
    ['App Studio · empty models', 'providers/app-studio/portal/src/ModelsSettings.vue', '<FirstRunGuide v-else-if="showFirstModel"'],
    ['Agents · empty models with a provider', 'providers/agents/portal/src/views/Models.vue', '<FirstRunGuide v-if="modelView && showFirstModel"'],
    ['Edges · services setup', 'providers/edges/portal/src/Services.vue', '<FirstRunGuide'],
  ],
  'components.layout-selector': [
    ['Agents · collection layout', 'providers/agents/portal/src/views/AgentsList.vue', '<LayoutSelector v-model="layout"'],
    ['App Studio · project layout', 'providers/app-studio/portal/src/App.vue', '<LayoutSelector v-model="projectLayout"'],
  ],
  'components.create-guidance': [
    ['Agents · connection creation', 'providers/agents/portal/src/views/Connections.vue', '<CreateGuidance title="Before you create"'],
    ['Edges · service creation', 'providers/edges/portal/src/ServiceCreate.vue', '<CreateGuidance'],
  ],
  'components.resource-page': [
    ['Agents · run investigation', 'providers/agents/portal/src/views/RunDetail.vue', '<ResourcePage'],
    ['Infrastructure · instance detail', 'providers/infrastructure/portal/src/views/InstanceDetailPage.vue', '<ResourcePage'],
  ],
  'components.resource-section-card': [
    ['Edges · connectivity', 'providers/edges/portal/src/Detail.vue', '<ResourceSectionCard id="edge-connectivity"'],
    ['Infrastructure · child resources', 'providers/infrastructure/portal/src/views/InstanceDetailPage.vue', '<ResourceSectionCard id="instance-children"'],
  ],
  'components.loading-placeholder': [
    ['Edges · detail read shell', 'providers/edges/portal/src/Detail.vue', '<ResourcePage'],
    ['Agents · detail read shell', 'providers/agents/portal/src/views/AgentDetail.vue', '<ResourcePage'],
    ['Infrastructure · instance read shell', 'providers/infrastructure/portal/src/views/InstanceDetailPage.vue', '<ResourcePage'],
  ],
  'components.resource-stat-cards': [
    ['Edges · summary', 'providers/edges/portal/src/Detail.vue', '<ResourceStatCards :cards="edgeStatCards"'],
    ['Infrastructure · instance summary', 'providers/infrastructure/portal/src/views/InstanceDetailPage.vue', '<ResourceStatCards :cards="statCards"'],
  ],
  'components.conditions-panel': [
    ['Edges · diagnostics', 'providers/edges/portal/src/Detail.vue', '<ConditionsPanel'],
    ['Infrastructure · instance conditions', 'providers/infrastructure/portal/src/views/InstanceDetailPage.vue', '<ConditionsPanel'],
  ],
  'components.confirm-dialog': [
    ['Edges · delete service', 'providers/edges/portal/src/Services.vue', "confirmDialog({ title: 'Delete service'"],
    ['Agents · delete schedule or trigger', 'providers/agents/portal/src/views/Automation.vue', 'title: `Delete ${one}'],
  ],
  'components.toast': [
    ['Edges · service deletion', 'providers/edges/portal/src/Services.vue', "toast('info', `Service deletion requested"],
    ['Agents · queued run action', 'providers/agents/portal/src/views/Automation.vue', "toast('ok', `${name} queued."],
  ],
  'components.ai-execution-details': [
    ['App Studio · execution projection', 'providers/app-studio/portal/src/AssistantExecDetails.vue', '<AIExecutionDetails'],
    ['Agents · execution projection', 'providers/agents/portal/src/components/AgentToolDetails.vue', '<AIExecutionDetails'],
  ],
  'components.ai-interrupt': [
    ['App Studio · review request', 'providers/app-studio/portal/src/ReviewRequest.vue', '<AIInterrupt'],
  ],
  'components.ai-action-row': [
    ['Agents · run execution', 'providers/agents/portal/src/views/RunDetail.vue', '<AIActivityFeed'],
    ['Agents · chat activity', 'providers/agents/portal/src/views/ChatMessage.vue', '<AITranscriptActivity'],
    ['App Studio · action log', 'providers/app-studio/portal/src/AssistantActionLog.vue', '<AITranscriptActivity'],
  ],
  'components.ai-command-palette': [
    ['Agents · rich composer', 'providers/agents/portal/src/AgentRichComposer.vue', '<AIConversationComposer'],
    ['App Studio · command palette adapter', 'providers/app-studio/portal/src/AssistantCommandPalette.vue', '<AICommandPalette'],
  ],
  'components.model-reasoning-picker': [
    ['Agents · conversation model', 'providers/agents/portal/src/views/AgentChat.vue', '<AIModelReasoningPicker'],
    ['App Studio · conversation model', 'providers/app-studio/portal/src/App.vue', '<AIModelReasoningPicker'],
  ],
  'components.tokenized-list-input': [
    ['Agents · agent template parameters', 'providers/agents/portal/src/views/AgentCreate.vue', '<AgentTemplateListInput'],
  ],
  'components.skill-catalog': [
    ['Agents · skills library', 'providers/agents/portal/src/views/SkillsLibrary.vue', '<AISkillCatalogList'],
    ['App Studio · skills workbench', 'providers/app-studio/portal/src/SkillsWorkbench.vue', '<AISkillCatalogList'],
  ],
  'components.menu': [
    ['App Studio · approval policy menu', 'providers/app-studio/portal/src/ApprovalModePicker.vue', 'role="menuitemradio"'],
    ['Edges · service actions', 'providers/edges/portal/src/Services.vue', '<ActionMenu :label="`More actions for service'],
  ],
  'components.action-menu': [
    ['Edges · service actions', 'providers/edges/portal/src/Services.vue', '<ActionMenu :label="`More actions for service'],
    ['Infrastructure · instance actions', 'providers/infrastructure/portal/src/views/InstanceDetailPage.vue', '<ActionMenu'],
  ],
  'components.button': [
    ['Agents · connection form actions', 'providers/agents/portal/src/views/Connections.vue', 'agents-connection-actions'],
    ['Edges · service refresh', 'providers/edges/portal/src/Services.vue', 'aria-label="Refresh services"'],
    ['Infrastructure · instance actions', 'providers/infrastructure/portal/src/views/InstanceDetailPage.vue', 'Review conditions'],
  ],
  'components.text-input': [
    ['Agents · connection name', 'providers/agents/portal/src/views/Connections.vue', 'agents-connection-create-name-hint'],
    ['App Studio · provider connection', 'providers/app-studio/portal/src/ModelsSettings.vue', 'id="provider-connection-name"'],
    ['Infrastructure · deployment parameters', 'providers/infrastructure/portal/src/views/ProvisionPage.vue', '<DynamicForm ref="dynamicForm"'],
    ['Edges · service creation', 'providers/edges/portal/src/ServiceCreate.vue', 'id="service-create-port"'],
    ['Edges · workload creation', 'providers/edges/portal/src/WorkloadCreate.vue', 'id="workload-replicas-error"'],
  ],
  'components.field-group': [
    ['Agents · toolset choices', 'providers/agents/portal/src/views/Toolsets.vue', 'aria-describedby="toolset-tools-hint"'],
  ],
  'components.inline-notification': [
    ['App Studio · model loading failure', 'providers/app-studio/portal/src/ModelsSettings.vue', 'v-if="loadError" data-model-recovery="models"'],
    ['App Studio · connection failure', 'providers/app-studio/portal/src/ModelsSettings.vue', 'v-if="connectionError" tone="error"'],
    ['Agents · partial usage', 'providers/agents/portal/src/views/AgentUsage.vue', 'v-if="normalizedUsage.unavailableAgents.length"'],
    ['Edges · service deletion failure', 'providers/edges/portal/src/Services.vue', 'class="services-delete-error"'],
  ],
  'components.resource-table': [
    ['Agents · toolsets', 'providers/agents/portal/src/views/Toolsets.vue', '<ResourceTable v-else'],
    ['Edges · services', 'providers/edges/portal/src/Services.vue', '<ResourceTable'],
    ['Infrastructure · instances', 'providers/infrastructure/portal/src/views/InstanceListPage.vue', '<ResourceTable'],
  ],
  'components.status-badge': [
    ['Edges · detail connection', 'providers/edges/portal/src/Detail.vue', '<StatusBadge :status="edgeStatus"'],
    ['Agents · run status', 'providers/agents/portal/src/views/RunDetail.vue', '<StatusBadge class="agents-phase"'],
    ['Infrastructure · instance phase', 'providers/infrastructure/portal/src/views/InstanceDetailPage.vue', '<StatusBadge'],
  ],
  'components.relationship-graph': [
    ['Kuery · impact analysis', 'providers/kuery/portal/src/components/ImpactView.vue', 'ref="graphHost"'],
    ['Kuery · fleet topology', 'providers/kuery/portal/src/components/TopologyView.vue', 'ref="graphHost"'],
  ],
  'components.copy-control': [
    ['Agents · run ID', 'providers/agents/portal/src/views/RunDetail.vue', 'async function copyRunID'],
    ['Agents · Slack setup handoff', 'providers/agents/portal/src/views/Connections.vue', '<SecretHandoff'],
    ['Infrastructure · resource value', 'providers/infrastructure/portal/src/components/ViewValue.vue', 'async function copy()'],
  ],
  'components.form-select': [
    ['Agents · agent setup', 'providers/agents/portal/src/views/AgentCreate.vue', '<FormSelect'],
    ['Agents · workspace assistant', 'providers/agents/portal/src/views/WorkspaceAssistantCard.vue', '<FormSelect'],
  ],
  'components.checkbox': [
    ['Agents · toolset choices', 'providers/agents/portal/src/views/Toolsets.vue', 'v-model="draftVisualization"'],
  ],
  'components.radio': [
    ['Agents · research focus', 'providers/agents/portal/src/views/AgentCreate.vue', 'type="radio" name="research-focus"'],
  ],
  'components.textarea': [
    ['Agents · instructions', 'providers/agents/portal/src/views/AgentCreate.vue', 'placeholder="Describe how this agent should help."'],
  ],
  'components.search-input': [
    ['Edges · services search', 'providers/edges/portal/src/Services.vue', 'search-placeholder="Search services…"'],
    ['Agents · toolset search', 'providers/agents/portal/src/views/Toolsets.vue', 'search-placeholder="Search toolsets…"'],
  ],
  'components.toggle': [
    ['Agents · workspace assistant', 'providers/agents/portal/src/views/WorkspaceAssistantCard.vue', 'role="switch"'],
    ['Edges · harness setting', 'providers/edges/portal/src/HarnessCard.vue', 'id="edges-harness-enabled"'],
    ['App Studio · skill activation', 'providers/app-studio/portal/src/SkillsWorkbench.vue', 'aria-label="Enable skill"'],
  ],
  'components.spinner': [
    ['Agents · run refresh', 'providers/agents/portal/src/views/RunDetail.vue', "'k-spin': loading"],
  ],
  'components.disclosure': [
    ['Agents · original request', 'providers/agents/portal/src/views/RunDetail.vue', 'class="agents-run-request k-resource-technical"'],
    ['Agents · tool arguments and result', 'providers/agents/portal/src/views/RunDetail.vue', '<details v-if="entry.step.args || entry.step.result"'],
  ],
  'components.file-upload': [
    ['Agents · conversation composer', 'providers/agents/portal/src/views/AgentChat.vue', '<AgentRichComposer'],
    ['App Studio · project composer', 'providers/app-studio/portal/src/AssistantRichComposer.vue', '<AIConversationComposer'],
  ],
  'components.pagination': [
    ['Edges · services table', 'providers/edges/portal/src/Services.vue', ':pagination-mode="tableMode"'],
    ['Infrastructure · instances table', 'providers/infrastructure/portal/src/views/InstanceListPage.vue', ':pagination-mode="paginationMode"'],
  ],
  'components.table-filter': [
    ['Edges · service filters', 'providers/edges/portal/src/Services.vue', ':filters="serviceFilters"'],
    ['Agents · agent filters', 'providers/agents/portal/src/views/AgentsList.vue', ':filters="filters"'],
  ],
  'components.timestamp': [
    ['Agents · run start', 'providers/agents/portal/src/views/RunDetail.vue', 'label="Run started"'],
    ['Agents · run step', 'providers/agents/portal/src/views/RunDetail.vue', 'label="Step recorded"'],
    ['Agents · selected delivery', 'providers/agents/portal/src/views/TriggerFilterTester.vue', 'label="Delivery received"'],
    ['Infrastructure · instance creation', 'providers/infrastructure/portal/src/views/InstanceDetailPage.vue', 'label="Instance created"'],
  ],
  'components.tooltip': [
    ['Infrastructure · value copy', 'providers/infrastructure/portal/src/components/ViewValue.vue', ':data-k-tip='],
    ['Agents · run ID copy', 'providers/agents/portal/src/views/RunDetail.vue', ':data-k-tip="copyState ==='],
  ],
  'components.tabs': [
    ['App Studio · setup navigation', 'providers/app-studio/portal/src/ModelsSettings.vue', '<PortalTabs'],
  ],
}
