// Agent config: the pointer-patch semantics each section relies on, and the
// fields that only recently became writable (description, limits).

import { describe, expect, it, vi } from 'vitest'
import type { AgentConfig } from '../views/agent-config'
import type { AgentCreateWizard } from '../views/agent-create'
import type { AgentPatch } from '../types'
import '../views/agent-config'
import '../views/agent-create'
import { familiesForConns } from '../conn-defs'
import { clearToasts, subscribeToasts } from '../ui/toast'
import { agentFixture, makeStore, mount, settle, stubApi, text } from './helpers'

async function mountConfig(spec: Record<string, unknown> = {}) {
  const patchAgent = vi.fn().mockImplementation((_n: string, body: AgentPatch) => Promise.resolve({ metadata: { name: 'scout' }, spec: body }))
  const api = stubApi({ patchAgent })
  const store = makeStore(api)
  store.agents.data = [agentFixture('scout', spec)]
  store.agents.loaded = true
  const el = await mount<AgentConfig>('agents-agent-config', { store, api, name: 'scout' })
  return { el, patchAgent }
}

function sectionButton(el: AgentConfig, label: string): HTMLButtonElement {
  return [...el.querySelectorAll('button')].find((b) => b.textContent?.includes(label)) as HTMLButtonElement
}

describe('agent config', () => {
  it('uses the canonical square action for removing a model fallback', async () => {
    const { el } = await mountConfig({ models: { chat: 'main' }, modelFallbacks: ['backup'] })
    const remove = el.querySelector<HTMLButtonElement>('.agents-chip-x')!

    expect(remove).not.toBeNull()
    expect(remove.classList.contains('k-icon-action')).toBe(true)
    expect(remove.type).toBe('button')
    expect(remove.getAttribute('aria-label')).toBe('Remove fallback backup')
  })

  it('saves an editable description with the persona section', async () => {
    const { el, patchAgent } = await mountConfig({ description: 'watches the deploy queue' })
    const inputs = [...el.querySelectorAll<HTMLInputElement>('input')]
    const desc = inputs.find((i) => i.value === 'watches the deploy queue')!
    expect(desc).toBeDefined() // editable input, not a read-only block

    desc.value = 'now watches everything'
    desc.dispatchEvent(new Event('input'))
    await settle(el)
    sectionButton(el, 'Save persona').click()
    await settle(el, 4)

    expect(patchAgent).toHaveBeenCalledWith('scout', expect.objectContaining({ description: 'now watches everything' }))
    // The persona patch must not carry keys another section owns.
    const patch = patchAgent.mock.calls[0][1] as AgentPatch
    expect(patch.autonomy).toBeUndefined()
    expect(patch.channels).toBeUndefined()
  })

  it('hydrates and saves maxToolTurns / timeoutSeconds alongside the budget', async () => {
    const { el, patchAgent } = await mountConfig({ limits: { maxToolTurns: 12, timeoutSeconds: 600 } })
    const inputs = [...el.querySelectorAll<HTMLInputElement>('input')]
    const turns = inputs.find((i) => i.value === '12')!
    const timeout = inputs.find((i) => i.value === '600')!
    expect(turns).toBeDefined()
    expect(timeout).toBeDefined()

    timeout.value = '900'
    timeout.dispatchEvent(new Event('input'))
    await settle(el)
    sectionButton(el, 'Save policy').click()
    await settle(el, 4)

    expect(patchAgent).toHaveBeenCalledWith(
      'scout',
      expect.objectContaining({ maxToolTurns: 12, timeoutSeconds: 900, autonomy: 'ask' }),
    )
  })

  it('sends 0 for a blank limit, which the backend reads as the provider default', async () => {
    const { el, patchAgent } = await mountConfig()
    sectionButton(el, 'Save policy').click()
    await settle(el, 4)
    expect(patchAgent.mock.calls[0][1]).toMatchObject({ maxToolTurns: 0, timeoutSeconds: 0, budgetTokens: 0 })
  })

  it('rolls the optimistic edit back when the save fails', async () => {
    const patchAgent = vi.fn().mockRejectedValue(new Error('409 conflict'))
    // The post-failure resync is made to fail too, so the restored value can
    // only have come from the rollback and not from a refetch.
    const api = stubApi({ patchAgent, listAgents: () => Promise.reject(new Error('unavailable')) })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { description: 'original' })]
    store.agents.loaded = true
    const el = await mount<AgentConfig>('agents-agent-config', { store, api, name: 'scout' })

    const desc = [...el.querySelectorAll<HTMLInputElement>('input')].find((i) => i.value === 'original')!
    desc.value = 'changed'
    desc.dispatchEvent(new Event('input'))
    await settle(el)
    sectionButton(el, 'Save persona').click()
    await settle(el, 4)

    expect(store.agent('scout')?.spec.description).toBe('original')
  })
})

