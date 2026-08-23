import { describe, expect, it } from 'vitest'

const source = import.meta.glob('./views/InstanceListPage.vue', { query: '?raw', import: 'default', eager: true })['./views/InstanceListPage.vue'] as string

describe('Infrastructure resource table conformance', () => {
  it('provides an action-oriented accessible name for interactive instance rows', () => {
    expect(source).toMatch(/:row-aria-label="\(row\) => `Open instance \$\{String\(rowInstance\(row\)\.name\)\}`"/)
  })
})
