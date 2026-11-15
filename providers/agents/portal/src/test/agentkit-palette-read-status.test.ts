import { expect, it } from 'vitest'
import AICommandPalette from '../agentkit/AICommandPalette.vue'
import { mountVue, settleVue } from './vue-helper'

it('announces resource loading and the settled empty result outside the listbox', async () => {
  let resolve!: (result: { groups: never[], warnings: never[] }) => void
  const pending = new Promise<{ groups: never[], warnings: never[] }>((done) => { resolve = done })
  await mountVue(AICommandPalette, {
    open: true, commandQuery: '', skills: [], selectedSkillIDs: [], selectedResources: [],
    resourceProviders: [{ name: 'demo', displayName: 'Demo', resourceTypes: [] }],
    discoverResources: () => pending,
  })
  const options = () => [...document.querySelectorAll<HTMLButtonElement>('[role=option]')]
  options().find(option => option.textContent?.includes('/resource'))!.click()
  await settleVue(4)
  options()[0].click()
  await settleVue(4)
  const status = document.querySelector<HTMLElement>('.k-ai-command-palette-panel [role=status]')!
  expect(status.textContent).toBe('Loading resources…')
  expect(status.closest('[role=listbox]')).toBeNull()
  resolve({ groups: [], warnings: [] })
  await settleVue(4)
  expect(status.textContent).toBe('No accessible resources match.')
})
