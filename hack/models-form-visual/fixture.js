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
const agentsCredentials = [{
  apiVersion: 'agents.railgrid.ai/v1alpha1',
  kind: 'ModelCredential',
  metadata: { name: 'everyday', uid: 'fixture-model', resourceVersion: '1' },
  spec: { provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', model: 'gpt-4o-mini', secretRef: { name: 'fixture-credential' } },
  status: { conditions: [{ type: 'Ready', status: 'True' }, { type: 'SecretResolved', status: 'True' }, { type: 'Reachable', status: 'True' }], models: ['gpt-4o-mini'] },
}]

// Represent the actual bound CRs. There is no model configuration REST route,
// and the fixture contains no real credential values or network requests.
const studio = {
  apiVersion: 'ai.railgrid.ai/v1alpha1', kind: 'Studio',
  metadata: { name: 'studio', uid: 'fixture-studio', resourceVersion: '1' },
  spec: { llm: { defaultModel: 'app-default', models: [{
    id: 'app-default', revisionID: 'fixture-revision', name: 'Workspace default',
    provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', model: 'gpt-5.4',
  }] } },
  status: { models: [{ id: 'app-default', configured: true }] },
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

function appFetch(input, init = {}) {
  const url = new URL(String(input), location.origin)
  const path = url.pathname
  calls.push({ provider: 'app', path, method: init.method || 'GET', headers: hostHeaders(init) })
  if (path === '/api/providers') return Promise.resolve(json(emptyList()))
  if (path === '/clusters/workspace-test/apis/ai.railgrid.ai/v1alpha1/studios/studio') return Promise.resolve(json(studio))
  if (/^\/clusters\/workspace-test\/apis\/[^/]+\/[^/]+\/[^/]+$/.test(path)) return Promise.resolve(json(emptyList()))
  return Promise.resolve(json({ message: `fixture did not mock ${path}` }, 404))
}

function agentsFetch(input, init = {}) {
  const url = new URL(String(input), location.origin)
  const path = url.pathname
  calls.push({ provider: 'agents', path, method: init.method || 'GET', headers: hostHeaders(init) })
  if (path === '/services/providers/agents/oauth/providers') return Promise.resolve(json({ providers: {} }))
  if (path === '/ui/providers/agents/model-catalog.json') return Promise.resolve(json([{ id: 'gpt-4o-mini', inputPer1M: 0.15, outputPer1M: 0.6, contextWindow: 128000, vision: true, toolCall: true }]))
  if (path === '/clusters/workspace-test/apis/agents.railgrid.ai/v1alpha1/modelcredentials') return Promise.resolve(json({ metadata: { resourceVersion: '1' }, items: agentsCredentials }))
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
