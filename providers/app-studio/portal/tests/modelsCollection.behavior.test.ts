// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { expect, it } from 'vitest'
import ModelsSettings from '../src/ModelsSettings.vue'

async function flush() {
  for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() }
  await new Promise(resolve => setTimeout(resolve, 150))
  await nextTick()
}

it('retains model search through an empty snapshot and restores matching rows', async () => {
  const model = { id: 'sol', name: 'Sol', model: 'gpt-6-sol', baseURL: 'https://example.test', configured: true, default: true }
  const settings = ref({ provider: 'openai-compatible', baseURL: '', model: 'gpt-6-sol', configured: true, models: [model] })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ModelsSettings, {
    ctx: null, connectionRef: '', connections: [], settings: settings.value,
    loading: false, loadError: null, saving: false, status: null, actionError: null,
    editorOpen: false, creationRoute: false, editingModelID: null, name: '', model: 'gpt-6-sol',
    nameError: '', modelError: '', modelHint: '', discoveredModels: [], discoveryLoading: false,
    discoveryError: null, testing: false, testStatus: null, testError: null,
    requireConnectionTest: false, nameOnlyEdit: false,
  } as never) })
  try {
    app.mount(host)
    await flush()
    const search = host.querySelector<HTMLInputElement>('input[aria-label="Search Models"]')!
    search.value = 'sol'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    await flush()
    settings.value = { ...settings.value, models: [] }
    await flush()
    expect(host.querySelector<HTMLInputElement>('input[aria-label="Search Models"]')?.value).toBe('sol')
    expect(host.textContent).not.toContain('Connect a provider to add models')
    settings.value = { ...settings.value, models: [model] }
    await flush()
    expect(host.textContent).toContain('Sol')
    const restoredSearch = host.querySelector<HTMLInputElement>('input[aria-label="Search Models"]')!
    restoredSearch.value = ''
    restoredSearch.dispatchEvent(new Event('input', { bubbles: true }))
    await flush()
    const statusFilter = [...host.querySelectorAll<HTMLButtonElement>('[role="combobox"]')].find(button => button.getAttribute('aria-labelledby')?.split(' ').some(id => document.getElementById(id)?.textContent?.trim() === 'Status'))!
    statusFilter.click()
    await flush()
    ;[...document.querySelectorAll<HTMLButtonElement>('[role="option"]')].find(option => option.textContent?.trim() === 'Not tested')!.click()
    await flush()
    settings.value = { ...settings.value, models: [] }
    await flush()
    expect(host.querySelector('table[aria-label="Models"]')).not.toBeNull()
    expect(host.querySelector('.k-table__filter.is-active')).not.toBeNull()
    expect(host.textContent).not.toContain('Connect a provider to add models')
  } finally {
    app.unmount()
    host.remove()
  }
})

it('retains the connection collection and restores focus only when its focused row disappears', async () => {
  const connection = { name: 'main', displayName: 'Main', provider: 'openai', baseURL: 'https://example.test', ready: true }
  const connections = ref([connection])
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ModelsSettings, {
    ctx: null, connectionRef: '', connections: connections.value, settings: null,
    loading: false, loadError: null, saving: false, status: null, actionError: null,
    editorOpen: false, creationRoute: false, editingModelID: null, name: '', model: '',
    nameError: '', modelError: '', modelHint: '', discoveredModels: [], discoveryLoading: false,
    discoveryError: null, testing: false, testStatus: null, testError: null,
    requireConnectionTest: false, nameOnlyEdit: false,
  } as never) })
  try {
    app.mount(host)
    await flush()
    host.querySelector<HTMLButtonElement>('button[aria-label="Delete provider connection Main"]')!.focus()
    connections.value = []
    await flush()
    expect(document.activeElement?.textContent).toBe('Provider connections')
    expect(host.querySelector('table[aria-label="Provider connections"]')).not.toBeNull()
    expect(host.textContent).toContain('No provider connections configured.')
    expect(host.textContent).not.toContain('Connect a provider to add models')
    connections.value = [connection]
    await flush()
    const external = document.createElement('button')
    document.body.append(external)
    external.focus()
    connections.value = []
    await flush()
    expect(document.activeElement).toBe(external)
    external.remove()
  } finally { app.unmount(); host.remove() }
})

it('reports an empty model collection and restores focus after its focused row disappears', async () => {
  const model = { id: 'sol', name: 'Sol', model: 'gpt-6-sol', baseURL: 'https://example.test', configured: true, default: true }
  const settings = ref({ provider: 'openai-compatible', baseURL: '', model: 'gpt-6-sol', configured: true, models: [model] })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ setup: () => () => h(ModelsSettings, {
    ctx: null, connectionRef: '', connections: [], settings: settings.value,
    loading: false, loadError: null, saving: false, status: null, actionError: null,
    editorOpen: false, creationRoute: false, editingModelID: null, name: '', model: '',
    nameError: '', modelError: '', modelHint: '', discoveredModels: [], discoveryLoading: false,
    discoveryError: null, testing: false, testStatus: null, testError: null,
    requireConnectionTest: false, nameOnlyEdit: false,
  } as never) })
  try {
    app.mount(host)
    await flush()
    host.querySelector<HTMLButtonElement>('button[aria-label="Delete Sol"]')!.focus()
    settings.value = { ...settings.value, models: [] }
    await flush()
    expect(document.activeElement?.textContent).toBe('Available models')
    expect(host.textContent).toContain('No models configured.')
    expect(host.textContent).not.toContain('No models match your search.')
  } finally { app.unmount(); host.remove() }
})

it('restores focus after confirming a delayed connection deletion', async () => {
  const { default: ConfirmDialog } = await import('../src/portalkit/ConfirmDialog.vue')
  const { confirmDialog } = await import('../src/portalkit/confirm')
  const connection = { name: 'main', displayName: 'Main', provider: 'openai', baseURL: 'https://example.test', ready: true }
  const connections = ref([connection]), saving = ref(false)
  let finish!: () => void
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => [h(ModelsSettings, {
    ctx: null, connectionRef: '', connections: connections.value, settings: null,
    loading: false, loadError: null, saving: saving.value, status: null, actionError: null,
    editorOpen: false, creationRoute: false, editingModelID: null, name: '', model: '', nameError: '', modelError: '', modelHint: '',
    discoveredModels: [], discoveryLoading: false, discoveryError: null, testing: false, testStatus: null, testError: null,
    requireConnectionTest: false, nameOnlyEdit: false,
    onDeleteConnection: async () => {
      if (!await confirmDialog({ title: 'Delete?', danger: true, confirmLabel: 'Delete connection' })) return
      saving.value = true
      await new Promise<void>(resolve => { finish = resolve })
      connections.value = []; saving.value = false
    },
  } as never), h(ConfirmDialog)] })
  try {
    app.mount(host); await flush()
    const button = host.querySelector<HTMLButtonElement>('button[aria-label="Delete provider connection Main"]')!
    button.focus(); button.click(); await flush()
    ;[...document.querySelectorAll<HTMLButtonElement>('button')].find(b => b.textContent?.trim() === 'Delete connection')!.click()
    await flush(); finish(); await flush()
    expect(document.activeElement?.textContent).toBe('Provider connections')
  } finally { app.unmount(); host.remove() }
})
