import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiClient } from '../api'
import { AgentsElement } from '../element'
import { AppStore } from '../store'
import type { Credential, RailgridContext } from '../types'
import { agentFixture, modelBackend } from './helpers'
import { settleVue } from './vue-helper'

if (!customElements.get('railgrid-provider-agents')) customElements.define('railgrid-provider-agents', AgentsElement)
const context = (workspace = 'workspace'): RailgridContext => ({
  basePath: '/ui/providers/agents', orgUUID: 'org', workspaceUUID: workspace,
  token: 'token-a', user: { sub: 'alice' },
})
const mounted: AgentsElement[] = []
beforeEach(() => {
  vi.spyOn(AppStore.prototype, 'loadAll').mockImplementation(() => undefined)
  vi.spyOn(AppStore.prototype, 'connect').mockImplementation(() => undefined)
})
afterEach(() => { mounted.splice(0).forEach(element => element.remove()); vi.restoreAllMocks() })
async function mountOverlay(options: {
  defaultCredential?: string
  credentials?: Credential[]
  sessionModel?: any
  saveSessionModel?: (name: string, session: string, preferences: any) => Promise<any>
  sessions?: any[]
  messages?: any[]
} = {}) {
  history.replaceState(null, '', '#/linuxservers/warehouse-west')
  const element = document.createElement('railgrid-provider-agents') as AgentsElement
  element.overlayState = { agent: 'scout', pageContext: '{"resource":"warehouse-west"}' }
  element.railgridContext = context()
  document.body.appendChild(element)
  mounted.push(element)
  await settleVue(6)
  vi.spyOn(element.api!, 'canConfigureChatModel').mockResolvedValue(true)
  element.store!.credentials.data = options.credentials || [{ name: 'openai', provider: 'openai', model: 'gpt-test' }]
  element.store!.credentials.loaded = element.store!.credentials.hasSnapshot = true
  vi.spyOn(element.api!, 'getSessionModel').mockResolvedValue(options.sessionModel || { selectedCredential: 'openai', modelCredential: '', effortOptions: [], defaultEffort: '' })
  vi.spyOn(element.api!, 'saveSessionModel').mockImplementation(options.saveSessionModel || (async (_name, _session, preferences) => preferences))
  vi.spyOn(element.api!, 'listSessions').mockResolvedValue(options.sessions || [])
  vi.spyOn(element.api!, 'listMessagePage').mockResolvedValue({ items: options.messages || [] })
  vi.spyOn(element.api!, 'listRuns').mockResolvedValue({ items: [] })
  element.store!.agents.data = [agentFixture('scout', { backend: modelBackend(options.defaultCredential || 'openai') })]
  element.store!.agents.loaded = element.store!.agents.hasSnapshot = true
  element.store!.dispatchEvent(new Event('change'))
  await settleVue(12)
  return element
}
async function send(element: HTMLElement, text: string) {
  const textarea = element.querySelector('textarea')!
  textarea.value = text
  textarea.dispatchEvent(new Event('input'))
  await settleVue()
  element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
  await settleVue(10)
}

