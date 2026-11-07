import { describe, expect, it } from 'vitest'
import type { Component } from 'vue'
import type { SliceKey } from '../store'
import AgentConfig from '../views/AgentConfig.vue'
import Automation from '../views/Automation.vue'
import Connections from '../views/Connections.vue'
import Toolsets from '../views/Toolsets.vue'
import Webhooks from '../views/Webhooks.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

interface EditCase {
  name: string
  component: Component
  slice: SliceKey
  loader: string
  props: Record<string, unknown>
  field: string
  row: (value: string) => unknown
}

const cases: EditCase[] = [
  { name: 'connection', component: Connections, slice: 'connections', loader: 'listConnections', props: { editRoute: true, editName: 'item' }, field: 'input', row: displayName => ({ metadata: { name: 'item' }, spec: { type: 'http', displayName } }) },
  { name: 'toolset', component: Toolsets, slice: 'toolsets', loader: 'listToolsets', props: { editRoute: true, editName: 'item' }, field: 'input', row: displayName => ({ metadata: { name: 'item' }, spec: { displayName, connections: [] } }) },
  { name: 'webhook', component: Webhooks, slice: 'webhooks', loader: 'listWebhooks', props: { editRoute: true, editName: 'item' }, field: 'input[name="displayName"]', row: displayName => ({ metadata: { name: 'item' }, spec: { source: 'webhook', displayName } }) },
  { name: 'agent config', component: AgentConfig, slice: 'agents', loader: 'listAgents', props: { name: 'item' }, field: 'input', row: displayName => agentFixture('item', { displayName }) },
  ...(['schedule', 'trigger'] as const).map(kind => ({
    name: kind, component: Automation, slice: (kind === 'schedule' ? 'schedules' : 'triggers') as SliceKey,
    loader: kind === 'schedule' ? 'listSchedules' : 'listTriggers', props: { kind, agent: 'scout', editName: 'item' }, field: 'textarea[name="task"]',
    row: (task: string) => ({ metadata: { name: 'item' }, spec: { agentRef: 'scout', task, type: 'cron', schedule: '0 * * * *' } }),
  })),
]

for (const test of cases) describe(`${test.name} initial edit refresh`, () => {
  it('waits for fresh data before hydration, then preserves edits across later refreshes', async () => {
    let finish!: (rows: unknown[]) => void
    const api = stubApi({
      [test.loader]: () => new Promise<unknown[]>(resolve => { finish = resolve }),
      listWebhookDeliveries: async () => ({ items: [], retention: 200 }),
    })
    const store = makeStore(api)
    Object.assign(store[test.slice], { data: [test.row('Cached value')], loaded: true, hasSnapshot: true })
    const load = store.load(test.slice)
    const view = await mountVue(test.component, { store, api, ...test.props })
    expect(view.element.querySelector(test.field)).toBeNull()

    finish([test.row('Changed by agent')])
    await load
    await settleVue()
    const field = view.element.querySelector<HTMLInputElement | HTMLTextAreaElement>(test.field)!
    expect(field.value).toBe('Changed by agent')
    field.value = 'My unsaved edit'
    field.dispatchEvent(new Event('input', { bubbles: true }))
    await settleVue()

    const refresh = store.load(test.slice)
    await settleVue()
    expect(view.element.querySelector(test.field)).toBe(field)
    finish([test.row('Another external change')])
    await refresh
    await settleVue()
    expect(field.value).toBe('My unsaved edit')
    view.unmount()
  })

  it('blocks stale initial fields after a failure and hydrates after a successful retry', async () => {
    let fail = true
    const api = stubApi({
      [test.loader]: async () => {
        if (fail) throw new Error('Refresh unavailable')
        return [test.row('Fresh after retry')]
      },
      listWebhookDeliveries: async () => ({ items: [], retention: 200 }),
    })
    const store = makeStore(api)
    Object.assign(store[test.slice], { data: [test.row('Cached value')], loaded: true, hasSnapshot: true })
    await store.load(test.slice)
    const view = await mountVue(test.component, { store, api, ...test.props })
    expect(text(view.element)).toContain('Refresh unavailable')
    expect(view.element.querySelector(test.field)).toBeNull()

    fail = false
    const retry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button).includes('Retry'))!
    retry.click()
    await settleVue()
    expect(view.element.querySelector<HTMLInputElement | HTMLTextAreaElement>(test.field)?.value).toBe('Fresh after retry')
    view.unmount()
  })
})
