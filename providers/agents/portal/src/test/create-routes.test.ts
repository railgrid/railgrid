import { describe, expect, it, vi } from 'vitest'
import { confirmState, resolveConfirm } from '../portalkit/confirm'
import { validDigestCron } from '../agent-digest-cron'
import type { Agent } from '../types'
import AgentCreateView from '../views/AgentCreate.vue'
import AgentsListView from '../views/AgentsList.vue'
import ConnectionsView from '../views/Connections.vue'
import ModelsView from '../views/Models.vue'
import ToolsetsView from '../views/Toolsets.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, type MountedVue } from './vue-helper'

type AgentCreateWizard = HTMLElement
type Connections = HTMLElement
type Models = HTMLElement
type Toolsets = HTMLElement
const mountedByElement = new WeakMap<Element, MountedVue>()
async function mount<T = HTMLElement>(_tag: string, props: Record<string, unknown>): Promise<T> {
  const component = _tag.includes('agent-create') ? AgentCreateView
    : _tag.includes('agents-list') ? AgentsListView
      : _tag.includes('connections') ? ConnectionsView
        : _tag.includes('models') ? ModelsView : ToolsetsView
  const mounted = await mountVue(component, props)
  mountedByElement.set(mounted.element, mounted)
  await settleVue(1, 120)
  return mounted.element as unknown as T
}
async function settle(_element: Element, passes = 4): Promise<void> { await settleVue(passes, 120) }
async function chooseFormSelect(root: Element, selector: string, label: string): Promise<void> {
  root.querySelector<HTMLButtonElement>(selector)!.click()
  await settleVue()
  const option = [...document.querySelectorAll<HTMLElement>('.k-form-select__option')].find(item => item.textContent?.includes(label))
  expect(option).toBeTruthy()
  option!.click()
  await settleVue()
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => (resolve = resolvePromise))
  return { promise, resolve }
}

function markAuthoritative(store: ReturnType<typeof makeStore>, ...keys: Array<'agents' | 'credentials' | 'connections' | 'toolsets' | 'edges'>): void {
  for (const key of keys) {
    store[key].loaded = true
    store[key].hasSnapshot = true
  }
}