describe('persistent agent overlay', () => {
  it('does not own browser routing and preserves a draft across page navigation', async () => {
    const element = await mountOverlay()
    expect(location.hash).toBe('#/linuxservers/warehouse-west')
    expect(element.route).toEqual({ kind: 'agent', name: 'scout', tab: 'chat' })
    const input = element.querySelector('textarea')!
    input.value = 'Keep this draft'
    input.dispatchEvent(new Event('input'))
    await settleVue()
    element.overlayState = { agent: 'scout', pageContext: '{"resource":"project"}' }
    history.replaceState(null, '', '#/projects/inventory')
    window.dispatchEvent(new Event('hashchange'))
    element.railgridContext = context()
    await settleVue(10)
    expect(element.querySelector('textarea')).toBe(input)
    expect(input.value).toBe('Keep this draft')
    expect(element.querySelector('#overlay-agent-persona-heading')).toBeNull()
    expect(element.route?.kind).toBe('agent')
    expect(location.hash).toBe('#/projects/inventory')
    element.overlayState = { agent: 'scout' }
    await settleVue()
    expect(element.querySelector('textarea')).toBe(input)
    expect(input.value).toBe('Keep this draft')
  })

  it('shows only chat without mounting a thread rail or workbench', async () => {
    const element = await mountOverlay()
    expect(element.querySelector('.agents-conversation-rail-shell')).toBeNull()
    expect(element.querySelector('.agents-overlay-threads')).toBeNull()
    element.dispatchEvent(new Event('railgrid-overlay-toggle-threads'))
    await settleVue()
    expect(element.querySelector('.agents-overlay-threads')).toBeNull()
    const input = element.querySelector('textarea')!
    element.dispatchEvent(new Event('railgrid-overlay-focus-composer'))
    await settleVue()
    expect(document.activeElement).toBe(input)
  })

  it('opens the panel only for a valid send, never for focus or an empty submission', async () => {
    vi.spyOn(ApiClient.prototype, 'chatStream').mockImplementation(async function* () {})
    const element = await mountOverlay()
    element.overlayState = { agent: 'scout', composerOnly: true }
    await settleVue()
    const engage = vi.fn()
    element.addEventListener('railgrid-overlay-engage', engage)
    element.querySelector('textarea')!.focus()
    await send(element, '   ')
    expect(engage).not.toHaveBeenCalled()
    await send(element, 'Inspect this instance')
    expect(engage).toHaveBeenCalledTimes(1)
  })

  it('keeps one composer and draft while collapsing to the assistant bar', async () => {
    const element = await mountOverlay()
    const input = element.querySelector('textarea')!
    input.value = 'Keep this draft'
    input.dispatchEvent(new Event('input'))
    const engage = vi.fn()
    element.addEventListener('railgrid-overlay-engage', engage)
    element.overlayState = { agent: 'scout', composerOnly: true }
    await settleVue()
    expect(element.querySelector('.agents-chat-shell--bar')).not.toBeNull()
    expect(engage).not.toHaveBeenCalled()
    expect(element.querySelector('textarea')).toBe(input)
    expect(input.value).toBe('Keep this draft')
    input.dispatchEvent(new Event('focus'))
    expect(engage).not.toHaveBeenCalled()
    element.overlayState = { agent: 'scout', composerOnly: false }
    await settleVue()
    expect(element.querySelector('.agents-chat-shell--bar')).toBeNull()
    expect(element.querySelector('textarea')).toBe(input)
  })

  it('hands a draft to the ordinary Agents chat in the same session', async () => {
    const overlay = await mountOverlay()
    const input = overlay.querySelector('textarea')!
    input.value = 'Continue in Agents'
    input.dispatchEvent(new Event('input'))
    await settleVue()
    overlay.dispatchEvent(new Event('railgrid-chat-handoff'))
    overlay.remove()
    history.replaceState(null, '', '#/agents/scout/chat')
    const normal = document.createElement('railgrid-provider-agents') as AgentsElement
    normal.railgridContext = context()
    document.body.appendChild(normal)
    mounted.push(normal)
    await settleVue(6)
    vi.spyOn(normal.api!, 'listSessions').mockResolvedValue([])
    vi.spyOn(normal.api!, 'listMessagePage').mockResolvedValue({ items: [] })
    vi.spyOn(normal.api!, 'listRuns').mockResolvedValue({ items: [] })
    normal.store!.credentials.data = [{ name: 'openai', provider: 'openai', model: 'test-model' }]
    normal.store!.credentials.loaded = normal.store!.credentials.hasSnapshot = true
    normal.store!.agents.data = [agentFixture()]
    normal.store!.agents.loaded = normal.store!.agents.hasSnapshot = true
    normal.store!.dispatchEvent(new Event('change'))
    await settleVue(12)
    expect(normal.querySelector('textarea')?.value).toBe('Continue in Agents')
    expect(normal.route).toEqual({ kind: 'agent', name: 'scout', tab: 'chat' })
  })

  it('captures page context once and keeps the live stream through context updates, then revokes it on workspace change', async () => {
    let release!: () => void
    const gate = new Promise<void>(resolve => { release = resolve })
    let signal: AbortSignal | undefined
    const stream = vi.spyOn(ApiClient.prototype, 'chatStream').mockImplementation(async function* (_agent, _message, session, abort) {
      signal = abort
      yield { event: 'start', data: JSON.stringify({ runID: 'run-a', sessionID: session }) }
      yield { event: 'delta', data: JSON.stringify({ text: 'Checking the edge' }) }
      await gate
    })
    const element = await mountOverlay()
    await send(element, 'Why is this failing?')
    expect(stream).toHaveBeenCalledTimes(1)
    expect(stream.mock.calls[0]![1]).toContain('"resource":"warehouse-west"')
    const session = stream.mock.calls[0]![2]
    expect(session).toBeTruthy()
    element.overlayState = { agent: 'scout', pageContext: '{"resource":"another-page"}' }
    element.railgridContext = context()
    await settleVue(10)
    element.dispatchEvent(new Event('railgrid-overlay-toggle-threads'))
    await settleVue()
    expect(signal?.aborted).toBe(false)
    expect(stream.mock.calls[0]![1]).not.toContain('another-page')
    element.railgridContext = context('other-workspace')
    await settleVue(10)
    expect(signal?.aborted).toBe(true)
    expect(element.textContent).not.toContain('Why is this failing?')
    release()
    await settleVue()
  })

  it('sends only the user message when page sharing is paused', async () => {
    const stream = vi.spyOn(ApiClient.prototype, 'chatStream').mockImplementation(async function* () {})
    const element = await mountOverlay()
    element.overlayState = { agent: 'scout' }
    await settleVue()
    await send(element, 'No page context')
    expect(stream.mock.calls[0]![1]).toBe('No page context')
  })
})