describe('connection test feedback', () => {
  it('surfaces the reason from a failed channel test (HTTP error convention)', async () => {
    const testConnection = vi.fn().mockRejectedValue(new Error('telegram: 403 bot was blocked by the user'))
    const api = stubApi({ testConnection })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { channels: [{ name: 'primary', connectionRef: 'tg', primary: true }] })]
    store.connections.data = [{ metadata: { name: 'tg' }, spec: { type: 'telegram', channel: '123' } }]
    store.agents.loaded = true
    const el = await mount<AgentConfig>('agents-agent-config', { store, api, name: 'scout' })

    // The toast host is rendered by the shell, so read the bus directly.
    clearToasts()
    const raised: string[] = []
    const off = subscribeToasts((ts) => raised.push(...ts.map((t) => `${t.kind}:${t.message}`)))

    const testBtn = [...el.querySelectorAll('.agents-inbound-actions button')].find((b) => b.textContent?.includes('Test')) as HTMLButtonElement
    testBtn.click()
    await settle(el, 4)
    off()

    expect(testConnection).toHaveBeenCalledWith('tg')
    const failure = raised.find((m) => m.startsWith('error:'))
    expect(failure).toContain('Test of “tg” failed')
    expect(failure).toContain('blocked by the user')
  })
})

// Research fan-out is a capability of the agent, not a wired connection. That
// makes it the one family a user picks directly — and the one that the
// derive-families-from-connections rule would silently drop.
describe('research fan-out grant', () => {
  const spawnRow = (el: AgentConfig): HTMLInputElement[] => {
    const fs = [...el.querySelectorAll('fieldset')].find((f) => f.textContent?.includes('Built-in capabilities'))!
    // Rows are [web linked, web background, spawn linked, spawn background].
    return [...fs.querySelectorAll<HTMLInputElement>('input[type=checkbox]')].slice(2)
  }

  it('grants and revokes spawn for interactive runs', async () => {
    const { el, patchAgent } = await mountConfig({ tools: { interactive: { families: ['core', 'web'] } } })
    const [linked] = spawnRow(el)
    expect(linked.checked).toBe(false)

    linked.checked = true
    linked.dispatchEvent(new Event('change'))
    await settle(el, 4)
    expect(patchAgent.mock.calls[0][1].interactiveFamilies).toEqual(expect.arrayContaining(['core', 'web', 'spawn']))
  })

  it('turning it off also clears the background grant', async () => {
    const { el, patchAgent } = await mountConfig({
      tools: { interactive: { families: ['core', 'spawn'] }, background: { families: ['core', 'spawn'] } },
    })
    const [linked] = spawnRow(el)
    expect(linked.checked).toBe(true)

    linked.checked = false
    linked.dispatchEvent(new Event('change'))
    await settle(el, 4)
    const patch = patchAgent.mock.calls[0][1]
    expect(patch.interactiveFamilies).not.toContain('spawn')
    expect(patch.backgroundFamilies).not.toContain('spawn')
  })

  // The trap: familiesForConns rebuilds the list from scratch on every tool
  // grant, so without carrying spawn over, wiring a tool would switch fan-out
  // off behind the user's back.
  it('survives a rebuild of families from connections', () => {
    const connType = (n: string) => (n === 'gh' ? 'github' : undefined)
    const rebuilt = familiesForConns(['gh'], connType, ['core', 'spawn'])
    expect(rebuilt).toContain('spawn')
    expect(rebuilt).toContain('github')
    expect(rebuilt).toContain('core')
  })

  it('does not invent spawn when it was never granted', () => {
    expect(familiesForConns(['gh'], () => 'github', ['core'])).not.toContain('spawn')
    expect(familiesForConns([], () => undefined)).toEqual(['core'])
  })
})

