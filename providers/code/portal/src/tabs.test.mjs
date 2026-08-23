import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const app = readFileSync(resolve(process.cwd(), 'src/App.vue'), 'utf8')
const style = readFileSync(resolve(process.cwd(), 'src/style.css'), 'utf8')

describe('Code route tabs', () => {
  it('defines the route items through the canonical PortalKit component', () => {
    expect(app).toContain("import Tabs from './portalkit/Tabs.vue'")
    expect(app).toContain("import { GitBranch, Package, Plug } from 'lucide-vue-next'")
    expect(app).toContain('{ id: \'connections\', label: \'Connections\', icon: Plug }')
    expect(app).toContain('{ id: \'repositories\', label: \'Repositories\', icon: GitBranch }')
    expect(app).toContain('{ id: \'packages\', label: \'Packages\', icon: Package }')
    expect(app).toContain('<Tabs :tabs="tabs" :active="route.page" aria-label="Code provider sections" @select="navigate" />')
  })

  it('leaves tab presentation to PortalKit', () => {
    expect(style).not.toMatch(/faros-provider-code \.tabs(?:\s|\{|\.)/)
  })
})
