import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { tabClass, tabCountClass, tabsClass } from '../portalkit/tabs'

const tabStyles = readFileSync(resolve(process.cwd(), '../../../portal/src/assets/faros-ui.css'), 'utf8')
const tabsComponent = readFileSync(resolve(process.cwd(), '../../../provider-sdk/portalkit-vue/Tabs.vue'), 'utf8')

// Import the real entrypoint so this contract covers the light-DOM bundle's
// stylesheet handoff, not only the vendored helper in isolation.
import '../main'

describe('PortalKit tabs contract', () => {
  it('builds stable framework-neutral classes for Lit/string templates', () => {
    expect(tabsClass()).toBe('k-tabs')
    expect(tabsClass('agents-nav')).toBe('k-tabs agents-nav')
    expect(tabClass()).toBe('k-tab')
    expect(tabClass({ active: true, disabled: true, className: 'agents-navtab' })).toBe(
      'k-tab k-tab--active k-tab--disabled agents-navtab',
    )
    expect(tabCountClass()).toBe('k-tab__count')
    expect(tabCountClass({ attention: true, className: 'agents-navcount' })).toBe(
      'k-tab__count k-tab__count--attention agents-navcount',
    )
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
    expect(tabStyles).toContain('color: inherit')
    expect(tabStyles).toContain('color: var(--color-on-accent')
    expect(tabStyles).toContain('box-shadow: none')
    expect(tabsComponent).toContain(':data-k-tab-id="tab.id"')
  })

  it('loads the canonical recipe into the Agents light-DOM bundle', () => {
    const style = document.getElementById('faros-provider-agents-css')
    // Vite's production build inlines ?raw CSS; Vitest's jsdom transform
    // intentionally leaves that import empty. The entrypoint still owns the
    // handoff, while the previous test verifies the vendored recipe itself.
    expect(style).toBeTruthy()
  })
})