describe('route-owned creation surfaces', () => {
  it('opens machine setup in a scoped new tab and refreshes choices without discarding the agent draft', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.credentials.data = [{ name: 'codex', provider: 'codex' }]
    markAuthoritative(store, 'agents', 'credentials', 'edges')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api, initialBackendType: 'harness' })
    const name = el.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'work-in-progress'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    const link = el.querySelector<HTMLAnchorElement>('a[target="_blank"]')!
    expect(link.getAttribute('href')).toBe('/ui/org/ws/providers/edges/connect/edge')
    expect(link.rel).toContain('noopener')
    const load = vi.spyOn(store, 'load').mockResolvedValue()
    const check = [...el.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent?.trim() === 'Check again')!
    check.click(); await settle(el)
    expect(load).toHaveBeenCalledWith('edges')
    expect(name.value).toBe('work-in-progress')
    expect(el.textContent).toContain('Keep this tab open')
  })

  it('renders agent creation as a page and emits its result after the API succeeds', async () => {
    const createAgent = vi.fn().mockResolvedValue({ metadata: { name: 'nova' }, spec: { displayName: 'nova' } })
    const api = stubApi({ createAgent })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5', ready: true }]
    markAuthoritative(store, 'agents', 'credentials')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api })
    const events = mountedByElement.get(el)!.events

    expect(el.querySelector('.agents-overlay')).toBeNull()
    expect(el.querySelector('.k-create-page')).not.toBeNull()
    expect(el.querySelector('[role="dialog"]')).toBeNull()
    expect(el.querySelector('.k-create-body--guided .k-create-guidance')?.textContent).toContain('After you create the agent')
    expect(el.querySelector('.k-create-guidance__values')).toBeNull()
    expect(el.querySelector('button[type=submit]')?.textContent).toContain('Create & open chat')
    expect(el.querySelector('#agent-create-model')?.textContent).toContain('main')
    expect(el.querySelector('#agent-create-channel')).toBeNull()
    el.querySelector<HTMLInputElement>('input[name=name]')!.value = 'nova'
    el.querySelector<HTMLInputElement>('input[name=name]')!.dispatchEvent(new InputEvent('input', { bubbles: true }))
    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 5)

    expect(createAgent).toHaveBeenCalledWith({ name: 'nova', displayName: 'nova', modelCredential: 'main' })
    expect(events['create-success']?.[0]).toEqual(expect.objectContaining({ resource: 'agent', name: 'nova', item: expect.anything() }))
  })

  it.each(['removed', 'unreachable'])('can clear a template when its catalog is %s and keep the draft', async (state) => {
    const createAgent = vi.fn().mockResolvedValue({ metadata: { name: 'nova' }, spec: {} })
    const agentTemplates = state === 'removed'
      ? vi.fn().mockResolvedValue([])
      : vi.fn().mockRejectedValue(new Error('offline'))
    const api = stubApi({ createAgent, agentTemplates })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5', ready: true }]
    markAuthoritative(store, 'agents', 'credentials')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api, initialTemplate: 'missing' })
    const name = el.querySelector<HTMLInputElement>('#agent-create-name')!
    const instructions = el.querySelector<HTMLTextAreaElement>('textarea')!
    const submit = el.querySelector<HTMLButtonElement>('button[type=submit]')!
    name.value = 'nova'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    instructions.value = 'Answer with references.'
    instructions.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await settleVue()
    expect(submit.disabled).toBe(true)

    const clear = [...el.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Clear template')!
    clear.click()
    await settleVue()
    expect(submit.disabled).toBe(false)
    el.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue()
    expect(createAgent).toHaveBeenCalledWith(expect.objectContaining({ name: 'nova', systemPrompt: 'Answer with references.' }))
  })

  it.each(['', 'Answer with references.'])('applies a template in place with draft instructions "%s"', async (draftInstructions) => {
    const catalog = (await import('../../public/agent-templates.json')).default
    const agentTemplates = vi.fn().mockResolvedValue(catalog)
    const api = stubApi({ agentTemplates })
    const store = makeStore(api)
    markAuthoritative(store, 'agents', 'credentials')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api })
    const name = el.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'my-draft'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    const sections = () => [...el.querySelectorAll('.agents-create-section h2')].map(section => section.textContent)
    const initialSections = ['Agent definition', 'Execution', 'Template inputs', 'Connections & automation']
    const instructions = el.querySelector<HTMLTextAreaElement>('textarea[aria-labelledby="agent-create-system-prompt-label"]')!
    instructions.value = draftInstructions
    instructions.dispatchEvent(new InputEvent('input', { bubbles: true }))
    expect(sections()).toEqual(initialSections)
    const trigger = el.querySelector<HTMLButtonElement>('.k-create-header [aria-haspopup="dialog"]')!
    const picker = el.querySelector<HTMLDialogElement>('dialog')!
    picker.showModal = () => { picker.open = true }
    picker.close = () => { picker.open = false }

    expect(picker.open).toBe(false)
    trigger.click()
    await settle(el)
    expect(picker.open).toBe(true)
    expect(picker.querySelectorAll('.agents-starter-card')).toHaveLength(4)
    picker.querySelector<HTMLButtonElement>('[aria-label="Close templates"]')!.click()
    expect(name.value).toBe('my-draft')
    expect(picker.open).toBe(false)

    trigger.click()
    const researcher = [...picker.querySelectorAll<HTMLButtonElement>('.agents-starter-card')]
      .find(card => card.querySelector('.agents-starter-name')?.textContent === 'Researcher')!
    researcher.click()
    await settle(el)
    expect(picker.open).toBe(false)
    expect(name.value).toBe('my-draft')
    expect(el.querySelector('[data-selected-template]')?.textContent).toContain('Researcher')
    expect(el.querySelector('#agent-create-param-domain')).toBeNull()
    expect(sections()).toEqual(['Agent definition', 'Connections'])
    expect(el.querySelector('[data-use-coding-harness]')).toBeNull()
    const templateInstructions = el.querySelector<HTMLTextAreaElement>('textarea[aria-labelledby="agent-create-system-prompt-label"]')!
    expect(templateInstructions.value).toContain(draftInstructions || 'research')

    const modes = el.querySelectorAll<HTMLInputElement>('input[name="research-focus"]')
    expect(modes[0].checked).toBe(true)
    modes[1].click()
    await settleVue()
    const domain = el.querySelector<HTMLTextAreaElement>('#agent-create-param-domain')!
    domain.value = 'cloud pricing'
    domain.dispatchEvent(new InputEvent('input', { bubbles: true }))

    trigger.click()
    const assistant = [...picker.querySelectorAll<HTMLButtonElement>('.agents-starter-card')]
      .find(card => card.querySelector('.agents-starter-name')?.textContent === 'Workspace assistant')!
    assistant.click()
    await settleVue()
    trigger.click()
    researcher.click()
    await settleVue()
    expect(el.querySelector<HTMLInputElement>('input[name="research-focus"]:checked')).toBe(el.querySelectorAll('input[name="research-focus"]')[1])
    expect(el.querySelector<HTMLTextAreaElement>('#agent-create-param-domain')?.value).toBe('cloud pricing')

    ;[...el.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Clear template')!.click()
    await settle(el)
    expect(sections()).toEqual(initialSections)
    expect(el.querySelector('[data-selected-template]')).toBeNull()
    expect(name.value).toBe('my-draft')
    expect(agentTemplates).toHaveBeenCalledTimes(1)
  })

  it('replaces team context only after confirmation and preserves newer edits', async () => {
    const catalog = (await import('../../public/agent-templates.json')).default
    const api = stubApi({ agentTemplates: async () => catalog })
    const store = makeStore(api)
    markAuthoritative(store, 'agents', 'credentials')
    const el = await mount('agents-agent-create', { store, api, initialTemplate: 'workspace-assistant' })
    const context = el.querySelector<HTMLTextAreaElement>('#agent-create-param-teamContext')!
    const platform = [...el.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Platform team')!
    const editContext = async (value: string) => {
      context.value = value
      context.dispatchEvent(new InputEvent('input', { bubbles: true }))
      await settleVue()
    }

    await editContext('We maintain a customer portal.')
    platform.click()
    expect(confirmState.open).toBe(true)
    expect(confirmState.title).toBe('Replace team context?')
    resolveConfirm(false)
    await settleVue()
    expect(context.value).toBe('We maintain a customer portal.')

    platform.click()
    await editContext('We maintain two customer portals.')
    resolveConfirm(true)
    await settleVue()
    expect(context.value).toBe('We maintain two customer portals.')

    platform.click()
    resolveConfirm(true)
    await settleVue()
    expect(context.value).toContain('We maintain Kubernetes clusters')
    expect(el.querySelector('[data-generated-instructions]')?.textContent).toContain(context.value)
  })

  it('requires an area of expertise for a Specialist researcher', async () => {
    const catalog = (await import('../../public/agent-templates.json')).default
    const createAgentFromTemplate = vi.fn(async (agent: Agent) => ({ agent, createdSchedules: [], warnings: [] }))
    const api = stubApi({ agentTemplates: async () => catalog, createAgentFromTemplate })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5', ready: true }]
    markAuthoritative(store, 'agents', 'credentials', 'connections')
    const el = await mount('agents-agent-create', { store, api, initialTemplate: 'researcher' })
    const specialist = [...el.querySelectorAll('label')].find(label => label.textContent?.includes('Specialist'))!
    specialist.querySelector('input')!.click()
    await settleVue()
    const form = el.querySelector('form')!
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue()
    expect(el.querySelector('#agent-create-param-domain-error')?.textContent).toContain('Enter an area of expertise')
    expect(confirmState.open).toBe(false)
    expect(createAgentFromTemplate).not.toHaveBeenCalled()

    const domain = el.querySelector<HTMLTextAreaElement>('#agent-create-param-domain')!
    domain.value = 'EU energy policy'
    domain.dispatchEvent(new InputEvent('input', { bubbles: true }))
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue()
    expect(confirmState.open).toBe(true)
    resolveConfirm(true)
    await settleVue()
    expect(createAgentFromTemplate.mock.calls[0]?.[0].spec.systemPrompt).toContain('Your area: EU energy policy.')
  })

  it('preserves repository entries while refreshing setup in new tabs and allows an offline machine with a warning', async () => {
    const catalog = (await import('../../public/agent-templates.json')).default
    const api = stubApi({ agentTemplates: async () => catalog })
    const store = makeStore(api)
    markAuthoritative(store, 'agents', 'credentials', 'edges', 'connections')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api, initialTemplate: 'code-reviewer' })
    await settle(el)
    const setup = el.querySelector('#agent-create-execution-heading')!.parentElement!
    expect(setup.querySelectorAll('[role="combobox"]')).toHaveLength(0)
    expect([...setup.querySelectorAll('a, button')].map(item => item.textContent?.trim())).toEqual([
      'Connect machine', 'Add harness credential', 'Add GitHub connection',
    ])
    expect(setup.querySelectorAll('a[target="_blank"]')).toHaveLength(3)
    expect(setup.querySelector('.agents-hint')).toBeNull()
    const harnessSetup = setup.querySelector<HTMLAnchorElement>('a[href*="/create/model/harness"]')!
    expect(harnessSetup.getAttribute('href')).toBe('/ui/org/ws/providers/agents/#/create/model/harness')
    harnessSetup.addEventListener('auxclick', event => event.preventDefault())
    harnessSetup.dispatchEvent(new MouseEvent('auxclick', { button: 1, bubbles: true, cancelable: true }))
    const load = vi.spyOn(store, 'load').mockImplementation(async key => {
      if (key === 'credentials') store.credentials.data = [{ name: 'codex', provider: 'codex' }]
      if (key === 'connections') store.connections.data = [{ metadata: { name: 'github' }, spec: { type: 'github' } }]
      store.dispatchEvent(new Event('change'))
    })
    window.dispatchEvent(new Event('focus'))
    expect(load).toHaveBeenCalledWith('credentials')
    store.edges.data = [{ kind: 'LinuxServer', name: 'build-01', connected: false }]
    store.dispatchEvent(new Event('change'))
    await settleVue()
    await chooseFormSelect(el, '#agent-create-edge', 'build-01')
    await chooseFormSelect(el, '#agent-create-harnesscred', 'codex')
    expect(el.querySelector('#agent-create-edge-hint')?.textContent).toContain('offline')
    const submit = el.querySelector<HTMLButtonElement>('button[type="submit"]')!
    expect(submit.disabled).toBe(true)

    const repo = el.querySelector<HTMLInputElement>('#agent-create-param-repositories')!
    const paste = new Event('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(paste, 'clipboardData', { value: { getData: () => 'railgrid/railgrid\r\nrailgrid/docs' } })
    repo.dispatchEvent(paste)
    repo.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
    repo.value = 'railgrid/仮'
    repo.dispatchEvent(new InputEvent('input', { bubbles: true, isComposing: true }))
    await settleVue()
    expect(el.querySelector('[data-generated-instructions]')?.textContent).not.toContain('railgrid/仮')
    repo.value = 'railgrid/日本語'
    repo.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true }))
    repo.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await settleVue()
    expect(el.querySelector('[aria-label="Remove railgrid/日本語"]')).not.toBeNull()
    const link = [...el.querySelectorAll<HTMLAnchorElement>('a')].find(item => item.textContent?.includes('Add GitHub connection'))!
    expect(link.getAttribute('href')).toBe('/ui/org/ws/providers/agents/#/create/connection/github')
    link.addEventListener('click', event => event.preventDefault())
    link.click()
    window.dispatchEvent(new Event('focus'))
    await settleVue()
    expect(load).toHaveBeenCalledWith('connections')
    await chooseFormSelect(el, '#agent-create-github', 'github')
    expect(el.querySelector('[aria-label="Remove railgrid/railgrid"]')).not.toBeNull()
    expect(el.querySelector('[aria-label="Remove railgrid/docs"]')).not.toBeNull()
    expect(submit.disabled).toBe(false)
  })

  it.each(['codex', 'claude-code'])('writes reasoning effort only for Codex after changing the harness to %s', async (provider) => {
    const catalog = (await import('../../public/agent-templates.json')).default
    const createAgentFromTemplate = vi.fn(async (agent: Agent) => ({ agent, createdSchedules: [], warnings: [] }))
    const api = stubApi({ agentTemplates: async () => catalog, createAgentFromTemplate })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'codex-login', provider: 'codex' }, { name: 'claude-login', provider: 'claude-code' }]
    store.edges.data = [{ kind: 'LinuxServer', name: 'build-01', connected: true }]
    store.connections.data = [{ metadata: { name: 'github' }, spec: { type: 'github' } }]
    markAuthoritative(store, 'agents', 'credentials', 'edges', 'connections')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api, initialTemplate: 'code-reviewer' })
    await chooseFormSelect(el, '#agent-create-edge', 'build-01')
    await chooseFormSelect(el, '#agent-create-github', 'github')
    await chooseFormSelect(el, '#agent-create-harnesscred', 'codex-login')
    const repo = el.querySelector<HTMLInputElement>('#agent-create-param-repositories')!
    repo.value = 'railgrid/railgrid'
    repo.dispatchEvent(new InputEvent('input', { bubbles: true }))

    const effort = el.querySelector<HTMLInputElement>('#agent-create-harness-effort')!
    effort.closest('details')!.open = true
    effort.value = 'max'
    effort.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await chooseFormSelect(el, '#agent-create-harnesscred', 'claude-login')
    expect(el.querySelector('#agent-create-harness-effort')).toBeNull()
    if (provider === 'codex') {
      await chooseFormSelect(el, '#agent-create-harnesscred', 'codex-login')
      expect(el.querySelector<HTMLInputElement>('#agent-create-harness-effort')?.value).toBe('max')
    }

    el.querySelector<HTMLButtonElement>('button[type="submit"]')!.click()
    await settle(el)
    expect(createAgentFromTemplate).toHaveBeenCalledTimes(1)
    const harness = createAgentFromTemplate.mock.calls[0][0].spec.backend?.harness
    expect(harness?.credentialRef).toBe(provider === 'codex' ? 'codex-login' : 'claude-login')
    expect(harness?.reasoningEffort).toBe(provider === 'codex' ? 'max' : undefined)
  })

  it('explains a GitHub connection that loses authorization and accepts a replacement', async () => {
    const catalog = (await import('../../public/agent-templates.json')).default
    const api = stubApi({ agentTemplates: async () => catalog })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'codex', provider: 'codex' }]
    store.edges.data = [{ kind: 'LinuxServer', name: 'build-01', connected: true }]
    const oauth = { metadata: { name: 'oauth-github' }, spec: { type: 'github', auth: 'oauth' }, status: { oauthConnected: true } }
    const replacement = { metadata: { name: 'other-github' }, spec: { type: 'github' } }
    store.connections.data = [oauth, replacement]
    markAuthoritative(store, 'agents', 'credentials', 'edges', 'connections')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api, initialTemplate: 'code-reviewer' })
    await chooseFormSelect(el, '#agent-create-edge', 'build-01')
    await chooseFormSelect(el, '#agent-create-harnesscred', 'codex')
    await chooseFormSelect(el, '#agent-create-github', 'oauth-github')
    const submit = el.querySelector<HTMLButtonElement>('button[type="submit"]')!
    expect(submit.disabled).toBe(false)

    store.connections.data = [{ ...oauth, status: { oauthConnected: false } }, replacement]
    store.dispatchEvent(new Event('change'))
    await settleVue()

    const picker = el.querySelector('#agent-create-github')!
    expect(submit.disabled).toBe(true)
    expect(picker.textContent).not.toContain('oauth-github')
    expect(picker.getAttribute('aria-invalid')).toBe('true')
    expect(picker.getAttribute('aria-describedby')).toContain('agent-create-github-status')
    expect(el.querySelector('#agent-create-github-status')?.textContent).toContain('Choose another connection.')

    await chooseFormSelect(el, '#agent-create-github', 'other-github')
    expect(el.querySelector('#agent-create-github-status')).toBeNull()
    expect(picker.hasAttribute('aria-invalid')).toBe(false)
    expect(submit.disabled).toBe(false)
  })

  it('checks digest cron time zone names and Go integer bounds', () => {
    expect(validDigestCron('CRON_TZ=+01:00 0 8 * * *')).toBe(false)
    expect(validDigestCron('TZ=-05:00 0 8 * * *')).toBe(false)
    expect(validDigestCron('CRON_TZ=america/new_york 0 8 * * *')).toBe(false)
    expect(validDigestCron('TZ=us/eastern 0 8 * * *')).toBe(false)
    expect(validDigestCron('CRON_TZ=America/New_York 0 8 * * *')).toBe(true)
    expect(validDigestCron('TZ=US/Eastern 0 8 * * *')).toBe(true)
    expect(validDigestCron('TZ=Local 0 8 * * *')).toBe(true)
    expect(validDigestCron('*/9223372036854775808 * * * *')).toBe(false)
    expect(validDigestCron('*/9223372036854775807 * * * *')).toBe(true)
    expect(validDigestCron('CRON_TZ=Europe/London */15 8 * * MON-FRI')).toBe(true)
    expect(validDigestCron('@every 3000000h')).toBe(false)
    expect(validDigestCron('@every 9223372036854775807ns1ns')).toBe(false)
    expect(validDigestCron('@every 9223372036854775808ns')).toBe(false)
    expect(validDigestCron('@every 9223372036854775807ns')).toBe(true)
    expect(validDigestCron('@every -9223372036854775808ns')).toBe(true)
    expect(validDigestCron('@every 9223372036.854775807s')).toBe(true)
    expect(validDigestCron('@every 9223372036.854775808s')).toBe(false)
    expect(validDigestCron('@every 1h30m.5s')).toBe(true)
    expect(validDigestCron('@every 1h 30m')).toBe(false)
  })

  it('preserves a cleared digest time through template switches and model setup', async () => {
    const catalog = (await import('../../public/agent-templates.json')).default
    const createAgentFromTemplate = vi.fn()
    const api = stubApi({ agentTemplates: async () => catalog, createAgentFromTemplate })
    const store = makeStore(api)
    markAuthoritative(store, 'agents', 'credentials', 'edges', 'connections')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api, initialTemplate: 'news-reporter' })
    const timeInput = (root: Element) => root.querySelector<HTMLInputElement>('input[type="time"]')!
    const setTime = (value: string) => {
      const input = timeInput(el)
      input.value = value
      input.dispatchEvent(new InputEvent('input', { bubbles: true }))
    }
    setTime('09:15')
    await settleVue()
    setTime('')
    await settleVue()

    const picker = el.querySelector<HTMLDialogElement>('dialog')!
    picker.showModal = () => { picker.open = true }
    picker.close = () => { picker.open = false }
    const switchTemplate = async (name: string) => {
      el.querySelector<HTMLButtonElement>('.k-create-header [aria-haspopup="dialog"]')!.click()
      await settleVue()
      const card = [...picker.querySelectorAll<HTMLButtonElement>('.agents-starter-card')]
        .find(button => button.querySelector('.agents-starter-name')?.textContent === name)!
      card.click()
      await settleVue()
    }
    await switchTemplate('Researcher')
    expect(timeInput(el)).toBeNull()
    await switchTemplate('News reporter')
    expect(timeInput(el).value).toBe('')
    expect(timeInput(el).checkValidity()).toBe(false)

    const mounted = mountedByElement.get(el)!
    const connect = [...el.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Connect a model')!
    connect.click()
    const { draft } = mounted.events['add-credential'][0] as { draft: unknown }
    mounted.unmount()
    const restored = await mount<AgentCreateWizard>('agents-agent-create', { store, api, initialDraft: draft })
    expect(timeInput(restored).value).toBe('')
    restored.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue()
    expect(restored.querySelector('#agent-digest-cron-error')?.textContent).toContain('choose a time for the digest')
    expect(createAgentFromTemplate).not.toHaveBeenCalled()

    const editCron = [...restored.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Edit cron')!
    editCron.click()
    await settleVue()
    const cron = restored.querySelector<HTMLInputElement>('#agent-create-param-digestTime')!
    expect(cron.value).toBe('')
    restored.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue()
    expect(restored.querySelector('#agent-digest-cron-error')?.textContent).toContain('Enter a cron expression')
    expect(cron.getAttribute('aria-invalid')).toBe('true')
    expect(createAgentFromTemplate).not.toHaveBeenCalled()
  })

  it('serializes selected digest days and keeps at least one day selected', async () => {
    const catalog = (await import('../../public/agent-templates.json')).default
    const api = stubApi({ agentTemplates: async () => catalog })
    const store = makeStore(api)
    markAuthoritative(store, 'agents', 'credentials', 'connections')
    const el = await mount('agents-agent-create', { store, api, initialTemplate: 'news-reporter' })
    const day = (label: string) => el.querySelector<HTMLButtonElement>(`.agents-schedule-days [aria-label="${label}"]`)!
    const clickButton = async (label: string) => {
      const button = [...el.querySelectorAll('button')].find(item => item.textContent?.trim() === label)!
      button.click()
      await settleVue()
    }
    const expectCron = async (expression: string) => {
      await clickButton('Edit cron')
      expect(el.querySelector<HTMLInputElement>('#agent-create-param-digestTime')?.value).toBe(expression)
      await clickButton('Use time and weekdays')
    }
    const time = el.querySelector<HTMLInputElement>('input[type="time"]')!
    time.value = '09:15'
    time.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await settleVue()

    day('Sunday').click()
    await settleVue()
    await expectCron('15 9 * * 0,1,2,3,4,5')
    day('Saturday').click()
    await settleVue()
    await expectCron('15 9 * * *')

    for (const label of ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']) {
      day(label).click()
      await settleVue()
    }
    expect(day('Sunday').disabled).toBe(true)
    expect(day('Sunday').getAttribute('aria-pressed')).toBe('true')
    await expectCron('15 9 * * 0')
  })

  it('keeps the last machine snapshot visible and describes runner fields accessibly', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.credentials.data = [{ name: 'claude', provider: 'claude-code' }]
    store.edges.data = [{ kind: 'LinuxServer', name: 'build-01', connected: true }]
    store.edges.error = 'edge refresh unavailable'
    markAuthoritative(store, 'agents', 'credentials', 'edges')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api })

    el.querySelector<HTMLInputElement>('[data-use-coding-harness]')!.click()
    await settle(el, 2)

    const machine = el.querySelector<HTMLButtonElement>('#agent-create-edge')!
    expect(el.querySelector('#agent-create-edge-label')?.tagName).toBe('SPAN')
    expect(machine.getAttribute('aria-labelledby')).toContain('agent-create-edge-label')
    expect(machine.getAttribute('aria-required')).toBe('true')
    expect(machine.getAttribute('aria-describedby')).toContain('agent-create-edge-hint')
    expect(machine.getAttribute('aria-describedby')).toContain('agent-create-edge-stale')
    const staleNotice = el.querySelector('#agent-create-edge-stale.k-inline-notification--warning[role="status"]')
    expect(staleNotice?.textContent).toContain('Showing the last loaded machines')

    const credential = el.querySelector<HTMLButtonElement>('#agent-create-harnesscred')!
    expect(el.querySelector('#agent-create-harnesscred-label')?.tagName).toBe('SPAN')
    expect(credential.getAttribute('aria-labelledby')).toContain('agent-create-harnesscred-label')
    expect(credential.getAttribute('aria-required')).toBe('true')
    expect(credential.getAttribute('aria-describedby')).toContain('agent-create-harnesscred-hint')

    expect(el.querySelector('#agent-create-execution-heading')?.textContent).toBe('Execution')
    expect(el.querySelector('.k-create-guidance')?.textContent).toContain('After you create the agent')

    machine.click()
    await settle(el)
    expect([...document.querySelectorAll<HTMLElement>('.k-form-select__option')].some(option => option.textContent?.includes('build-01'))).toBe(true)

    const loadEdges = vi.spyOn(store, 'load').mockResolvedValue()
    el.querySelector<HTMLButtonElement>('#agent-create-edge-stale button')!.click()
    expect(loadEdges).toHaveBeenCalledWith('edges')
  })

  it('announces agent validation and locks the create surface while submitting', async () => {
    const request = deferred<{ metadata: { name: string }; spec: { displayName: string } }>()
    const createAgent = vi.fn(() => request.promise)
    const api = stubApi({ createAgent })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    markAuthoritative(store, 'agents', 'credentials')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api })
    const form = el.querySelector<HTMLFormElement>('form')!

    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el)
    expect(el.querySelector('#agent-create-name-error[role="alert"]')?.textContent).toContain('required')
    expect(el.querySelector('#agent-create-name')?.getAttribute('aria-invalid')).toBe('true')
    expect(el.querySelector('#agent-create-model')?.getAttribute('aria-describedby')).toContain('agent-create-model-error')

    const name = el.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'nova'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await chooseFormSelect(el, '#agent-create-model', 'main')
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el)
    expect(form.getAttribute('aria-busy')).toBe('true')
    expect([...form.querySelectorAll('input:not([type=hidden]), select, textarea, button')].every((control) => (control as HTMLInputElement).disabled)).toBe(true)

    request.resolve({ metadata: { name: 'nova' }, spec: { displayName: 'Nova' } })
    await settle(el, 5)
    expect(form.getAttribute('aria-busy')).toBe('false')
  })

  it('waits for authoritative agent and credential snapshots before rendering the agent form', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }]
    markAuthoritative(store, 'credentials')

    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api })

    expect(el.querySelector('form')).toBeNull()
    expect(el.querySelector('.agents-state-loading')?.textContent).toContain('Loading existing agents and model credentials')
    expect(el.textContent).not.toContain('This workspace needs a chat model')
  })

  it('shows retryable first-load errors instead of treating missing agent prerequisites as empty', async () => {
    const api = stubApi()
    const credentialStore = makeStore(api)
    markAuthoritative(credentialStore, 'agents')
    credentialStore.credentials.loaded = true
    credentialStore.credentials.error = 'credential API unavailable'
    const credentialLoad = vi.spyOn(credentialStore, 'load').mockResolvedValue()
    const credentialError = await mount<AgentCreateWizard>('agents-agent-create', { store: credentialStore, api })

    expect(credentialError.querySelector('form')).toBeNull()
    expect(credentialError.querySelector('.agents-state-error')?.textContent).toContain('Could not load model credentials')
    credentialError.querySelector<HTMLButtonElement>('.agents-state-error button')!.click()
    expect(credentialLoad).toHaveBeenCalledWith('credentials')

    const agentStore = makeStore(api)
    markAuthoritative(agentStore, 'credentials')
    agentStore.agents.loaded = true
    agentStore.agents.error = 'agent API unavailable'
    const agentLoad = vi.spyOn(agentStore, 'load').mockResolvedValue()
    const agentError = await mount<AgentCreateWizard>('agents-agent-create', { store: agentStore, api })

    expect(agentError.querySelector('form')).toBeNull()
    expect(agentError.querySelector('.agents-state-error')?.textContent).toContain('Could not load existing agents')
    agentError.querySelector<HTMLButtonElement>('.agents-state-error button')!.click()
    expect(agentLoad).toHaveBeenCalledWith('agents')

    const runnerLoadingStore = makeStore(api)
    runnerLoadingStore.credentials.data = [{ name: 'claude', provider: 'claude-code' }]
    markAuthoritative(runnerLoadingStore, 'agents', 'credentials')
    const runnerLoading = await mount<AgentCreateWizard>('agents-agent-create', { store: runnerLoadingStore, api })
    runnerLoading.querySelector<HTMLInputElement>('[data-use-coding-harness]')!.click()
    await settle(runnerLoading, 2)
    expect(runnerLoading.querySelector('.k-inline-notification--error[role="alert"]')).toBeNull()
    expect(runnerLoading.querySelector('[aria-labelledby=agent-create-execution-heading]')?.textContent).toContain('Loading machines…')
    expect(runnerLoading.querySelector('.k-create-guidance')?.textContent).not.toContain('Join a Linux or macOS machine')

    const runnerErrorStore = makeStore(api)
    runnerErrorStore.credentials.data = [{ name: 'claude', provider: 'claude-code' }]
    markAuthoritative(runnerErrorStore, 'agents', 'credentials')
    runnerErrorStore.edges.loaded = true
    runnerErrorStore.edges.error = 'edge API unavailable'
    const runnerError = await mount<AgentCreateWizard>('agents-agent-create', { store: runnerErrorStore, api })
    runnerError.querySelector<HTMLInputElement>('[data-use-coding-harness]')!.click()
    await settle(runnerError, 2)
    expect(runnerError.querySelector('.k-inline-notification--error[role="alert"]')?.textContent).toContain('Could not load machines')
    expect(runnerError.querySelector('[aria-labelledby=agent-create-execution-heading] button.k-inline-notification__action')?.textContent).toContain('Retry')
    expect(runnerError.querySelector('.k-create-guidance')?.textContent).not.toContain('Join a Linux or macOS machine')
  })

  it('routes a missing chat credential through a resumable chat setup event', async () => {
    const api = stubApi()
    const store = makeStore(api)
    markAuthoritative(store, 'agents', 'credentials')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api })
    const events = mountedByElement.get(el)!.events

    expect(el.querySelector('form')).not.toBeNull()
    expect(el.textContent).toContain('This workspace needs a chat model')
    expect(el.querySelector<HTMLButtonElement>('button[type=submit]')?.disabled).toBe(true)
    const recovery = el.querySelector<HTMLButtonElement>('.k-dashboard-action')!
    expect(recovery.textContent).toContain('Connect a model')
    recovery.click()
    expect(events['add-credential']?.[0]).toEqual(expect.objectContaining({
      family: 'chat',
      draft: expect.objectContaining({ backendType: 'model' }),
      store,
      authorityEpoch: 0,
      createSession: 0,
    }))
    expect(JSON.stringify(events['add-credential']?.[0])).not.toContain('secret')
  })

  it('routes a missing harness identity to harness setup with the full non-secret agent draft', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.edges.data = [{ kind: 'LinuxServer', name: 'build-01', connected: true }]
    markAuthoritative(store, 'agents', 'credentials', 'edges')
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api })
    const events = mountedByElement.get(el)!.events

    el.querySelector<HTMLInputElement>('#agent-create-name')!.value = 'draft-agent'
    el.querySelector<HTMLInputElement>('#agent-create-name')!.dispatchEvent(new InputEvent('input', { bubbles: true }))
    el.querySelector<HTMLTextAreaElement>('textarea[aria-labelledby="agent-create-system-prompt-label"]')!.value = 'Keep this instruction.'
    el.querySelector<HTMLTextAreaElement>('textarea[aria-labelledby="agent-create-system-prompt-label"]')!.dispatchEvent(new InputEvent('input', { bubbles: true }))
    el.querySelector<HTMLInputElement>('[data-use-coding-harness]')!.click()
    await settle(el, 2)
    el.querySelector<HTMLButtonElement>('#agent-create-harnesscred-empty .k-dashboard-action')!.click()

    expect(events['add-credential']?.[0]).toEqual(expect.objectContaining({
      family: 'harness',
      draft: expect.objectContaining({
        name: 'draft-agent',
        backendType: 'harness',
        systemPrompt: 'Keep this instruction.',
        harnessWorkspace: 'persistent',
      }),
      store,
    }))
    expect(events['add-credential']?.[0]).not.toHaveProperty('secret')
  })

  it('keeps connection type selection and creation in hash-owned surfaces', async () => {
    const createConnection = vi.fn().mockResolvedValue({ metadata: { name: 'gh' }, spec: { type: 'github' } })
    const api = stubApi({ createConnection })
    const store = makeStore(api)
    store.connections.loaded = true
    const picker = await mount<Connections>('agents-connections', { store, api, routeOwned: true, createRoute: true, createType: '' })
    const routes = mountedByElement.get(picker)!.navigations
    expect(picker.querySelector('.agents-conn-form')).toBeNull()
    picker.querySelector<HTMLButtonElement>('.agents-conn-tile')!.click()
    expect(routes).toEqual([{ kind: 'create', resource: 'connection', type: 'github' }])

    const formSurface = await mount<Connections>('agents-connections', { store, api, routeOwned: true, createRoute: true, createType: 'github' })
    expect(formSurface.querySelector('.agents-conn-form')).not.toBeNull()
    expect(formSurface.querySelector('.k-create-surface--guided .k-create-guidance')).not.toBeNull()
    const name = formSurface.querySelector<HTMLInputElement>('input[name=name]')!
    name.value = 'gh'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    const token = formSurface.querySelector<HTMLInputElement>('input[name=token]')!
    token.value = 'secret'
    token.dispatchEvent(new InputEvent('input', { bubbles: true }))
    formSurface.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(formSurface, 5)
    expect(createConnection).not.toHaveBeenCalled()
    expect(formSurface.querySelector('.k-wizard-steps [aria-current=step]')?.textContent).toBe('Review')
    expect(formSurface.querySelector('.agents-connection-review')?.textContent).not.toContain('secret')
    formSurface.querySelector<HTMLButtonElement>('.agents-step-back')!.click()
    await settle(formSurface)
    expect(name.value).toBe('gh')
    expect(token.value).toBe('secret')
    formSurface.querySelector<HTMLButtonElement>('.agents-step-back')!.click()
    await settle(formSurface)
    expect(formSurface.querySelector('.k-wizard-steps [aria-current=step]')?.textContent).toBe('Type')
    formSurface.querySelector<HTMLButtonElement>('.agents-conn-tile')!.click()
    await settle(formSurface)
    expect(formSurface.querySelector<HTMLInputElement>('input[name=token]')?.value).toBe('secret')
    const form = formSurface.querySelector<HTMLFormElement>('form')!
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(formSurface)
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(formSurface, 5)
    expect(createConnection).toHaveBeenCalledWith(expect.objectContaining({ name: 'gh', type: 'github', secret: 'secret' }))
  })

  it('reacts when a platform OAuth app becomes available after the form mounts', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.connections.loaded = true
    const el = await mount<Connections>('agents-connections', { store, api, routeOwned: true, createRoute: true, createType: 'github' })
    const oauthMode = [...el.querySelectorAll<HTMLButtonElement>('.agents-modebtn')].find(button => button.textContent?.includes('OAuth app'))!
    expect(el.querySelector('.agents-cap-fs > legend')?.textContent).toBe('Authentication mode')
    expect(oauthMode.closest('fieldset')?.tagName).toBe('FIELDSET')
    oauthMode.click()
    await settle(el, 2)

    expect(el.querySelector('input[name=clientID]')).not.toBeNull()
    expect(el.querySelector('input[name=clientSecret]')).not.toBeNull()

    store.oauthApps = new Set(['github'])
    store.dispatchEvent(new Event('change'))
    await settle(el, 4)

    expect(el.querySelector('input[name=clientID]')).toBeNull()
    expect(el.querySelector('input[name=clientSecret]')).toBeNull()
    expect(el.querySelector('.agents-platform-note')).not.toBeNull()
  })

  it('renders toolset creation separately from the connections collection', async () => {
    const createToolset = vi.fn().mockResolvedValue({ metadata: { name: 'dev-tools' }, spec: { connections: [] } })
    const api = stubApi({ createToolset })
    const store = makeStore(api)
    store.connections.data = [{ metadata: { name: 'gh' }, spec: { type: 'github' } }]
    markAuthoritative(store, 'connections')
    const el = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true, createRoute: true })
    expect(el.querySelector('table')).toBeNull()
    expect(el.querySelector('.k-create-surface--guided .k-create-guidance')).not.toBeNull()
    const name = el.querySelector<HTMLInputElement>('input[name=name]')!
    name.value = 'dev-tools'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(el, 5)
    expect(createToolset).toHaveBeenCalledWith(expect.objectContaining({ name: 'dev-tools', connections: [], families: ['core'] }))
  })

  it('renders provider creation separately and saves only the provider connection', async () => {
    const saveCredential = vi.fn().mockResolvedValue({ name: 'main', provider: 'openai-compatible', model: '', isConnection: true })
    const api = stubApi({ probeCredentialDraft: () => Promise.resolve({ ok: true, latencyMS: 1 }), saveCredential, catalog: () => Promise.resolve([]) })
    const store = makeStore(api)
    const el = await mount<Models>('agents-models', { store, api, routeOwned: true, createRoute: true })
    expect(el.querySelector('.k-create-page')).not.toBeNull()
    expect(el.querySelectorAll('.k-back-action')).toHaveLength(1)
    expect(el.querySelector('.k-create-header .k-create-title')?.textContent).toContain('Connect provider')
    expect(el.querySelector('.k-create-surface')).not.toBeNull()
    expect(el.querySelector('.k-model-grid')).toBeNull()
    const input = el.querySelector<HTMLInputElement>('[name="apiKey"]')!
    input.value = 'secret'
    input.dispatchEvent(new InputEvent('input', { bubbles: true }))
    await settle(el)
    expect(el.querySelector('#model-id')).toBeNull()
    ;[...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'Verify & connect')!.click()
    await settle(el, 12)
    expect(saveCredential).toHaveBeenCalledOnce()
    expect(saveCredential.mock.calls[0][0]).toMatchObject({ model: '', apiKey: 'secret' })
    expect(saveCredential.mock.calls[0][0]).not.toHaveProperty('connectionRef')
    expect(mountedByElement.get(el)?.events['create-success']).toEqual([
      expect.objectContaining({ resource: 'model', name: 'main', item: expect.objectContaining({ isConnection: true }) }),
    ])
    expect(store.credentials.data).toContainEqual(expect.objectContaining({ name: 'main', isConnection: true }))
  })})

