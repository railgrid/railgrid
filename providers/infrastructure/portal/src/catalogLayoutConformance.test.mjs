import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const source = readFileSync(new URL('./views/CatalogPage.vue', import.meta.url), 'utf8')
const styles = readFileSync(new URL('./style.css', import.meta.url), 'utf8')

describe('Infrastructure template catalog layouts', () => {
  it('persists a grid-default layout through the shared preference contract', () => {
    expect(source).toContain("import LayoutSelector from '../portalkit/LayoutSelector.vue'")
    expect(source).toContain("import { readLayoutPreference, writeLayoutPreference, type LayoutMode } from '../portalkit/layoutPreference'")
    expect(source).toContain("'faros:portal:infrastructure:templates-layout'")
    expect(source).toContain('ref<LayoutMode>(readLayoutPreference(layoutPreferenceKey))')
    expect(source).toContain('watch(layout, mode => writeLayoutPreference(layoutPreferenceKey, mode))')
    expect(source).toMatch(/<div class="filters">[\s\S]*All categories[\s\S]*<LayoutSelector v-model="layout" aria-label="Template layout"[\s\S]*<\/div>/)
    const header = source.match(/<header class="page-head">([\s\S]*?)<\/header>/)?.[1] ?? ''
    expect(header).not.toContain('LayoutSelector')
  })

  it('keeps catalog controls aligned and allows safe wrapping at narrow widths', () => {
    expect(styles).toContain(`faros-provider-infrastructure .filters {
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
    expect(source).not.toContain('variant="simple"')
    expect(source).toContain(':row-aria-label="(row) => `Provision template ${String(row.identity)}`"')
    expect(source).toContain('@row-click="selectTemplateRow"')
  })

  it('shows the supported template fields without inventing a cloud column', () => {
    expect(source).toContain("{ key: 'identity', label: 'Template', primary: true }")
    expect(source).toContain("{ key: 'category', label: 'Category' }")
    expect(source).toContain("{ key: 'kind', label: 'Kind' }")
    expect(source).toContain("{ key: 'version', label: 'Version' }")
    expect(source).toContain("{ key: 'exposure', label: 'Exposure' }")
    expect(source).not.toContain("{ key: 'cloud', label: 'Cloud' }")
    expect(source).toContain('<span class="template-card-desc">{{ row.description }}</span>')
  })

  it('retains true-empty refresh and filter-empty clearing in both layouts', () => {
    expect(source.match(/Refresh catalog/g)).toHaveLength(2)
    expect(source.match(/Clear filters/g)).toHaveLength(2)
    expect(source).toContain("'No infrastructure templates are available in this workspace.'")
    expect(source).toContain("'No templates match the current filters.'")
  })
})
