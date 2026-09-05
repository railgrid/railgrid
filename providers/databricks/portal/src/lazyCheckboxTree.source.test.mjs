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
  assert.match(source, /class="k-checkbox" type="checkbox" tabindex="-1" aria-hidden="true"/)
  assert.match(source, /@mousedown\.prevent/)
  assert.match(source, /@click\.stop="focusKey\(id\)"/)
  assert.match(source, /@change="checkboxChanged\(id, \$event\)"/)
  assert.match(source, /function checkboxChanged\(id: string, event: Event\)[\s\S]*event\.currentTarget as HTMLInputElement\)\.checked/)
  assert.doesNotMatch(source, /@pointerdown\.prevent/)
  assert.doesNotMatch(source, /@click[^>]*\.prevent/)
  assert.doesNotMatch(source, /class="k-checkbox"[^>]*tabindex="0"/)
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

test('wizard backdrop preserves the viewport scrim while honoring AppLayout insets', async () => {
  const styles = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  const backdrop = styles.match(/faros-provider-databricks \.import-backdrop\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
  assert.match(backdrop, /inset:\s*0;/)
  assert.match(backdrop, /position:\s*fixed;/)
  assert.match(backdrop, /padding:\s*24px calc\(24px \+ var\(--app-inset-right, 0px\)\) calc\(24px \+ var\(--app-inset-bottom, 0px\)\) calc\(24px \+ var\(--app-inset-left, 0px\)\);/)
  assert.match(styles, /@media \(max-width: 620px\) \{[\s\S]*?faros-provider-databricks \.import-backdrop\s*\{[\s\S]*?padding:\s*10px calc\(10px \+ var\(--app-inset-right, 0px\)\) calc\(10px \+ var\(--app-inset-bottom, 0px\)\) calc\(10px \+ var\(--app-inset-left, 0px\)\);/)
})

test('wizard focus trap follows the natural tab sequence', async () => {
  const source = await readFile(new URL('./ResourceImportWizard.vue', import.meta.url), 'utf8')
  assert.match(source, /function dialogTabStops\(\)/)
  assert.match(source, /\.filter\(element => element\.tabIndex === 0\)/)
  assert.match(source, /<header[\s\S]*class="k-icon-action databricks-dialog-close"[\s\S]*data-k-tip="Close import dialog"[\s\S]*aria-label="Close import dialog"/)
  assert.doesNotMatch(source, /class="icon-button"/)
})

test('split create uses canonical buttons and menu items', async () => {
  const source = await readFile(new URL('./components/SplitCreateButton.vue', import.meta.url), 'utf8')
  const styles = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  assert.match(source, /class="k-btn k-btn--primary split-create-main"/)
  assert.match(source, /class="k-btn k-btn--primary split-create-toggle"/)
  assert.doesNotMatch(styles, /\.split-create-toggle\s*\{[^}]*min-width:\s*30px/)
  assert.match(source, /class="split-create-menu k-menu" role="menu"/)
  assert.match(source, /class="k-menu-item split-create-menu-item" type="button" role="menuitem" tabindex="-1"/)
  assert.doesNotMatch(source, /class="(?:secondary|icon-button)[^"]*"/)
})

test('lazy tree uses dense transparent rows without decorative focus glow', async () => {
  const styles = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  const wizard = await readFile(new URL('./ResourceImportWizard.vue', import.meta.url), 'utf8')
  const tree = await readFile(new URL('./LazyCheckboxTree.vue', import.meta.url), 'utf8')
  const row = styles.match(/faros-provider-databricks \.lazy-tree-row \{([^}]*)\}/)?.[1] ?? ''
  const expander = styles.match(/faros-provider-databricks \.lazy-tree-expander \{([^}]*)\}/)?.[1] ?? ''
  const checkboxHit = styles.match(/faros-provider-databricks \.lazy-tree-checkbox-hit \{([^}]*)\}/)?.[1] ?? ''
  const focus = styles.match(/faros-provider-databricks \.lazy-tree-item:focus > \.lazy-tree-row \{([^}]*)\}/)?.[1] ?? ''
  const description = styles.match(/faros-provider-databricks \.lazy-tree-copy small \{([^}]*)\}/)?.[1] ?? ''
  assert.match(row, /background:\s*transparent/)
  assert.match(row, /border:\s*0/)
  assert.match(row, /grid-template-columns:\s*44px\s+44px\s+minmax\(0,\s*1fr\)/)
  assert.match(row, /min-block-size:\s*44px/)
  for (const hitZone of [expander, checkboxHit]) {
    assert.match(hitZone, /height:\s*44px/)
    assert.match(hitZone, /min-height:\s*44px/)
    assert.match(hitZone, /min-width:\s*44px/)
    assert.match(hitZone, /width:\s*44px/)
  }
  assert.doesNotMatch(focus, /box-shadow|accent-glow/)
  assert.match(focus, /outline:/)
  assert.match(styles, /\.lazy-tree-item:focus\s*>\s*\.lazy-tree-row/)
  assert.doesNotMatch(styles, /\.lazy-tree-row input\s*\{/)
  assert.doesNotMatch(styles, /input:not\(\[type="checkbox"\]\):not\(\[type="radio"\]\)/)
  assert.doesNotMatch(styles, /faros-provider-databricks select:focus/)
  assert.match(wizard, /<FormSelect[\s\S]*:options="connectionOptions"/)
  assert.doesNotMatch(wizard, /<select\b/)
  assert.match(wizard, /<input[^>]*class="k-input"/)
  assert.doesNotMatch(styles, /faros-provider-databricks input\s*,/)
  assert.doesNotMatch(styles, /faros-provider-databricks input:focus/)
  assert.match(tree, /function activateFromPointer\(id: string\): void \{\s*focusKey\(id\)\s*activate\(id\)/)
  assert.match(tree, /class="lazy-tree-checkbox-hit" @click\.stop="activateFromPointer\(id\)"/)
  assert.match(tree, /@mousedown\.prevent @click\.stop="focusKey\(id\)"/)
  assert.match(description, /overflow:\s*hidden/)
  assert.match(description, /text-overflow:\s*ellipsis/)
  assert.match(description, /white-space:\s*nowrap/)
})

