import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

test('lazy checkbox tree exposes the composite ARIA and keyboard contract', async () => {
  const source = await readFile(new URL('./LazyCheckboxTree.vue', import.meta.url), 'utf8')
  for (const contract of [
    /role="treeitem"/,
    /'tree' : 'group'/,
    /aria-multiselectable/,
    /aria-expanded/,
    /aria-checked/,
    /aria-disabled/,
    /tabindex="-1"/,
    /ArrowDown/,
    /ArrowUp/,
    /ArrowRight/,
    /ArrowLeft/,
    /Home/,
    /End/,
    /event\.key === ' '/,
    /event\.key === 'Enter'/,
    /data-tree-focus/,
    /role="treeitem" type="button"/,
  ]) assert.match(source, contract)
  assert.match(source, /function expand\(id: string\)[\s\S]*focusKey\(id\)[\s\S]*emit\('expand', id\)/)
  assert.match(source, /aria-hidden="true"/)
  assert.doesNotMatch(source, /type="checkbox"[\s\S]{0,200}:aria-label/)
  assert.match(source, /getAttribute\('aria-disabled'\) !== 'true'/)
})

test('wizard separates source and browse and registers only from confirmation', async () => {
  const source = await readFile(new URL('./ResourceImportWizard.vue', import.meta.url), 'utf8')
  assert.match(source, /type Step = 'source' \| 'browse' \| 'review' \| 'results'/)
  assert.match(source, />Source<\/li>.*>Browse<\/li>.*>Review<\/li>/s)
  assert.match(source, /v-else-if="step === 'review'"/)
  assert.match(source, /@click="register"/)
  assert.doesNotMatch(source, /registerResources[\s\S]*function review\(/)
})

test('wizard stepper allocates one column to each of its four stages', async () => {
  const styles = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  assert.match(styles, /\.import-steps\s*\{[\s\S]*?grid-template-columns:\s*repeat\(4,/)
})

test('wizard focus trap follows the natural tab sequence', async () => {
  const source = await readFile(new URL('./ResourceImportWizard.vue', import.meta.url), 'utf8')
  assert.match(source, /function dialogTabStops\(\)/)
  assert.match(source, /\.filter\(element => element\.tabIndex === 0\)/)
  assert.match(source, /<header[\s\S]*class="icon-button"[\s\S]*aria-label="Close"/)
})

test('lazy tree uses dense transparent rows without decorative focus glow', async () => {
  const styles = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  const row = styles.match(/faros-provider-databricks \.lazy-tree-row \{([^}]*)\}/)?.[1] ?? ''
  const focus = styles.match(/faros-provider-databricks \.lazy-tree-item:focus > \.lazy-tree-row \{([^}]*)\}/)?.[1] ?? ''
  const description = styles.match(/faros-provider-databricks \.lazy-tree-copy small \{([^}]*)\}/)?.[1] ?? ''
  assert.match(row, /background:\s*transparent/)
  assert.match(row, /border:\s*0/)
  assert.match(row, /min-height:\s*30px/)
  assert.doesNotMatch(focus, /box-shadow|accent-glow/)
  assert.match(focus, /outline:/)
  assert.match(description, /overflow:\s*hidden/)
  assert.match(description, /text-overflow:\s*ellipsis/)
  assert.match(description, /white-space:\s*nowrap/)
})
