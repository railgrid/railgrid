import { describe, expect, it } from 'vitest'

const source = import.meta.glob('./views/InstanceListPage.vue', { query: '?raw', import: 'default', eager: true })['./views/InstanceListPage.vue'] as string

describe('Infrastructure resource table conformance', () => {
  it('provides an action-oriented accessible name for interactive instance rows', () => {
    expect(source).toMatch(/:row-aria-label="\(row\) => `Open instance \$\{String\(rowInstance\(row\)\.name\)\}`"/)
  })

  it('preserves the authoritative table body during adaptive background refresh', () => {
    expect(source).toContain("load('background')")
    expect(source).toContain(':refresh-mode="refreshMode"')
    expect(source).toContain("@retry=\"load('foreground')\"")
    expect(source).not.toContain('window.setInterval')
  })
})
