import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const source = readFileSync(new URL('./views/CatalogPage.vue', import.meta.url), 'utf8')
const styles = readFileSync(new URL('./style.css', import.meta.url), 'utf8')

describe('Infrastructure template catalog', () => {
  it('places one persistent search control between the cloud guide and results', () => {
    expect(source).not.toContain('LayoutSelector')
    expect(source.match(/type="search"/g)).toHaveLength(1)
    expect(source).not.toContain("<Tabs")
    expect(source).toMatch(/<aside[^>]+catalog-cloud-guide[\s\S]*<div class="catalog-search k-table__search" role="search">[\s\S]*<ResourceTable/)
    expect(source).toContain(':query="query"')
    expect(source).toContain(':search-keys="searchKeys"')
  })

  it('keeps the desktop table columns and stacks their values in narrow rows', () => {
    const columns = source.match(/const tableColumns = \[([\s\S]*?)\]/)?.[1] ?? ''
    expect(columns).toContain("{ key: 'identity', label: 'Template', primary: true, sortable: true }")
    expect(columns).toContain("{ key: 'category', label: 'Category', sortable: true }")
    expect(columns).not.toContain("key: 'kind'")
    expect(columns).toContain("{ key: 'destination', label: 'Deploys\\u00a0to', ariaLabel: 'Deploys to', align: 'center' as const }")
    expect(source).toContain('<span class="catalog-row-label">Category</span> {{ row.category }}')
    expect(source).toContain('<span class="catalog-row-label">Deploys to</span> <DeploymentTarget')
    expect(source).toContain('<template #destination="{ value }"><DeploymentTarget')
    expect(source).toContain(':columns="visibleColumns"')
    expect(styles).toMatch(/\.catalog-row-details \{\s*display: grid;/)
    expect(source).toContain('@row-click="selectTemplateRow"')
  })
})