describe('route-owned collection affordances', () => {
  it('shows guided first-run journeys only after each collection is authoritative', async () => {
    const usage = {
      windowDays: 30,
      total: { key: 'total', runs: 0, errors: 0, inputTokens: 0, outputTokens: 0, usdMicros: 0, latencyP50MS: 0, latencyP95MS: 0 },
      byAgent: [],
      byModel: [],
      series: [],
    }
    const api = stubApi({ catalog: () => Promise.resolve([]), usage: () => Promise.resolve(usage) })

    const pendingStore = makeStore(api)
    const pendingAgents = await mount('agents-agents-list', { store: pendingStore, api })
    expect(pendingAgents.querySelector('.agents-new-agent')).toBeNull()

    const agentsStore = makeStore(api)
    agentsStore.agents.loaded = true
    agentsStore.agents.hasSnapshot = true
    agentsStore.credentials.loaded = true
    agentsStore.credentials.hasSnapshot = true
    const agents = await mount('agents-agents-list', { store: agentsStore, api })
    expect(agents.querySelector('.agents-new-agent')).not.toBeNull()

    const modelsStore = makeStore(api)
    modelsStore.credentials.loaded = true
    modelsStore.credentials.hasSnapshot = true
    const models = await mount<Models>('agents-models', { store: modelsStore, api, routeOwned: true })
    expect(models.querySelector('.k-first-run')).not.toBeNull()

    const connectionsStore = makeStore(api)
    connectionsStore.connections.loaded = true
    connectionsStore.connections.hasSnapshot = true
    const connections = await mount<Connections>('agents-connections', { store: connectionsStore, api, routeOwned: true })
    expect(connections.querySelector('.k-first-run')).not.toBeNull()

    const toolsetsStore = makeStore(api)
    toolsetsStore.toolsets.loaded = true
    toolsetsStore.toolsets.hasSnapshot = true
    const toolsets = await mount<Toolsets>('agents-toolsets', { store: toolsetsStore, api, routeOwned: true })
    expect(toolsets.querySelector('.k-first-run')).not.toBeNull()
    expect([...toolsets.querySelectorAll('.k-first-run__step-status')].map(step => step.textContent?.trim()))
      .toEqual(['Current step:', 'Upcoming step:', 'Upcoming step:'])
  })

  it('keeps core and edge-only toolset creation available when there are no connections', async () => {
    const api = stubApi()
    const store = makeStore(api)
    markAuthoritative(store, 'toolsets', 'connections')
    const el = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true })
    const routes = mountedByElement.get(el)!.navigations

    expect(el.textContent).toContain('Start with core and edge capabilities now')
    const createToolset = [...el.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent?.includes('Create toolset'))!
    const createConnection = [...el.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent?.includes('Create connection'))!
    expect(createToolset).toBeTruthy()
    expect(createConnection).toBeTruthy()
    createToolset.click()
    createConnection.click()
    expect(routes).toEqual([
      { kind: 'create', resource: 'toolset' },
      { kind: 'create', resource: 'connection' },
    ])
  })

  it('does not infer missing tool connections before their slice is authoritative', async () => {
    const api = stubApi()
    const store = makeStore(api)
    markAuthoritative(store, 'toolsets')
    const el = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true })

    expect(el.querySelector('.k-inline-notification--info')?.textContent).toContain('Tool connections have not loaded yet')
    expect(el.textContent).not.toContain('External tool connections are optional')
    expect(el.textContent).toContain('Available external tool connections will appear after they finish loading')
    expect(el.textContent).not.toContain('No external connections are configured yet')
    expect([...el.querySelectorAll('button')].some((button) => button.textContent?.includes('Create toolset'))).toBe(true)
  })

  it('keeps toolset creation available when optional connection discovery fails', async () => {
    const api = stubApi()
    const store = makeStore(api)
    markAuthoritative(store, 'toolsets')
    store.connections.loaded = true
    store.connections.error = 'connection API unavailable Authorization: Bearer sensitive-backend-detail'
    const load = vi.spyOn(store, 'load').mockResolvedValue()
    const el = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true })

    const failure = el.querySelector<HTMLElement>('.k-inline-notification--error')!
    expect(failure.textContent).toContain('Tool connections could not be loaded')
    expect(failure.textContent).toContain('Technical details')
    expect(failure.textContent).toContain('connection API unavailable')
    expect(failure.textContent).not.toContain('sensitive-backend-detail')
    expect([...el.querySelectorAll('button')].some((button) => button.textContent?.includes('Create toolset'))).toBe(true)
    failure.querySelector<HTMLButtonElement>('.k-inline-notification__action')!.click()
    expect(load).toHaveBeenCalledWith('connections')
  })

  it('navigates New agent instead of opening collection-local state', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true
    const el = await mount('agents-agents-list', { store, api })
    const routes = mountedByElement.get(el)!.navigations
    el.querySelector<HTMLButtonElement>('.agents-new-agent')!.click()
    expect(routes).toEqual([{ kind: 'create', resource: 'agent' }])
    expect(el.querySelector('agents-agent-create')).toBeNull()
  })
})

