import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { Bot } from 'lucide-vue-next'
import Tabs from '../portalkit/Tabs.vue'
import { mountVue, settleVue } from './vue-helper'

// Portal Docker builds intentionally receive only this portal directory during
// the frontend stage. Exercise the synced PortalKit copies that the standalone
// bundle actually ships so the prebuild remains hermetic in that context.
const tabStyles = readFileSync(resolve(process.cwd(), 'src/portalkit/faros-ui.css'), 'utf8')
const tabsComponent = readFileSync(resolve(process.cwd(), 'src/portalkit/Tabs.vue'), 'utf8')

// Import the real entrypoint so this contract covers the light-DOM bundle's
// stylesheet handoff, not only the vendored component in isolation.
import '../main'

describe('PortalKit tabs contract', () => {
  it('renders accessible Vue tabs and reports selection to its owner', async () => {
    const selections: string[] = []
    const mounted = await mountVue(Tabs, {
      tabs: [{ id: 'agents', label: 'Agents', icon: Bot, count: 2 }, { id: 'activity', label: 'Activity' }],
      active: 'agents',
      ariaLabel: 'Provider sections',
      onSelect: (id: string) => selections.push(id),
    })
    const nav = mounted.element.querySelector('nav')!
    const tabs = [...nav.querySelectorAll<HTMLButtonElement>('[data-k-tab-id]')]
    expect(nav.getAttribute('aria-label')).toBe('Provider sections')
    expect(tabs.map(tab => tab.dataset.kTabId)).toEqual(['agents', 'activity'])
    expect(tabs[0].getAttribute('aria-current')).toBe('page')
    expect(tabs[0].querySelector('.k-tab__count')?.textContent).toBe('2')
    tabs[1].click()
    await settleVue()
    expect(selections).toEqual(['activity'])
  })

  it('keeps the canonical visual and narrow-layout invariants', () => {
    expect(tabStyles).toContain('flex-wrap: nowrap')
    expect(tabStyles).toContain('overflow-x: auto')
    expect(tabStyles).toContain('border-bottom: 1px solid var(--color-border-default')
    expect(tabStyles).toContain('border-radius: 4px')
    expect(tabStyles).toContain('background: var(--color-surface-hover')
    expect(tabStyles).toContain('background: var(--color-accent-subtle')
    expect(tabStyles).toContain('color: var(--color-accent')
    expect(tabStyles).toContain('outline: 2px solid var(--color-accent')
    expect(tabStyles).toContain('.k-tab__icon svg')
    expect(tabStyles).toContain('width: 1.05em')
    expect(tabStyles).toContain('color: var(--color-on-accent')
    expect(tabStyles).toContain('box-shadow: none')
    expect(tabsComponent).toContain(':data-k-tab-id="tab.id"')
  })

  it('loads the provider stylesheet handoff from the entrypoint', () => {
    expect(document.getElementById('faros-provider-agents-css')).toBeTruthy()
  })
})
