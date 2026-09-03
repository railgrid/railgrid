import { api, setBasePath, setTenantSelection, setToken } from './api.js'

function assert(condition: unknown, label: string): asserts condition { if (!condition) throw new Error(label) }
async function expectProtocol(task: () => Promise<unknown>, label: string): Promise<void> {
  try {
    await task()
  } catch (error) {
    const failure = error as { reason?: string; retryable?: boolean }
    assert(failure.reason === 'ProtocolError' && failure.retryable === true, `${label}: wrong error`)
    return
  }
  throw new Error(`${label}: malformed payload was accepted`)
}
const requests: Array<{ url: string; init?: RequestInit }> = []
const originalFetch = globalThis.fetch
setBasePath('/ui/providers/databricks'); setTenantSelection('org-1', 'workspace-1'); setToken('token-1')
globalThis.fetch = async (input, init) => {
  requests.push({ url: String(input), init })
  return new Response(String(input).includes('/registrations') ? JSON.stringify({ results: [{ index: 0, name: 'orders', state: 'created' }] }) : JSON.stringify({ items: [{ id: 'wh-1', name: 'Orders', supported: true }], nextPageToken: 'next' }), { status: 200 })
}
try {
  const page = await api.discoverWarehouses('sales prod', 'page/1')
  assert(page.items[0]?.id === 'wh-1', 'discovery response')
  assert(requests[0]?.url === '/services/providers/databricks/api/v1/discovery/warehouses?connectionRef=sales+prod&pageToken=page%2F1', 'encoded discovery URL')
  const headers = requests[0]?.init?.headers as Record<string, string>
  assert(headers.Authorization === 'Bearer token-1' && headers['X-Faros-Org'] === 'org-1' && headers['X-Faros-Workspace'] === 'workspace-1', 'identity headers')
  const result = await api.registerResources({ kind: 'warehouse', connectionRef: 'sales', items: [{ name: 'orders', warehouseID: 'wh-1' }] })
  assert(result.results[0]?.state === 'created' && requests[1]?.init?.method === 'POST', 'registration contract')

  globalThis.fetch = async () => new Response(JSON.stringify({ results: [{ index: 0, name: 'orders', state: 'failed', message: 'HTTPError: {"error":"token=dapi-secret"}' }] }), { status: 200 })
  const sanitized = await api.registerResources({ kind: 'warehouse', connectionRef: 'sales', items: [{ name: 'orders', warehouseID: 'wh-1' }] })
  assert(sanitized.results[0]?.message === 'Registration failed. Retry this item.', 'registration result exposed raw or sensitive provider text')

  globalThis.fetch = async () => new Response(JSON.stringify({ results: [{ index: 0, name: 'orders', state: 'conflict', message: 'A warehouse with this ID is already registered.' }] }), { status: 200 })
  const actionable = await api.registerResources({ kind: 'warehouse', connectionRef: 'sales', items: [{ name: 'orders', warehouseID: 'wh-1' }] })
  assert(actionable.results[0]?.message === 'A warehouse with this ID is already registered.', 'safe registration result detail was not preserved')

  globalThis.fetch = async () => new Response('{}', { status: 200 })
  await expectProtocol(() => api.discoverWarehouses('sales'), 'missing discovery envelope')

  globalThis.fetch = async () => new Response(JSON.stringify({ items: [], nextPageToken: 7 }), { status: 200 })
  await expectProtocol(() => api.discoverCatalogs('sales'), 'invalid discovery page token')

  globalThis.fetch = async () => new Response(JSON.stringify({ items: [{ id: 'wh-1', name: 'Orders' }] }), { status: 200 })
  await expectProtocol(() => api.discoverWarehouses('sales'), 'missing discovery item field')

  globalThis.fetch = async () => new Response('{}', { status: 200 })
  await expectProtocol(() => api.registerResources({ kind: 'warehouse', connectionRef: 'sales', items: [{ name: 'orders', warehouseID: 'wh-1' }] }), 'missing registration envelope')

  globalThis.fetch = async () => new Response(JSON.stringify({ results: [{ index: 0, state: 'unknown' }] }), { status: 200 })
  await expectProtocol(() => api.registerResources({ kind: 'warehouse', connectionRef: 'sales', items: [{ name: 'orders', warehouseID: 'wh-1' }] }), 'invalid registration state')
} finally { globalThis.fetch = originalFetch }
