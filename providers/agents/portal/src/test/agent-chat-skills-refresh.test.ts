import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, ref, shallowRef } from 'vue'
import type { AgentProviderResourceCoordinate } from '../api'
import type { Route } from '../router'
import AgentChat from '../views/AgentChat.vue'
import type { AgentSkill, AgentSkillCatalog, ModelInfo } from '../types'
import { provideAgentsRuntime } from '../vue/runtime'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text, unmountVueApps } from './vue-helper'

const runtimeHosts: HTMLElement[] = []

afterEach(() => {
  unmountVueApps()
  while (runtimeHosts.length) runtimeHosts.pop()?.remove()
})

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(res => { resolve = res })
  return { promise, resolve }
}

function skill(id: string, enabled: boolean): AgentSkill {
  return { id, name: id.split(':').at(-1) || id, description: 'A test skill', scope: 'workspace', enabled, editable: true }
}

function catalog(...skills: AgentSkill[]): AgentSkillCatalog {
  return { skills, catalogDigest: 'test-catalog', warnings: [] }
}

async function openSkillPalette(view: Awaited<ReturnType<typeof mountVue>>): Promise<void> {
  view.element.querySelector<HTMLButtonElement>('[aria-label="Add files or open slash commands"]')!.click()
  await settleVue()
  const addSkills = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
    .find(button => text(button).includes('Skill, resource, or command'))!
  addSkills.click()
  await settleVue()
  const skillCommand = document.querySelector<HTMLButtonElement>('.k-ai-command-palette-panel [role="option"]')!
  expect(text(skillCommand)).toContain('/skill')
  skillCommand.click()
  await settleVue()
}

async function openResourcePalette(view: Awaited<ReturnType<typeof mountVue>>): Promise<void> {
  view.element.querySelector<HTMLButtonElement>('[aria-label="Add files or open slash commands"]')!.click()
  await settleVue()
  const addSkills = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
    .find(button => text(button).includes('Skill, resource, or command'))!
  addSkills.click()
  await settleVue()
  const resourceCommand = [...document.querySelectorAll<HTMLButtonElement>('.k-ai-command-palette-panel [role="option"]')]
    .find(button => text(button).includes('/resource'))!
  expect(resourceCommand).toBeTruthy()
  resourceCommand.click()
  await settleVue()
}

