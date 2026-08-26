import { readFile } from 'node:fs/promises'
import { describe, expect, it } from 'vitest'

const sourceFiles = [
  'DashboardTile.vue',
  'views/ConnectionDetailView.vue',
  'views/ConnectionsView.vue',
  'views/PackagesView.vue',
  'views/RepoDetailView.vue',
  'views/RepositoriesView.vue',
]
const sources = await Promise.all(sourceFiles.map(async path => [
  path,
  await readFile(new URL(`./${path}`, import.meta.url), 'utf8'),
]))
const style = await readFile(new URL('./style.css', import.meta.url), 'utf8')
const farosUI = await readFile(new URL('./portalkit/faros-ui.css', import.meta.url), 'utf8')

describe('Code portal conformance', () => {
  it('uses canonical k-* controls and scoped Code layout hooks', () => {
  const source = sources.map(([, content]) => content).join('\n')
  expect(source).not.toMatch(/(?:class|:class)="[^"]*(?:^|\s)(?:link|badge|actions|row-actions)(?=\s|")/m)
  expect(style).not.toMatch(/\.(?:link|badge|actions|row-actions)\b|button\.(?:link|primary|secondary|danger)\b/)
  expect(source).toMatch(/k-btn k-btn--ghost code-inline-action/)
  expect(source).toMatch(/k-badge k-badge--muted/)
  expect(source).toMatch(/code-form-actions/)
  expect(source).toMatch(/code-row-actions/)
  })

  it('names every interactive resource-table row', () => {
    const source = sources.map(([, content]) => content).join('\n')
    expect(source).toMatch(/:row-aria-label="\(row\) => `Open repository/)
    expect(source).toMatch(/:row-aria-label="\(row\) => `Open connection/)
  })

  it('uses the canonical resource-link treatment for navigable table identities', () => {
    const source = sources.map(([, content]) => content).join('\n')
    expect(source.match(/k-table-resource-link/g)?.length).toBeGreaterThanOrEqual(3)
    expect(farosUI).toMatch(/\.k-table-resource-link\s*\{[\s\S]*color: var\(--color-accent[\s\S]*font-weight: 400[\s\S]*padding: 0;/)
  })
})
