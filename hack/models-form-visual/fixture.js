import agentTemplates from '../../providers/agents/portal/public/agent-templates.json'

// Match portal/src/main.ts: register self-hosted fonts before main.css
// references the family tokens. Await the real faces before loading provider
// roots so native controls cannot cache a fallback font during first paint.
await Promise.all([
  import('@fontsource-variable/instrument-sans'),
  import('@fontsource-variable/archivo/wdth.css'),
  import('@fontsource/ibm-plex-mono/400.css'),
  import('@fontsource/ibm-plex-mono/500.css'),
  import('/fixture.css'),
])
await Promise.all([
  document.fonts.load('400 14px "Instrument Sans Variable"'),
  document.fonts.load('600 18px "Instrument Sans Variable"'),
  document.fonts.load('400 18px "Archivo Variable"'),
  document.fonts.load('400 12px "IBM Plex Mono"'),
  document.fonts.load('500 12px "IBM Plex Mono"'),
])
await document.fonts.ready

// Load every real provider entry point. The selected element below receives
// the host context exactly as it does when embedded in the main portal.
await Promise.all([
  import('railgrid-app-main'),
  import('railgrid-agents-main'),
])

const params = new URLSearchParams(location.search)
const provider = params.get('provider') || 'app'
const route = params.get('route') || (provider === 'app' ? '~models' : '')
const theme = params.get('theme') === 'light' ? 'light' : 'dark'
const root = document.querySelector('#root')

document.documentElement.className = theme
if (provider === 'agents' && route.startsWith('#')) location.hash = route
localStorage.setItem('railgrid:portal:tenant', JSON.stringify({
  orgUUID: 'org-test',
  workspaceUUID: 'workspace-test',
}))

const calls = []
function hostHeaders(init) {
  const headers = new Headers(init?.headers)
  headers.set('Authorization', 'Bearer test-token')
  headers.set('X-Railgrid-Org', 'org-test')
  headers.set('X-Railgrid-Workspace', 'workspace-test')
  return Object.fromEntries(headers.entries())
}
const agentsProviderConnections = [{
  apiVersion: 'agents.railgrid.ai/v1alpha1',
  kind: 'ModelProviderConnection',
  metadata: { name: 'openai-primary', uid: 'fixture-agent-connection', resourceVersion: '1' },
  spec: { provider: 'openai', baseURL: 'https://api.openai.com/v1', secretRef: { name: 'fixture-agent-key' }, secretKey: 'apiKey' },
  status: { conditions: [{ type: 'Ready', status: 'True' }, { type: 'SecretResolved', status: 'True' }, { type: 'Reachable', status: 'True' }], models: ['gpt-4o-mini'] },
}]
const agentsCredentials = [{
  apiVersion: 'agents.railgrid.ai/v1alpha1',
  kind: 'ModelCredential',
  metadata: { name: 'everyday', uid: 'fixture-model', resourceVersion: '1' },
  spec: { connectionRef: 'openai-primary', model: 'gpt-4o-mini' },
  status: { conditions: [{ type: 'Ready', status: 'True' }, { type: 'ConnectionReady', status: 'True' }, { type: 'ModelConfigured', status: 'True' }] },
}]
let reviewModelPreferences = { modelCredential: '', selectedCredential: 'everyday', reasoningEffortByModel: {}, effortOptions: ['low', 'medium', 'high'], defaultEffort: 'medium' }
const reviewSkills = [
  { apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'Skill', metadata: { name: 'rollout-review', resourceVersion: '1' }, spec: { document: '---\nname: Rollout review\ndescription: Check rollout health and recovery options.\n---\nReview the rollout status, check recent failures, and identify a safe recovery path.\n', resources: [] } },
  { apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'Skill', metadata: { name: 'incident-summary', resourceVersion: '1' }, spec: { document: '---\nname: Incident summary\ndescription: Summarize investigation evidence.\n---\nSummarize the timeline, impact, and open questions.\n', resources: [] } },
]