it('creates a reusable visualization toolset without an external connection', async () => {
  const createToolset = vi.fn().mockResolvedValue({ metadata: { name: 'charts' }, spec: { families: ['core', 'visualization'] } })
  const api = stubApi({ createToolset })
  const store = makeStore(api)
  markAuthoritative(store, 'toolsets', 'connections')
  const el = await mount<Toolsets>('agents-toolsets', { store, api, routeOwned: true, createRoute: true })
  const name = el.querySelector<HTMLInputElement>('input[name=name]')!
  name.value = 'charts'
  name.dispatchEvent(new Event('input'))
  const label = [...el.querySelectorAll('label')].find(item => item.textContent?.includes('Visualize data'))!
  const enabled = label.querySelector<HTMLInputElement>('input')!
  enabled.checked = true
  enabled.dispatchEvent(new Event('change'))
  await settleVue()
  el.querySelector('form')!.dispatchEvent(new Event('submit'))
  await settleVue(4, 1)
  expect(createToolset).toHaveBeenCalledWith(expect.objectContaining({ name: 'charts', connections: [], families: ['core', 'visualization'] }))
})

it.each([false, true])('creates an agent with the final template inputs and schedule (edited instructions: %s)', async (edited) => {
  const catalog = (await import('../../public/agent-templates.json')).default as unknown[]
  const createAgentFromTemplate = vi.fn(async (agent: { metadata: { name: string } }, schedules: { metadata: { name: string } }[]) => ({
    agent, createdSchedules: schedules.map(s => s.metadata.name), warnings: [],
  }))
  const api = stubApi({ agentTemplates: async () => catalog, createAgentFromTemplate })
  const store = makeStore(api)
  store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'gpt-4.1' }]
  store.connections.data = [
    { metadata: { name: 'tg' }, spec: { type: 'telegram', displayName: 'Team Telegram' } },
    { metadata: { name: 'searx' }, spec: { type: 'websearch' } },
    { metadata: { name: 'gh' }, spec: { type: 'github' } },
  ] as never
  markAuthoritative(store, 'agents', 'credentials', 'connections', 'edges')
  const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api, initialTemplate: 'news-reporter' })
  await settle(el)

  // The template preselects itself, suggests a name, locks the backend and
  // asks for exactly its parameters and slots.
  expect(el.querySelector('[data-selected-template]')?.textContent).toContain('News reporter')
  expect(el.querySelector<HTMLInputElement>('#agent-create-name')!.value).toBe('news-reporter')
  expect(el.querySelector('#agent-create-edge')).toBeNull()
  expect(el.querySelector('#agent-create-param-topics')).not.toBeNull()
  expect(el.querySelector('#agent-create-slot-websearch')).not.toBeNull()
  expect(el.querySelector('#agent-create-channel-label')?.textContent).toContain('*')
  // The existing instructions field receives the template and tracks its inputs.
  const instructions = el.querySelector<HTMLTextAreaElement>('textarea[aria-labelledby="agent-create-system-prompt-label"]')!
  expect(instructions.value).toContain('news reporter')
  el.querySelector<HTMLButtonElement>('#agent-create-slot-websearch')!.click()
  await settleVue()
  const slotOptions = [...document.querySelectorAll<HTMLElement>('.k-form-select__option')].map(o => o.textContent?.trim())
  expect(slotOptions).toContain('searx')
  expect(slotOptions).not.toContain('gh')
  document.body.click()
  await settleVue()

  // Submitting without the required pieces names each one and writes nothing.
  el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await settle(el)
  expect(el.querySelector('#agent-create-param-topics-error')?.textContent).toContain('Topics')
  expect(el.querySelector('#agent-create-channel-error')?.textContent).toContain('channel')
  expect(createAgentFromTemplate).not.toHaveBeenCalled()

  const customInstruction = 'Use a numbered summary.'
  if (edited) {
    instructions.closest('details')!.open = true
    instructions.focus()
    await settleVue()
    expect(instructions.value).toContain('{{topics}}')
    instructions.value += `\n${customInstruction}`
    instructions.dispatchEvent(new InputEvent('input', { bubbles: true }))
    instructions.blur()
  }
  const topics = el.querySelector<HTMLInputElement>('#agent-create-param-topics')!
  for (const topic of ['kcp', 'kubernetes']) {
    topics.value = topic
    topics.dispatchEvent(new InputEvent('input', { bubbles: true }))
    topics.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
  }
  const saturday = el.querySelector<HTMLButtonElement>('[aria-label="Saturday"]')!
  saturday.click()
  const time = el.querySelector<HTMLInputElement>('input[type="time"]')!
  await settleVue()
  time.value = ''
  time.dispatchEvent(new InputEvent('input', { bubbles: true }))
  await settleVue()
  expect(time.value).toBe('')
  expect(saturday.disabled).toBe(true)
  time.value = '09:15'
  time.dispatchEvent(new InputEvent('input', { bubbles: true }))
  await chooseFormSelect(el, '#agent-create-model', 'openai')
  await chooseFormSelect(el, '#agent-create-channel', 'Team Telegram')
  await chooseFormSelect(el, '#agent-create-slot-websearch', 'searx')
  expect(el.querySelector('[data-generated-instructions]')?.textContent).toContain('- kcp')
  const editCron = [...el.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Edit cron')!
  editCron.click()
  await settleVue()
  const cron = el.querySelector<HTMLInputElement>('#agent-create-param-digestTime')!
  expect(cron.value).toBe('15 9 * * 1,2,3,4,5,6')
  for (const invalid of ['CRON_TZ=UTC', '@every 3000000h', 'CRON_TZ=america/new_york 0 8 * * *']) {
    cron.value = invalid
    cron.dispatchEvent(new InputEvent('input', { bubbles: true }))
    el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue()
    expect(el.querySelector('#agent-digest-cron-error')?.textContent).toContain('valid cron')
    expect(createAgentFromTemplate).not.toHaveBeenCalled()
  }
  cron.value = '15 9 * * MON-SAT'
  cron.dispatchEvent(new InputEvent('input', { bubbles: true }))
  if (edited) {
    instructions.focus()
    await settleVue()
    expect(instructions.value).toContain('{{topics}}')
    expect(instructions.value).toContain(customInstruction)
    instructions.blur()
  }
  el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await settle(el)

  const remaining = [...el.querySelectorAll('.agents-fielderr, .k-inline-notification--error')].map(n => n.textContent?.trim())
  expect(remaining, 'no validation errors should remain').toEqual([])
  expect(confirmState.message).toContain('news-reporter-daily-digest')
  expect(confirmState.message).toContain('Background runs can use web, searx')
  expect(confirmState.message).toContain('Posts to Team Telegram.')
  expect(createAgentFromTemplate).not.toHaveBeenCalled()
  resolveConfirm(true)
  await settle(el)
  expect(createAgentFromTemplate).toHaveBeenCalledTimes(1)
  const [agent, schedules] = createAgentFromTemplate.mock.calls[0] as unknown as [
    { metadata: { name: string }; spec: Record<string, any> },
    { metadata: { name: string }; spec: Record<string, any> }[],
  ]
  expect(agent.metadata.name).toBe('news-reporter')
  expect(agent.spec.systemPrompt).toContain('- kcp\n- kubernetes')
  expect(agent.spec.systemPrompt).not.toContain('{{topics}}')
  if (edited) expect(agent.spec.systemPrompt).toContain(customInstruction)
  expect(agent.spec.backend).toEqual({ type: 'model', model: { credentials: { chat: 'openai' } } })
  expect(agent.spec.channels).toEqual([{ name: 'primary', connectionRef: 'tg', primary: true }])
  expect(agent.spec.tools.interactive.connections).toEqual(['searx'])
  expect(schedules.map(s => s.metadata.name)).toEqual(['news-reporter-daily-digest'])
  expect(schedules[0].spec).toMatchObject({ agentRef: 'news-reporter', type: 'cron', channelRef: 'primary', schedule: '15 9 * * MON-SAT' })
})