test('import surfaces keep readable placeholders, semantic status colors, and unique review IDs', async () => {
  const styles = await readFile(new URL('./style.css', import.meta.url), 'utf8')
  const canonicalStyles = await readFile(new URL('../../../../provider-sdk/portalkit/faros-ui.css', import.meta.url), 'utf8')
  const wizard = await readFile(new URL('./ResourceImportWizard.vue', import.meta.url), 'utf8')
  const providerTokens = styles.match(/faros-provider-databricks \{([\s\S]*?)\n\}/)?.[1] ?? ''
  const selectPlaceholder = canonicalStyles.match(/\.k-form-select__value\.is-placeholder \{([^}]*)\}/)?.[1] ?? ''
  const inputPlaceholder = styles.match(/faros-provider-databricks \.import-dialog input::placeholder \{([^}]*)\}/)?.[1] ?? ''
  for (const token of ['accent', 'danger', 'success', 'warning']) {
    assert.match(providerTokens, new RegExp(`--databricks-readable-${token}:\\s*color-mix\\([^;]*var\\(--color-${token}`), `${token} has a foreground-aware semantic token`)
  }
  assert.match(styles, /\.result-state\.created[^}]*color:\s*var\(--databricks-readable-success\)/)
  assert.match(styles, /\.result-state\.existing[^}]*color:\s*var\(--databricks-readable-accent\)/)
  assert.match(styles, /\.result-state\.(?:conflict|failed)[^}]*color:\s*var\(--databricks-readable-danger\)/)
  assert.match(styles, /\.initialization-summary--ready[^}]*color:\s*var\(--databricks-readable-success\)/)
  assert.match(selectPlaceholder, /color:\s*var\(--color-text-secondary/)
  assert.match(inputPlaceholder, /color:\s*var\(--color-text-secondary/)

  assert.match(wizard, /function reviewInputID\(index: number\): string \{ return `import-review-name-\$\{index\}` \}/)
  assert.match(wizard, /function reviewErrorID\(index: number\): string \{ return `\$\{reviewInputID\(index\)\}-error` \}/)
  assert.match(wizard, /:for="reviewInputID\(index\)"[\s\S]*:id="reviewInputID\(index\)"[\s\S]*:aria-invalid="reviewEntryError\(entry\) \? 'true' : undefined"[\s\S]*:aria-describedby="reviewEntryError\(entry\) \? reviewErrorID\(index\) : undefined"/)
  assert.match(wizard, /:id="reviewErrorID\(index\)" class="error" role="alert"/)
})