// The config that looks configured but cannot work: spawn granted, web not.
// Workers inherit a SUBSET of the parent's tools, so they would get none — a
// fan-out that answers from the model alone, at fan-out cost.
describe('web + fan-out capability warnings', () => {
  const capsFieldset = (el: AgentConfig) =>
    [...el.querySelectorAll('fieldset')].find((f) => f.textContent?.includes('Built-in capabilities'))!

  it('warns when spawn is on but web is not', async () => {
    const { el } = await mountConfig({ tools: { interactive: { families: ['core', 'spawn'] } } })
    const warn = capsFieldset(el).querySelector('.agents-warn-inline')
    expect(warn).toBeTruthy()
    expect(warn!.textContent).toContain('no web access')
  })

  it('says nothing once web is granted too', async () => {
    const { el } = await mountConfig({ tools: { interactive: { families: ['core', 'web', 'spawn'] } } })
    expect(capsFieldset(el).querySelector('.agents-warn-inline')).toBeNull()
  })

  it('grants web directly — web_fetch needs no connection', async () => {
    const { el, patchAgent } = await mountConfig({ tools: { interactive: { families: ['core'] } } })
    const [webLinked] = [...capsFieldset(el).querySelectorAll<HTMLInputElement>('input[type=checkbox]')]
    expect(webLinked.checked).toBe(false)
    webLinked.checked = true
    webLinked.dispatchEvent(new Event('change'))
    await settle(el, 4)
    expect(patchAgent.mock.calls[0][1].interactiveFamilies).toContain('web')
  })

  it('keeps web across a rebuild of families from connections', () => {
    // Without this, wiring any tool would silently drop a preset-granted web.
    const rebuilt = familiesForConns(['gh'], () => 'github', ['core', 'web', 'spawn'])
    expect(rebuilt).toContain('web')
    expect(rebuilt).toContain('spawn')
  })
})

// Creating an agent asks what it can DO, not which template it is. There is no
// "research agent" kind: fan-out is a capability any agent can have, and it
// carries its own behaviour, so the create form and the Config pane use the same
// two toggles and the same words.
describe('agent creation capabilities', () => {
  async function mountCreate() {
    const createAgent = vi.fn().mockResolvedValue({ metadata: { name: 'scout' }, spec: {} })
    const api = stubApi({ createAgent })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'gpt-5' }] as never
    store.credentials.loaded = true
    store.credentials.hasSnapshot = true
    store.agents.loaded = true
    store.agents.hasSnapshot = true
    const el = await mount<AgentCreateWizard>('agents-agent-create', { store, api })
    await settle(el, 3)
    return { el, createAgent }
  }

  const caps = (el: AgentCreateWizard) => [...el.querySelectorAll<HTMLInputElement>('.agents-cap input[type=checkbox]')]

  it('offers capabilities, not agent templates', async () => {
    const { el } = await mountCreate()
    expect(text(el)).not.toContain('Research agent')
    expect(text(el)).not.toContain('Blank agent')
    expect(text(el)).toContain('Read the web')
    expect(text(el)).toContain('Research fan-out')
    expect(caps(el)).toHaveLength(2)
  })

  // The form is terse, so the surviving signal that behaviour is not the user's
  // job is the prompt field's own label. If that ever goes back to asking for
  // mechanics, the capability has stopped being self-sufficient.
  it('asks the prompt for persona, not mechanics', async () => {
    const { el } = await mountCreate()
    expect(text(el)).toContain('not mechanics')
  })

  it('puts capabilities last, after the fields you must fill in', async () => {
    const { el } = await mountCreate()
    const body = text(el)
    // Name and model are required; capabilities are optional extras and should
    // not lead the form.
    expect(body.indexOf('Can do')).toBeGreaterThan(body.indexOf('Model credential'))
    expect(body.indexOf('Can do')).toBeGreaterThan(body.indexOf('Primary channel'))
  })

  it('sends only the families that were ticked', async () => {
    const { el, createAgent } = await mountCreate()

    const nameInput = el.querySelector<HTMLInputElement>('input[name=name]')!
    nameInput.value = 'scout'
    nameInput.dispatchEvent(new Event('input'))
    const sel = el.querySelector<HTMLSelectElement>('select')!
    sel.value = 'main'
    sel.dispatchEvent(new Event('change'))

    const [web, fanOut] = caps(el)
    web.checked = true
    web.dispatchEvent(new Event('change'))
    fanOut.checked = true
    fanOut.dispatchEvent(new Event('change'))
    await settle(el, 3)

    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit'))
    await settle(el, 3)

    const sent = createAgent.mock.calls[0][0] as Record<string, unknown>
    expect(sent.interactiveFamilies).toEqual(['core', 'web', 'spawn'])
    // No prompt is invented on the user's behalf.
    expect(sent!.systemPrompt).toBeUndefined()
  })

  it('omits families entirely when nothing is ticked', async () => {
    const { el, createAgent } = await mountCreate()
    const nameInput = el.querySelector<HTMLInputElement>('input[name=name]')!
    nameInput.value = 'plain'
    nameInput.dispatchEvent(new Event('input'))
    const sel = el.querySelector<HTMLSelectElement>('select')!
    sel.value = 'main'
    sel.dispatchEvent(new Event('change'))
    await settle(el, 3)
    el.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit'))
    await settle(el, 3)
    const sent = createAgent.mock.calls[0][0] as Record<string, unknown>
    expect(sent!.interactiveFamilies).toBeUndefined()
  })
})