// Represent the actual bound CRs. There is no model configuration REST route,
// and the fixture contains no real credential values or network requests.
const appProviderConnections = [{
  apiVersion: 'ai.railgrid.ai/v1alpha1',
  kind: 'ModelProviderConnection',
  metadata: { name: 'model-0123456789abcdef01234567', uid: 'fixture-app-connection', resourceVersion: '1', annotations: { 'ai.railgrid.ai/display-name': 'OpenAI production' } },
  spec: { provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', secretRef: { name: 'fixture-app-key' }, secretKey: 'apiKey' },
  status: { observedGeneration: 1, conditions: [{ type: 'Ready', status: 'True' }, { type: 'SecretResolved', status: 'True' }, { type: 'Reachable', status: 'True' }], models: ['gpt-5.4'] },
}]

const studio = {
  apiVersion: 'ai.railgrid.ai/v1alpha1', kind: 'Studio',
  metadata: { name: 'studio', uid: 'fixture-studio', resourceVersion: '1' },
  // New model/provider creation starts with no model binding and needs no
  // preconfigured model Secret. Connections are separate bound resources.
  spec: { llm: { models: [] } },
  status: { models: [] },
}

// Tab-review data is opt-in so the create-form comparator keeps its empty registry.
if (['tabs', 'relationship', 'app-picker', 'project-links'].includes(params.get('scenario'))) {
  const connection = appProviderConnections[0]
  connection.metadata.generation = 1
  connection.status.connectionFingerprint = 'fixture-fingerprint'
  connection.status.activeSnapshot = {
    provider: connection.spec.provider, baseURL: connection.spec.baseURL,
    connectionRef: connection.metadata.name, connectionUID: connection.metadata.uid,
    connectionGeneration: 1, connectionFingerprint: 'fixture-fingerprint',
    secretRef: connection.spec.secretRef, secretKey: 'apiKey',
    secretUID: 'fixture-key-uid', secretResourceVersion: '1',
  }
  studio.spec.llm.models = [{
    id: 'fast', revisionID: 'fixture-revision', name: 'Everyday model', model: 'gpt-5.4',
    provider: connection.spec.provider, baseURL: connection.spec.baseURL,
    connectionRef: connection.metadata.name, connectionSnapshot: connection.status.activeSnapshot,
  }]
  studio.status.models = [{ id: 'fast', configured: true }]
  if (['app-picker', 'project-links'].includes(params.get('scenario'))) {
    studio.spec.llm.models[0].model = 'gpt-5'
    studio.spec.llm.models.push({ ...studio.spec.llm.models[0], id: 'quick', revisionID: 'fixture-quick', name: 'Quick model', model: 'gpt-4o-mini' })
    studio.status.models.push({ id: 'quick', configured: true })
  }
  if (params.get('scenario') === 'relationship') {
    studio.spec.llm.models.push({ ...studio.spec.llm.models[0], id: 'reasoning', revisionID: 'fixture-reasoning', name: 'Reasoning model', model: 'gpt-5.4-mini' })
    studio.status.models.push({ id: 'reasoning', configured: true })
    agentsCredentials.push({ ...agentsCredentials[0], metadata: { name: 'reasoning', uid: 'fixture-reasoning', resourceVersion: '1' }, spec: { connectionRef: 'openai-primary', model: 'gpt-4o' } })
  }
}

const usage = {
  windowDays: 30,
  total: { key: 'total', runs: 28, errors: 1, inputTokens: 120000, outputTokens: 28000, usdMicros: 1840000, latencyP50MS: 620, latencyP95MS: 1500 },
  byAgent: [{ key: 'Research assistant', runs: 28, usdMicros: 1840000 }],
  byModel: [{ key: 'gpt-4o-mini', runs: 28, usdMicros: 1840000 }],
  series: [
    { date: '2026-09-01', runs: 4, inputTokens: 12000, outputTokens: 3000, usdMicros: 240000 },
    { date: '2026-09-02', runs: 9, inputTokens: 42000, outputTokens: 9000, usdMicros: 780000 },
    { date: '2026-09-03', runs: 15, inputTokens: 66000, outputTokens: 16000, usdMicros: 820000 },
  ],
}

const json = (value, status = 200) => new Response(JSON.stringify(value), {
  status,
  headers: { 'Content-Type': 'application/json' },
})

function emptyList() { return { metadata: { resourceVersion: '1' }, items: [] } }
function resourceList(kind, items) { return { apiVersion: 'v1', kind, metadata: { resourceVersion: '1' }, items } }

function streamResponse() {
  const stream = new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(': keepalive\n\n'))
      // Leave the event stream open so Agents liveness is measured against the
      // real fetch/SSE path rather than a completed mock response.
    },
    cancel() {},
  })
  return new Response(stream, { status: 200, headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' } })
}

let appConnectionReads = 0
let appStudioReads = 0
function appFetch(input, init = {}) {
  const url = new URL(String(input), location.origin)
  const path = url.pathname
  calls.push({ provider: 'app', path, method: init.method || 'GET', headers: hostHeaders(init) })
  if (['app-picker', 'project-links'].includes(params.get('scenario'))) {
    if (init.method && init.method !== 'GET') return Promise.resolve(json({ message: 'This review fixture is read-only.' }, 405))
    const project = { name: 'rollout-console', uid: 'fixture-project', displayName: 'Rollout console', phase: 'Ready' }
    const projectCR = { apiVersion: 'ai.railgrid.ai/v1alpha1', kind: 'Project', metadata: { name: project.name, uid: project.uid }, spec: { displayName: project.displayName }, status: { phase: project.phase } }
    const deletingCR = { ...projectCR, metadata: { name: 'retiring-app', uid: 'fixture-retiring', deletionTimestamp: '2026-11-12T15:00:00Z' }, spec: { displayName: 'Retiring app' } }
    if (path === '/clusters/workspace-test/apis/ai.railgrid.ai/v1alpha1/projects') return Promise.resolve(json(resourceList('ProjectList', params.get('scenario') === 'project-links' ? [projectCR, deletingCR] : [projectCR])))
    if (path.endsWith('/projects/rollout-console/view')) return Promise.resolve(json(project))
    if (path.endsWith('/projects/rollout-console/sessions')) return Promise.resolve(json({ items: [] }))
    if (path.includes('/projects/rollout-console/')) return Promise.resolve(json({ items: [] }))
  }
  if (path === '/api/providers') return Promise.resolve(json(emptyList()))
  if (path === '/clusters/workspace-test/apis/ai.railgrid.ai/v1alpha1/modelproviderconnections') {
    if (params.get('scenario') === 'app-slow-connections') {
      return new Promise(resolve => setTimeout(() => resolve(json(resourceList('ModelProviderConnectionList', appProviderConnections))), 10000))
    }
    if (params.get('scenario') === 'app-connection-retry') {
      appConnectionReads++
      if (appConnectionReads === 1) return Promise.resolve(json({ apiVersion: 'v1', kind: 'Status', status: 'Failure', reason: 'ServiceUnavailable', code: 503, message: 'Provider connections temporarily unavailable.' }, 503))
      return new Promise(resolve => setTimeout(() => resolve(json(resourceList('ModelProviderConnectionList', appProviderConnections))), 1500))
    }
    return Promise.resolve(json(resourceList('ModelProviderConnectionList', appProviderConnections)))
  }
  const connection = appProviderConnections.find(item => path === `/clusters/workspace-test/apis/ai.railgrid.ai/v1alpha1/modelproviderconnections/${item.metadata.name}`)
  if (connection) return Promise.resolve(json(connection))
  if (path === '/clusters/workspace-test/apis/ai.railgrid.ai/v1alpha1/studios/studio') {
    if (params.get('scenario') === 'app-model-retry') {
      appStudioReads++
      if (appStudioReads <= 2) return Promise.resolve(json({ apiVersion: 'v1', kind: 'Status', status: 'Failure', reason: 'ServiceUnavailable', code: 503, message: 'Models are temporarily unavailable.' }, 503))
      return new Promise(resolve => setTimeout(() => resolve(json(studio)), 1500))
    }
    return Promise.resolve(json(studio))
  }
  if (path === '/clusters/workspace-test/apis/ai.railgrid.ai/v1alpha1/studios/studio/create-readiness') {
    return Promise.resolve(json({ gitConnection: { ready: false, status: 'connection-missing', message: 'No Git connection is configured.' } }))
  }
  if (/^\/clusters\/workspace-test\/apis\/[^/]+\/[^/]+\/[^/]+$/.test(path)) return Promise.resolve(json(emptyList()))
  return Promise.resolve(json({ message: `fixture did not mock ${path}` }, 404))
}

let toolsetConnectionReads = 0
let reviewScheduleSuspended = false
let reviewLastModelDeleted = false
function agentsFetch(input, init = {}) {
  const url = new URL(String(input), location.origin)
  const path = url.pathname
  calls.push({ provider: 'agents', path, method: init.method || 'GET', headers: hostHeaders(init) })
  if (params.get('scenario') === 'toolset-missing-connection' && (!init.method || init.method === 'GET')) {
    const prefix = '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/'
    if (path === prefix + 'connections') return Promise.resolve(json(resourceList('ConnectionList', [])))
    if (path === prefix + 'toolsets') return Promise.resolve(json(resourceList('ToolsetList', [
      { metadata: { name: 'research' }, spec: { displayName: 'Research', connections: ['retired-search'], families: ['core', 'web'] } },
    ])))
  }
  if (params.get('scenario') === 'news-template' && path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/connections' && (!init.method || init.method === 'GET')) return Promise.resolve(json(resourceList('ConnectionList', [{ metadata: { name: 'team-telegram' }, spec: { type: 'telegram', displayName: 'Team Telegram' } }])))
  if (params.get('scenario') === 'assisted-search' && path === '/clusters/workspace-test/apis/railgrid.ai/v1alpha1/mcpservers/default') return Promise.resolve(json({ apiVersion: 'railgrid.ai/v1alpha1', kind: 'MCPServer', metadata: { name: 'default' }, status: { federatedProviders: [{ name: 'infrastructure', reachable: true }] } }))
  if (params.get('scenario') === 'connection-states' && path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/connections' && (!init.method || init.method === 'GET')) return Promise.resolve(json(resourceList('ConnectionList', [
    { metadata: { name: 'team-slack' }, spec: { type: 'slack', displayName: 'Team Slack', channel: 'C012345', auth: 'oauth' }, status: { webhookPath: '/fixture/inbound', oauthConnected: true } },
    { metadata: { name: 'github' }, spec: { type: 'github', displayName: 'GitHub' } },
    { metadata: { name: 'search' }, spec: { type: 'websearch', displayName: 'Self-hosted search', config: { provider: 'searxng' } } },
  ])))
  if (params.get('scenario') === 'toolset-connection-retry' && path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/connections' && (!init.method || init.method === 'GET')) {
    toolsetConnectionReads++
    if (toolsetConnectionReads === 1) return Promise.resolve(json({ message: 'Connection list temporarily unavailable.' }, 503))
    return new Promise(resolve => setTimeout(() => resolve(json(resourceList('ConnectionList', []))), 1500))
  }
  if (params.get('scenario') === 'skill-catalog' && path === '/api/providers') return Promise.resolve(json({ items: [] }))
  if (params.get('scenario') === 'skill-catalog' && path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/skills' && (!init.method || init.method === 'GET')) return Promise.resolve(json(resourceList('SkillList', reviewSkills)))
  if (params.get('scenario') === 'skill-catalog' && (!init.method || init.method === 'GET')) {
    const skill = reviewSkills.find(item => path === `/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/skills/${item.metadata.name}`)
    if (skill) return Promise.resolve(json(skill))
  }
  if (path === '/services/providers/agents/oauth/providers') return Promise.resolve(json({ providers: {} }))
  if (path === '/ui/providers/agents/model-catalog.json') return Promise.resolve(json([{ id: 'gpt-4o-mini', inputPer1M: 0.15, outputPer1M: 0.6, contextWindow: 128000, vision: true, toolCall: true }]))
  if (path === '/ui/providers/agents/agent-templates.json') return Promise.resolve(json(['researcher', 'news-template'].includes(params.get('scenario')) ? agentTemplates : []))
  if (['webhook', 'automation-review'].includes(params.get('scenario')) && path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/webhooks') return Promise.resolve(json(resourceList('WebhookList', [{
    apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'Webhook', metadata: { name: 'github-org', uid: 'fixture-webhook' },
    spec: { source: 'github', displayName: 'GitHub org' }, status: { webhookPath: '/services/providers/agents/webhooks/fixture/github-org/sample-token', routes: [] },
  }])))
  if (['webhook', 'automation-review'].includes(params.get('scenario')) && /\/webhooks\/github-org\/deliveries$/.test(path)) return Promise.resolve(json({ items: [], retention: 200 }))
  if (path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/modelproviderconnections') return Promise.resolve(json(resourceList('ModelProviderConnectionList', agentsProviderConnections)))
  if (path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/modelcredentials') return Promise.resolve(json(resourceList('ModelCredentialList', params.get('scenario') === 'first-model' || (params.get('scenario') === 'delete-last-model' && reviewLastModelDeleted) ? [] : agentsCredentials)))
  if (params.get('scenario') === 'spinner' && path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/modelcredentials/everyday/test' && init.method === 'POST') {
    return new Promise(resolve => setTimeout(() => resolve(json({ ok: true, latencyMS: 420 })), 3000))
  }
  if (params.get('scenario') === 'delete-busy' && path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/modelcredentials/everyday' && init.method === 'DELETE') {
    return new Promise(resolve => setTimeout(() => resolve(json({ message: 'The fixture could not delete this model. Try again.' }, 503)), 3000))
  }
  if (params.get('scenario') === 'delete-last-model' && path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/modelcredentials/everyday' && init.method === 'DELETE') {
    reviewLastModelDeleted = true
    return Promise.resolve(json({ kind: 'Status', status: 'Success' }))
  }
  if (['delete-busy', 'delete-last-model'].includes(params.get('scenario')) && path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/modelcredentials/everyday' && (!init.method || init.method === 'GET')) return Promise.resolve(json(agentsCredentials[0]))
  if (params.get('scenario') === 'automation-review' && path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/schedules/daily-review' && init.method === 'PATCH') {
    const suspend = !!JSON.parse(init.body || '{}').spec?.suspend
    return new Promise(resolve => setTimeout(() => {
      reviewScheduleSuspended = suspend
      resolve(json({ apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'Schedule', metadata: { name: 'daily-review' }, spec: { agentRef: 'research-assistant', type: 'cron', schedule: '0 9 * * *', task: 'Review the deployment each morning.', suspend } }))
    }, 3000))
  }
  if (params.get('scenario') === 'automation-review' && path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/schedules') return Promise.resolve(json(resourceList('ScheduleList', [{ apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'Schedule', metadata: { name: 'daily-review' }, spec: { agentRef: 'research-assistant', type: 'cron', schedule: '0 9 * * *', task: 'Review the deployment each morning.', suspend: reviewScheduleSuspended } }])))
  if (params.get('scenario') === 'automation-review' && path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/triggers') return Promise.resolve(json(resourceList('TriggerList', [{ apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'Trigger', metadata: { name: 'on-release' }, spec: { agentRef: 'research-assistant', webhookRef: 'github-org', task: 'Review the release.' } }])))
  if (params.get('scenario') === 'palette' && /\/agents\/research-assistant\/sessions$/.test(path)) return Promise.resolve(json({ items: [{ id: 'review-session', title: 'Review a rollout', messageCount: 0, createdAt: '2026-11-01T12:00:00Z', lastActivity: '2026-11-01T12:00:00Z' }] }))
  if (params.get('scenario') === 'palette' && /\/agents\/research-assistant\/messages$/.test(path)) return Promise.resolve(json({ items: [] }))
  if (params.get('scenario') === 'palette' && /\/agents\/research-assistant\/session\/review-session$/.test(path)) {
    if (init.method === 'PATCH') reviewModelPreferences = { ...reviewModelPreferences, ...JSON.parse(init.body || '{}') }
    return Promise.resolve(json(reviewModelPreferences))
  }
  if (params.get('scenario') === 'palette' && /\/agents\/research-assistant\/skill-catalog$/.test(path)) return Promise.resolve(json({ skills: [{ id: 'rollout-review', name: 'Rollout review', description: 'Check rollout health and recovery options.', enabled: true, scope: 'workspace', digest: 'a'.repeat(64) }], warnings: [] }))
  if (['layout', 'assistant', 'automation-review', 'palette', 'assisted-search'].includes(params.get('scenario')) && path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/agents') return Promise.resolve(json(resourceList('AgentList', [
    { apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'Agent', metadata: { name: 'research-assistant' }, spec: { displayName: 'Research assistant', ...(params.get('scenario') === 'palette' ? { skills: [{ name: 'rollout-review' }] } : {}), backend: { type: 'model', model: { credentials: { chat: 'everyday' } } } } },
    { apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'Agent', metadata: { name: 'release-reviewer' }, spec: { displayName: 'Release reviewer', backend: { type: 'model', model: { credentials: { chat: 'everyday' } } } } },
  ])))
  if (/^\/clusters\/workspace-test\/apis\/[^/]+\/[^/]+\/[^/]+$/.test(path)) return Promise.resolve(json(emptyList()))
  if (/\/agents\/[^/]+\/events$/.test(path)) return Promise.resolve(streamResponse())
  if (/\/agents\/[^/]+\/usage$/.test(path)) return Promise.resolve(json({ result: usage }))
  return Promise.resolve(json({ message: `fixture did not mock ${path}` }, 404))
}


function updateContext(element, context, path) {
  const normalized = String(path || '').replace(/^#\/?/, '').replace(/^\//, '')
  context.subPath = normalized
  element.railgridContext = { ...context, subPath: normalized }
}

const tag = provider === 'agents'
  ? 'railgrid-provider-agents'
  : 'railgrid-provider-app-studio'
const element = document.createElement(tag)
const context = {
  token: 'test-token',
  tenant: 'workspace-test',
  orgUUID: 'org-test',
  workspaceUUID: 'workspace-test',
  basePath: `/ui/providers/${provider === 'app' ? 'app-studio' : provider}`,
  subPath: route.replace(/^#\/?/, '').replace(/^\//, ''),
  theme,
  user: { userId: 'visual-user', sub: 'visual-user' },
  fetch: provider === 'agents' ? agentsFetch : appFetch,
}
if (provider === 'agents' && params.get('scenario') === 'assistant') {
  const overlay = {
    selectedAgent: 'research-assistant', followContext: true, settingsOpen: false,
    setSettingsOpen(open) { overlay.settingsOpen = open; element.railgridContext = { ...context, agentOverlay: { ...overlay } } },
    configure(name, follow) { overlay.selectedAgent = name; overlay.followContext = follow; element.railgridContext = { ...context, agentOverlay: { ...overlay } } },
    select(name) { overlay.selectedAgent = name; element.railgridContext = { ...context, agentOverlay: { ...overlay } } },
  }
  context.agentOverlay = overlay
}

element.addEventListener('railgrid-navigate', event => {
  const detail = event.detail || {}
  if (provider === 'app' && typeof detail.path === 'string') {
    updateContext(element, context, detail.path)
    return
  }
  if (provider === 'agents' && typeof detail.path === 'string' && location.hash !== detail.path) {
    location.hash = detail.path
    updateContext(element, context, detail.path)
  }
})
root.replaceChildren(element)
element.railgridContext = context

// Expose deterministic evidence for Playwright scripts without relying on
// Vue internals. The host receives the same context shape it would push.
globalThis.__railgridModelFixture = { element, calls, context, provider, theme }