describe('overlay model recovery', () => {
  function models(element: AgentsElement, data: Credential[]) {
    element.store!.credentials.data = data
    element.store!.credentials.loaded = element.store!.credentials.hasSnapshot = true
    element.store!.dispatchEvent(new Event('change'))
  }
  it('replaces stale controls and preserves the draft through model removal and restoration', async () => {
    const element = await mountOverlay()
    const stream = vi.spyOn(element.api!, 'chatStream')
    const input = element.querySelector('textarea')!
    input.value = 'Keep this unsent draft'; input.dispatchEvent(new Event('input')); await settleVue()
    element.querySelector<HTMLButtonElement>('.k-ai-model-reasoning-picker__trigger')!.click(); await settleVue()
    expect(document.querySelector('[role=dialog]')).not.toBeNull()
    models(element, []); await settleVue(12)
    expect(element.textContent).toContain('Connect a model to continue')
    expect(element.querySelector('textarea')).toBeNull()
    expect(element.querySelector('.k-ai-model-reasoning-picker')).toBeNull()
    expect(element.querySelector('form.agents-composer')).toBeNull()
    expect(document.querySelector('[role=dialog]')).toBeNull()
    models(element, [{ name: 'openai', provider: 'openai', model: 'restored' }]); await settleVue(10)
    expect(element.querySelector('textarea')?.value).toBe('Keep this unsent draft')
    expect(stream).not.toHaveBeenCalled()
  })
  it('requires an explicit replacement selection and keeps the same conversation', async () => {
    const element = await mountOverlay()
    const save = vi.spyOn(element.api!, 'saveSessionModel').mockImplementation(async (_name, _session, preferences) => preferences)
    models(element, [{ name: 'replacement', provider: 'openai', model: 'new-model' }]); await settleVue(12)
    expect(element.textContent).toContain('Choose a model to continue')
    expect(save).not.toHaveBeenCalled()
    const selector = element.querySelector<HTMLSelectElement>('.agents-model-recovery select')!
    const use = [...element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent === 'Use model')!
    expect(use.disabled).toBe(true)
    selector.value = 'replacement'; selector.dispatchEvent(new Event('change')); await settleVue()
    use.click(); await settleVue(12)
    expect(save).toHaveBeenCalledOnce()
    expect(save.mock.calls[0][2]).toMatchObject({ modelCredential: 'replacement' })
    expect(element.querySelector('.agents-model-recovery')).toBeNull()
    expect(element.querySelector('textarea')).toBeTruthy()
  })
  it('offers the current Agent default for a deleted explicit chat model without sending or replacing context', async () => {
    const sessionID = 'existing-chat'
    localStorage.setItem('railgrid:agents:session:org:workspace:scout', sessionID)
    const defaultCredential = { value: 'openai' }
    const saveSessionModel = vi.fn(async (_name: string, _session: string, preferences: any) => ({
      ...preferences,
      selectedCredential: preferences.modelCredential || defaultCredential.value,
      modelCredential: preferences.modelCredential || '',
      effortOptions: [],
      defaultEffort: '',
      reasoningEffortByModel: preferences.reasoningEffortByModel || {},
    }))
    const element = await mountOverlay({
      defaultCredential: defaultCredential.value,
      credentials: [
        { name: 'openai', provider: 'openai', model: 'gpt-test' },
        { name: 'deleted-haiku', provider: 'anthropic', model: 'claude-opus-5-5' },
        { name: 'new-haiku', provider: 'anthropic', model: 'claude-haiku-5-5' },
      ],
      sessions: [{ id: sessionID, preview: 'Existing conversation', messageCount: 1, createdAt: '', lastActivity: '' }],
      messages: [{ id: 'earlier-answer', role: 'assistant', content: 'Earlier answer', createdAt: '' }],
      sessionModel: { selectedCredential: 'openai', modelCredential: '', effortOptions: [], defaultEffort: '', reasoningEffortByModel: {} },
      saveSessionModel,
    })
    const stream = vi.spyOn(element.api!, 'chatStream')
    const input = element.querySelector<HTMLTextAreaElement>('textarea')!
    input.value = 'Keep this unsent draft'
    input.dispatchEvent(new Event('input'))
    await settleVue()
    expect(element.textContent).toContain('Earlier answer')

    element.querySelector<HTMLButtonElement>('.k-ai-model-reasoning-picker__trigger')!.click()
    await settleVue()
    document.querySelector<HTMLButtonElement>('[aria-label^="Choose model:"]')!.click()
    await settleVue()
    const deletedModel = [...document.querySelectorAll<HTMLButtonElement>('[role="option"]')]
      .find(option => option.textContent?.includes('deleted-haiku'))
    expect(deletedModel).toBeDefined()
    deletedModel!.click()
    await settleVue(12)
    expect(saveSessionModel).toHaveBeenCalledOnce()
    expect(saveSessionModel.mock.calls[0]![2]).toMatchObject({ modelCredential: 'deleted-haiku' })

    defaultCredential.value = 'new-haiku'
    element.store!.credentials.data = [
      { name: 'openai', provider: 'openai', model: 'gpt-test' },
      { name: 'new-haiku', provider: 'anthropic', model: 'claude-haiku-5-5' },
    ]
    element.store!.agents.data[0]!.spec.backend = modelBackend(defaultCredential.value)
    element.store!.dispatchEvent(new Event('change'))
    await settleVue(12)

    expect(element.textContent).toContain('Choose a model to continue')
    expect(element.textContent).toContain('The agent’s default is new-haiku · claude-haiku-5-5')
    const useDefault = [...element.querySelectorAll<HTMLButtonElement>('button')]
      .find(button => button.textContent === 'Use agent default')
    expect(useDefault?.getAttribute('aria-label')).toContain('Use agent default: new-haiku · claude-haiku-5-5')
    expect(saveSessionModel).toHaveBeenCalledOnce()
    element.store!.setPendingPrompt('scout', 'Do not send this queued prompt')

    useDefault!.click()
    await settleVue(12)

    expect(saveSessionModel).toHaveBeenCalledTimes(2)
    expect(saveSessionModel.mock.calls[1]).toEqual([
      'scout', sessionID, expect.objectContaining({ modelCredential: '' }),
    ])
    expect(element.querySelector('.agents-model-recovery')).toBeNull()
    expect(element.querySelector<HTMLTextAreaElement>('textarea')?.value).toBe('Keep this unsent draft')
    expect(element.textContent).toContain('Earlier answer')
    expect(element.querySelector('.k-ai-model-reasoning-picker__trigger')?.getAttribute('aria-label')).toContain('new-haiku')
    expect(vi.mocked(element.api!.listMessagePage)).toHaveBeenCalledTimes(1)
    expect(element.store!.takePendingPrompt('scout')).toBe('Do not send this queued prompt')
    expect(stream).not.toHaveBeenCalled()
  })
  it('distinguishes loading from failure and retries the credential read', async () => {
    const element = await mountOverlay()
    const load = vi.spyOn(element.store!, 'load').mockResolvedValue(undefined)
    element.store!.credentials.data = []; element.store!.credentials.hasSnapshot = false; element.store!.credentials.loading = true
    element.store!.dispatchEvent(new Event('change')); await settleVue()
    expect(element.textContent).toContain('Loading models…')
    expect(element.querySelector('.agents-model-recovery select')).toBeNull()
    element.store!.credentials.loading = false; element.store!.credentials.error = 'offline'; element.store!.dispatchEvent(new Event('change')); await settleVue()
    expect(element.textContent).toContain('Models could not be loaded')
    const retry = [...element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent === 'Retry')!
    retry.click(); await settleVue()
    expect(load).toHaveBeenCalledWith('credentials')
    expect(element.textContent).not.toContain('Connect model')
  })
  it('offers administrator guidance when setup permission is denied', async () => {
    const element = await mountOverlay()
    vi.mocked(element.api!.canConfigureChatModel).mockResolvedValue(false)
    models(element, []); await settleVue(12)
    expect(element.textContent).toContain('Ask a workspace administrator')
    expect([...element.querySelectorAll('button')].some(button => button.textContent === 'Connect model')).toBe(false)
  })
  it('retries permission checks without treating a failed check as denial', async () => {
    const element = await mountOverlay()
    vi.mocked(element.api!.canConfigureChatModel).mockRejectedValueOnce(new Error('offline')).mockResolvedValue(true)
    models(element, []); await settleVue(12)
    expect(element.textContent).toContain('Setup access could not be checked')
    const retry = [...element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent === 'Retry access check')!
    retry.click(); await settleVue(12)
    expect([...element.querySelectorAll('button')].some(button => button.textContent === 'Connect model')).toBe(true)
  })
  it('navigates into Add model with a recovery return intent and refreshes models on return', async () => {
    const element = await mountOverlay()
    models(element, []); await settleVue(12)
    const navigation = vi.fn(); element.addEventListener('railgrid-overlay-navigate', navigation)
    const connect = [...element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent === 'Connect model')!
    connect.click(); await settleVue()
    expect(navigation.mock.calls[0][0].detail).toEqual({ path: '#/create/model/from-connection', recovery: true })
    const load = vi.spyOn(element.store!, 'load').mockResolvedValue(undefined)
    element.dispatchEvent(new Event('railgrid-overlay-resume')); await settleVue()
    expect(load).toHaveBeenCalledWith('credentials')
  })
  it('explains forbidden model reads without presenting configuration or retry actions', async () => {
    const element = await mountOverlay()
    element.store!.credentials.error = 'forbidden'; element.store!.credentials.errorStatus = 403
    element.store!.dispatchEvent(new Event('change')); await settleVue()
    expect(element.textContent).toContain('Model access is restricted')
    expect(element.textContent).toContain('Ask a workspace administrator')
    expect(element.querySelector('textarea')).toBeNull()
    expect(element.querySelector('.agents-model-recovery button')).toBeNull()
  })
  it('moves focus out of the removed composer and restores it after recovery', async () => {
    const element = await mountOverlay()
    element.querySelector('textarea')!.focus()
    models(element, []); await settleVue(12)
    expect(document.activeElement?.id).toBe('overlay-model-recovery-title')
    const connect = [...element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent === 'Connect model')!
    connect.focus()
    models(element, [{ name: 'openai', provider: 'openai', model: 'restored' }]); await settleVue(10)
    expect(document.activeElement).toBe(element.querySelector('textarea'))
  })
  it('keeps Stop available when a model disappears during an active reply', async () => {
    let finish!: () => void
    const pending = new Promise<void>(resolve => { finish = resolve })
    vi.spyOn(ApiClient.prototype, 'chatStream').mockImplementation(async function* () {
      yield { event: 'start', data: JSON.stringify({ runID: 'active-run', sessionID: 'active-session' }) }
      await pending
      yield { event: 'done', data: JSON.stringify({ runID: 'active-run', content: 'Done', status: 'Succeeded' }) }
    })
    const element = await mountOverlay()
    await send(element, 'Start work')
    models(element, []); await settleVue()
    expect(element.querySelector('.agents-stop')).not.toBeNull()
    expect(element.querySelector('.agents-model-recovery')).toBeNull()
    finish(); await settleVue(14)
    expect(element.querySelector('.agents-model-recovery')).not.toBeNull()
  })

  it('keeps compact recovery visible under the real bar stylesheet and opens the full recovery view', async () => {
    const style = document.createElement('style')
    style.textContent = readFileSync(resolve(process.cwd(), 'src/style.css'), 'utf8')
    document.head.appendChild(style)
    try {
      const element = await mountOverlay()
      element.overlayState = { agent: 'scout', composerOnly: true }
      models(element, []); await settleVue(12)
      const recovery = element.querySelector<HTMLElement>('.agents-model-recovery')!
      expect(getComputedStyle(recovery).display).not.toBe('none')
      expect(getComputedStyle(element.querySelector('.agents-log')!).display).toBe('none')
      expect(recovery.textContent).toContain('Model needed')
      expect(element.querySelector('textarea')).toBeNull()
      const engage = vi.fn(() => { element.overlayState = { agent: 'scout', composerOnly: false } })
      element.addEventListener('railgrid-overlay-engage', engage)
      recovery.querySelector<HTMLButtonElement>('button')!.click(); await settleVue(12)
      expect(engage).toHaveBeenCalledOnce()
      expect(element.textContent).toContain('Connect a model to continue')
      expect([...element.querySelectorAll('button')].some(button => button.textContent === 'Connect model')).toBe(true)
    } finally { style.remove() }
  })

})