describe('AgentChat skill catalog refresh', () => {
  it('refreshes on enable and disable, prunes the disabled chip, and leaves other catalogs alone', async () => {
    const review = skill('workspace:review', false)
    let skillCatalogReads = 0
    let store!: ReturnType<typeof makeStore>
    const getAgentSkillCatalog = vi.fn(async (name: string) => {
      skillCatalogReads += 1
      if (skillCatalogReads === 3) throw new Error('skill catalog unavailable')
      const enabled = (store.agent(name)?.spec.skills || []).some(item => item.name === review.id)
      return catalog({ ...review, enabled })
    })
    const listProviderResourceCatalog = vi.fn(async () => [])
    const modelCatalog = vi.fn(async () => [])
    const api = stubApi({ getAgentSkillCatalog, listProviderResourceCatalog, catalog: modelCatalog })
    store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'gpt-test' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true

    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    await vi.waitFor(() => expect(getAgentSkillCatalog).toHaveBeenCalledTimes(1))

    // An unrelated store tick does not refresh the skill, provider, or model catalogs.
    store.agent('scout')!.spec.description = 'Updated without changing skills'
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(getAgentSkillCatalog).toHaveBeenCalledTimes(1)
    expect(listProviderResourceCatalog).toHaveBeenCalledTimes(1)
    expect(modelCatalog).toHaveBeenCalledTimes(1)

    store.agent('scout')!.spec.skills = [{ name: review.id }]
    store.dispatchEvent(new Event('change'))
    await vi.waitFor(() => expect(getAgentSkillCatalog).toHaveBeenCalledTimes(2))
    await settleVue()
    await openSkillPalette(view)
    const option = [...document.querySelectorAll<HTMLButtonElement>('.k-ai-command-palette-panel [role="option"]')]
      .find(button => text(button).includes(review.name))!
    expect(option).toBeTruthy()
    option.click()
    await settleVue()
    expect(view.element.querySelector('[data-assistant-chip="skill"]')).not.toBeNull()

    store.agent('scout')!.spec.skills = []
    store.dispatchEvent(new Event('change'))
    await vi.waitFor(() => expect(getAgentSkillCatalog).toHaveBeenCalledTimes(3))
    await settleVue()
    expect(view.element.querySelector('[data-assistant-chip="skill"]')).toBeNull()
    await openSkillPalette(view)
    const remainingOptions = [...document.querySelectorAll<HTMLButtonElement>('.k-ai-command-palette-panel [role="option"]')]
      .map(button => text(button))
    expect(remainingOptions.some(option => option.includes(review.name))).toBe(false)
    expect(listProviderResourceCatalog).toHaveBeenCalledTimes(1)
    expect(modelCatalog).toHaveBeenCalledTimes(1)
    view.unmount()
  })

  it('ignores a late catalog response from the previous skill configuration', async () => {
    const oldResponse = deferred<AgentSkillCatalog>()
    const currentSkill = skill('workspace:current', true)
    const getAgentSkillCatalog = vi.fn()
      .mockImplementationOnce(() => oldResponse.promise)
      .mockResolvedValueOnce(catalog(currentSkill))
    const api = stubApi({ getAgentSkillCatalog })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'gpt-test' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true

    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    await vi.waitFor(() => expect(getAgentSkillCatalog).toHaveBeenCalledTimes(1))

    store.agent('scout')!.spec.skills = [{ name: currentSkill.id }]
    store.dispatchEvent(new Event('change'))
    await vi.waitFor(() => expect(getAgentSkillCatalog).toHaveBeenCalledTimes(2))
    await settleVue()

    oldResponse.resolve(catalog(skill('workspace:stale', true)))
    await settleVue()
    await openSkillPalette(view)
    const options = [...document.querySelectorAll<HTMLButtonElement>('.k-ai-command-palette-panel [role="option"]')]
      .map(button => text(button))
    expect(options.some(option => option.includes('current'))).toBe(true)
    expect(options.some(option => option.includes('stale'))).toBe(false)
    view.unmount()
  })

  it('retries a pending refresh against the current workbench route', async () => {
    const staleResponse = deferred<AgentSkillCatalog>()
    const currentSkill = skill('workspace:current', true)
    const getAgentSkillCatalog = vi.fn()
      .mockResolvedValueOnce(catalog())
      .mockImplementationOnce(() => staleResponse.promise)
      .mockResolvedValueOnce(catalog(currentSkill))
    const listProviderResourceCatalog = vi.fn(async () => [])
    const modelCatalog = vi.fn(async () => [])
    const api = stubApi({ getAgentSkillCatalog, listProviderResourceCatalog, catalog: modelCatalog })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'gpt-test' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    const route = ref<Route>({ kind: 'agent', name: 'scout', tab: 'chat' })
    const host = document.createElement('div')
    document.body.appendChild(host)
    runtimeHosts.push(host)
    const runtime = {
      store: shallowRef(store),
      api: shallowRef(api),
      route,
      authorityEpoch: ref(0),
      host: ref(host),
    }
    const ChatWithRuntime = defineComponent({
      setup() {
        provideAgentsRuntime(runtime)
        return () => h(AgentChat, { store, api, name: 'scout' })
      },
    })

    const view = await mountVue(ChatWithRuntime, {})
    await vi.waitFor(() => expect(getAgentSkillCatalog).toHaveBeenCalledTimes(1))
    store.agent('scout')!.spec.skills = [{ name: currentSkill.id }]
    store.dispatchEvent(new Event('change'))
    await vi.waitFor(() => expect(getAgentSkillCatalog).toHaveBeenCalledTimes(2))

    // The chat remains mounted while the user opens the Skills workbench. A
    // refresh started on the chat route is fenced, so the route change starts
    // one skill-only refresh under the current route authority.
    route.value = { kind: 'agent', name: 'scout', tab: 'skills' }
    await vi.waitFor(() => expect(getAgentSkillCatalog).toHaveBeenCalledTimes(3))
    await settleVue()
    staleResponse.resolve(catalog(skill('workspace:stale', true)))
    await settleVue()

    await openSkillPalette(view)
    const options = [...document.querySelectorAll<HTMLButtonElement>('.k-ai-command-palette-panel [role="option"]')]
      .map(button => text(button))
    expect(options.some(option => option.includes('current'))).toBe(true)
    expect(options.some(option => option.includes('stale'))).toBe(false)
    expect(listProviderResourceCatalog).toHaveBeenCalledTimes(1)
    expect(modelCatalog).toHaveBeenCalledTimes(1)
    view.unmount()
  })

  it('retries pending model and provider catalogs after a route change and keeps the newer results', async () => {
    const oldModels = deferred<ModelInfo[]>()
    const oldProviders = deferred<AgentProviderResourceCoordinate[]>()
    const currentModels: ModelInfo[] = [{ id: 'current-model', family: 'test', inputPer1M: 1, outputPer1M: 1 }]
    const currentProviders: AgentProviderResourceCoordinate[] = [{
      name: 'current-provider',
      displayName: 'Current Provider',
      ready: true,
      export: { resources: [{ name: 'items', apiVersion: 'test.railgrid.ai/v1alpha1', kind: 'Item', actions: [{}] }] },
    }]
    const staleProviders: AgentProviderResourceCoordinate[] = [{
      name: 'stale-provider',
      displayName: 'Stale Provider',
      ready: true,
      export: { resources: [{ name: 'items', apiVersion: 'test.railgrid.ai/v1alpha1', kind: 'Item', actions: [{}] }] },
    }]
    const listProviderResourceCatalog = vi.fn()
      .mockImplementationOnce(() => oldProviders.promise)
      .mockResolvedValueOnce(currentProviders)
    const modelCatalog = vi.fn()
      .mockImplementationOnce(() => oldModels.promise)
      .mockResolvedValueOnce(currentModels)
    const api = stubApi({ listProviderResourceCatalog, catalog: modelCatalog })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'gpt-test' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    const route = ref<Route>({ kind: 'agent', name: 'scout', tab: 'chat' })
    const host = document.createElement('div')
    document.body.appendChild(host)
    runtimeHosts.push(host)
    const runtime = {
      store: shallowRef(store),
      api: shallowRef(api),
      route,
      authorityEpoch: ref(0),
      host: ref(host),
    }
    const ChatWithRuntime = defineComponent({
      setup() {
        provideAgentsRuntime(runtime)
        return () => h(AgentChat, { store, api, name: 'scout' })
      },
    })

    const view = await mountVue(ChatWithRuntime, {})
    await vi.waitFor(() => {
      expect(listProviderResourceCatalog).toHaveBeenCalledTimes(1)
      expect(modelCatalog).toHaveBeenCalledTimes(1)
    })
    route.value = { kind: 'agent', name: 'scout', tab: 'skills' }
    await vi.waitFor(() => {
      expect(listProviderResourceCatalog).toHaveBeenCalledTimes(2)
      expect(modelCatalog).toHaveBeenCalledTimes(2)
    })
    await settleVue()

    // Return to the original route before the first reads settle. The current
    // route hash now matches their capture again, so request serials must keep
    // these older responses from replacing the route-change retry results.
    route.value = { kind: 'agent', name: 'scout', tab: 'chat' }
    oldModels.resolve([{ id: 'stale-model', family: 'old', inputPer1M: 1, outputPer1M: 1 }])
    oldProviders.resolve(staleProviders)
    await settleVue()
    route.value = { kind: 'agent', name: 'scout', tab: 'config' }
    await settleVue()
    expect(listProviderResourceCatalog).toHaveBeenCalledTimes(2)
    expect(modelCatalog).toHaveBeenCalledTimes(2)

    await openResourcePalette(view)
    const providerOptions = [...document.querySelectorAll<HTMLButtonElement>('.k-ai-command-palette-panel [role="option"]')]
      .map(button => text(button))
    expect(providerOptions.some(option => option.includes('Current Provider'))).toBe(true)
    expect(providerOptions.some(option => option.includes('Stale Provider'))).toBe(false)
    view.unmount()
  })
})
