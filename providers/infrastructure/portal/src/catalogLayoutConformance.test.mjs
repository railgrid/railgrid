import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const source = readFileSync(new URL('./views/CatalogPage.vue', import.meta.url), 'utf8')
const styles = readFileSync(new URL('./style.css', import.meta.url), 'utf8')

describe('Infrastructure template catalog layouts', () => {
  it('persists a grid-default layout through the shared preference contract', () => {
    expect(source).toContain("import LayoutSelector from '../portalkit/LayoutSelector.vue'")
    expect(source).toContain("import { readLayoutPreference, writeLayoutPreference, type LayoutMode } from '../portalkit/layoutPreference'")
    expect(source).toContain("'railgrid:portal:infrastructure:templates-layout'")
    expect(source).toContain('ref<LayoutMode>(readLayoutPreference(layoutPreferenceKey))')
    expect(source).toContain('watch(layout, mode => writeLayoutPreference(layoutPreferenceKey, mode))')
    const toolbar = source.match(/<div class="filters">([\s\S]*?)<\/div>/)?.[1] ?? ''
    expect(toolbar.match(/<ResourceTableFilter\b[\s\S]*?\/>/g)).toEqual([
      expect.stringContaining('v-model="category"'),
      expect.stringContaining('v-model="cloud"'),
    ])
    expect(toolbar).toContain('<LayoutSelector v-model="layout" aria-label="Template layout"')
    const header = source.match(/<header class="page-head">([\s\S]*?)<\/header>/)?.[1] ?? ''
    expect(header).not.toContain('LayoutSelector')
  })

  it('keeps catalog controls aligned and allows safe wrapping at narrow widths', () => {
    expect(styles).toContain(`railgrid-provider-infrastructure .filters {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}`)
  })

  it('keeps card geometry for grid mode and uses ResourceTable for list mode', () => {
    expect(source).toContain('v-if="layout === \'grid\'"')
    expect(source).toContain('class="catalog-loading-grid k-delayed-loading"')
    expect(source).toContain('<TemplateCard')
    expect(source).toMatch(/<ResourceTable[\s\S]*:loaded="loaded"[\s\S]*:loading="loading"/)
    expect(source).toMatch(/<ResourceTable[\s\S]*searchable[\s\S]*paginated/)
    expect(source).not.toContain('variant="simple"')
    expect(source).toContain(':row-aria-label="(row) => `Provision template ${String(row.identity)}`"')
    expect(source).toContain('@row-click="selectTemplateRow"')
  })

  it('shows the supported template fields without inventing a cloud column', () => {
    const columns = source.match(/const tableColumns = \[([\s\S]*?)\]/)?.[1] ?? ''
    expect(columns).toContain("{ key: 'identity', label: 'Template', primary: true }")
    expect(columns).toContain("{ key: 'category', label: 'Category' }")
    expect(columns).toContain("{ key: 'kind', label: 'Kind' }")
    expect(columns).toContain("{ key: 'version', label: 'Version' }")
    expect(columns).toContain("{ key: 'exposure', label: 'Exposure' }")
    expect(columns).not.toContain("key: 'cloud'")
    expect(source).toContain(":search-keys=\"['identity', 'name', 'description', 'category', 'kind']\"")
  })

  it('retains true-empty refresh and filter-empty clearing in both layouts', () => {
    expect(source.match(/Refresh catalog/g)).toHaveLength(2)
    expect(source.match(/Clear filters/g)).toHaveLength(2)
    expect(source).toContain("'No infrastructure templates are available in this workspace.'")
    expect(source).toContain("'No templates match the current filters.'")
})
    expect(styles).toMatch(/\.grid \{[\s\S]*grid-template-columns: repeat\(auto-fill, minmax\(min\(100%, 280px\), 480px\)\)[\s\S]*justify-content: start/)
  })
