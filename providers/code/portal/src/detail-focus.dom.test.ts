// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import { createResourceDeletions } from './refresh'
const mocks = vi.hoisted(() => ({ api: { getConnection: vi.fn(), getRepository: vi.fn(), listConnections: vi.fn(), listDeployKeys: vi.fn(), listCollaborators: vi.fn(), listPackagesPage: vi.fn(), listPackages: vi.fn() } }))
vi.mock('./api', () => mocks)
import ConnectionDetail from './views/ConnectionDetailView.vue'
import RepoDetail from './views/RepoDetailView.vue'
const settle = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
afterEach(() => { document.body.innerHTML = ''; vi.resetAllMocks() })
it.each(([['connection', ConnectionDetail, 'getConnection'], ['repository', RepoDetail, 'getRepository']] as const).flatMap(([kind, component, method]) => ['success', 'moved', 'scope'].map(outcome => ({ kind, component, method, outcome }))))('$kind retry focus: $outcome', async ({ component, method, outcome }) => {
  mocks.api.listConnections.mockResolvedValue([]); mocks.api.listDeployKeys.mockResolvedValue([]); mocks.api.listCollaborators.mockResolvedValue([]); mocks.api.listPackagesPage.mockResolvedValue({ items: [] }); mocks.api.listPackages.mockResolvedValue([])
  mocks.api[method].mockRejectedValueOnce(new Error('Unavailable'))
  let resolve!: (value: any) => void
  mocks.api[method].mockReturnValueOnce(new Promise(r => { resolve = r }))
  const generation = ref(1); const host = document.createElement('div'); const outside = document.createElement('button'); document.body.append(host, outside)
  const deletions = createResourceDeletions()
  const app = createApp({ render: () => h(component, { name: 'demo', authorityGeneration: generation.value, deletions }) })
  app.mount(host)
  try {
    await settle(); const retry = host.querySelector<HTMLButtonElement>('.k-resource-page__retry')!; expect(retry).toBeTruthy(); retry.focus(); retry.click(); await settle()
    if (outcome === 'moved') outside.focus()
    if (outcome === 'scope') { generation.value++; await nextTick() }
    resolve({ name: 'demo', uid: 'uid', conditions: [], scopes: [], provider: 'github', type: 'pat', owner: 'owner', connectionRef: 'conn', repo: 'demo', visibility: 'private', ready: true }); await settle()
    expect(document.activeElement).toBe(outcome === 'success' ? host.querySelector('.k-resource-page__title') : outcome === 'moved' ? outside : document.body)
  } finally { app.unmount() }
})
