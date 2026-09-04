import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const read = path => readFile(new URL(path, import.meta.url), 'utf8')

test('Quickstart keeps code dumps flat inside the two card panels', async () => {
  const source = await read('./element.ts')
  const panels = source.match(/<section class="k-card quickstart-panel">[\s\S]*?<\/section>/g) ?? []

  assert.equal(panels.length, 2)
  for (const panel of panels) {
    assert.equal((panel.match(/<pre\b/g) ?? []).length, 1)
    assert.match(panel, /<pre class="quickstart-dump(?:\s[^\"]*)?"/)
    assert.doesNotMatch(panel, /<pre[^>]*\bk-card(?:\s|\")/)
  }
})

test('Quickstart comparison geometry is locally bounded and container responsive', async () => {
  const styles = await read('./style.css')
  const grid = styles.match(/faros-provider-quickstart \.quickstart-grid\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  const dump = styles.match(/faros-provider-quickstart \.quickstart-dump\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''

  assert.match(grid, /width:\s*100%/)
  assert.match(grid, /max-width:\s*64rem/)
  assert.match(grid, /margin-inline:\s*auto/)
  assert.match(grid, /grid-template-columns:\s*minmax\(0, 1fr\)/)
  assert.match(styles, /container-name:\s*quickstart-provider/)
  assert.match(styles, /container-type:\s*inline-size/)
  assert.match(styles, /@container quickstart-provider \(min-width: 46rem\)[\s\S]*?repeat\(2, minmax\(0, 1fr\)\)/)
  assert.match(styles, /@supports not \(container-type: inline-size\)[\s\S]*?@media \(min-width: 46rem\)[\s\S]*?repeat\(2, minmax\(0, 1fr\)\)/)
  assert.doesNotMatch(styles, /@supports not \(container-type: inline-size\)[\s\S]*?@media \(min-width: \d+px\)/)

  assert.match(dump, /background:\s*var\(--color-surface-overlay/)
  assert.match(dump, /border:\s*1px solid var\(--color-border-subtle/)
  assert.match(dump, /border-radius:\s*4px/)
  assert.match(dump, /box-shadow:\s*none/)
})
