import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const readSource = (file) => readFileSync(resolve(process.cwd(), 'src', file), 'utf8')
const app = readSource('App.vue')
const edgeCollection = readSource('EdgeCollection.vue')
const services = readSource('Services.vue')
const workloads = readSource('Workloads.vue')
const styles = readSource('style.css')

describe('Edges portal conformance', () => {
  it('uses PortalKit tabs for route navigation and keeps the route contract', () => {
    expect(app).toMatch(/import Tabs from '\.\/portalkit\/Tabs\.vue'/)
    expect(app).toMatch(/const edgeRouteTabs = \[[\s\S]*id: 'edges'[\s\S]*Server[\s\S]*id: 'workloads'[\s\S]*Boxes[\s\S]*id: 'services'[\s\S]*Plug/)

    const routeTabs = app.match(/<Tabs[\s\S]*?@select="selectView"\s*\/>/)?.[0]
    expect(routeTabs).toBeTruthy()
    expect(routeTabs).toMatch(/v-if="!route\.edge && !route\.service && !route\.connect && !route\.create && !route\.deploy"/)
    expect(routeTabs).toMatch(/:tabs="edgeRouteTabs"/)
    expect(routeTabs).toMatch(/:active="view"/)
    expect(routeTabs).toMatch(/aria-label="Edges sections"/)
    expect(routeTabs).not.toMatch(/wiz-steps|wiz-step|style=/)
  })

  it('keeps route-owned creation and detail surfaces out of collection overlays', () => {
    expect(styles).toMatch(/faros-provider-edges \.edges-app \{[^}]*display: flex;[^}]*flex-direction: column;[^}]*gap: 16px;/)
    expect(app).toMatch(/<WorkloadCreate[\s\S]*route\.deploy\?\.resource === 'workload'/)
    expect(app).toMatch(/<ServiceCreate[\s\S]*route\.create\?\.resource === 'service'/)
    expect(app).toMatch(/<Wizard[\s\S]*route\.connect\?\.resource === 'edge'/)
    expect(app).toMatch(/<Detail[\s\S]*v-if="route\.edge"/)
    expect(app).not.toMatch(/const selected = ref/)
    expect(services).not.toMatch(/const showCreate = ref/)
    expect(workloads).not.toMatch(/const showCreate = ref/)
    expect(styles).toMatch(/\.wiz-steps\s*\{[\s\S]*\.wiz-step\s*\{[\s\S]*\.wiz-step\.active\s*\{[\s\S]*\.wiz-step\.done\s*\{/)
  })

  it('keeps header actions intrinsic while descriptive copy wraps', () => {
    expect(styles).toMatch(/\.edges-header\s*>\s*:first-child\s*\{[^}]*flex:\s*1 1 auto;[^}]*min-width:\s*0;/)
    expect(styles).toMatch(/\.header-actions\s*\{[^}]*flex:\s*0 0 auto;[^}]*flex-wrap:\s*wrap;/)
    expect(styles).toMatch(/\.header-actions button\s*\{[^}]*flex:\s*0 0 auto;[^}]*white-space:\s*nowrap;/)
    expect(styles).toMatch(/@media \(max-width:\s*720px\)[\s\S]*?\.edges-header\s*\{[^}]*align-items:\s*stretch;[^}]*flex-direction:\s*column;/)
  })

  it('keeps interactive table rows labeled and nested actions explicit', () => {
    for (const [source, labelFunction] of [
      [edgeCollection, 'edgeRowAriaLabel'],
      [services, 'serviceRowAriaLabel'],
      [workloads, 'workloadRowAriaLabel'],
    ]) {
      expect(source).toMatch(new RegExp(`function ${labelFunction}\\(row: Record<string, unknown>\\)`))
      expect(source).toMatch(new RegExp(`:row-aria-label="${labelFunction}"`))
      expect(source).toMatch(/@row-click=/)
    }

    expect(edgeCollection).toMatch(/ResourceTableDeleteButton/)
    expect(services).toMatch(/ResourceTableEditButton/)
    expect(services).toMatch(/ResourceTableDeleteButton/)
    expect(workloads).toMatch(/ResourceTableDeleteButton/)
    expect(workloads).toMatch(/<button[\s\S]*class="k-table-action"[\s\S]*:aria-expanded="expanded === row\.name"[\s\S]*@click="toggle\(String\(row\.name\)\)"/)
  })
})
