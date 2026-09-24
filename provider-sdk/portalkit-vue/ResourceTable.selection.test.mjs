import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from '../../portal/node_modules/typescript/lib/typescript.js'

const component = readFileSync(new URL('./ResourceTable.vue', import.meta.url), 'utf8')
const tableSource = readFileSync(new URL('./table.ts', import.meta.url), 'utf8')
const tableModule = ts.transpileModule(tableSource, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText
const table = await import(`data:text/javascript;base64,${Buffer.from(tableModule).toString('base64')}`)

test('page selection updates only eligible current-page keys and preserves other pages', () => {
  const current = ['off-page', 7]
  const selected = table.updatePageSelection(current, ['visible', 7], true)
  assert.deepEqual(selected, ['off-page', 7, 'visible'])
  assert.deepEqual(table.updatePageSelection(selected, ['visible', 7], false), ['off-page'])
  assert.deepEqual(table.updatePageSelection(['1'], [1], true), ['1', 1])
})

test('page checkbox state counts unique stable identities and exposes mixed state', () => {
  const pageKeys = table.uniqueSelectionKeys(['first', 'first', 2])
  assert.deepEqual(table.selectionPageState(['first'], pageKeys), {
    allSelected: false,
    partiallySelected: true,
  })
  assert.deepEqual(table.selectionPageState(['first', 2], pageKeys), {
    allSelected: true,
    partiallySelected: false,
  })
  assert.deepEqual(table.selectionPageState(['off-page'], []), {
    allSelected: false,
    partiallySelected: false,
  })
})

test('client pruning uses authoritative eligibility and preserves string/number key identity', () => {
  assert.deepEqual(table.pruneClientSelectionKeys(
    ['removed', 2, '2', 'ineligible', 2],
    [
      { key: 2, selectable: true },
      { key: '2', selectable: true },
      { key: 'ineligible', selectable: false },
    ],
  ), [2, '2'])
})

test('ResourceTable selection is controlled, page-scoped, accessible, and opt-in', () => {
  assert.match(component, /selectable\?: boolean/)
  assert.match(component, /selectedKeys\?: TableSelectionKey\[\]/)
  assert.match(component, /'update:selectedKeys': \[keys: TableSelectionKey\[\]\]/)
  assert.match(component, /rowSelectable\?: \(row: Record<string, unknown>\) => boolean/)
  assert.match(component, /rowSelectionDisabledReason\?: \(row: Record<string, unknown>\) => string/)
  assert.match(component, /selectionLabel\?: \(row: Record<string, unknown>\) => string/)
  assert.match(component, /rowKey\?: string \| \(\(row: Record<string, unknown>, index: number\) => string \| number\)/)
  assert.match(component, /props\.rowKey\(row, props\.rows\.indexOf\(row\)\)/)
  assert.match(component, /const eligiblePageKeys = computed\(\(\) => uniqueSelectionKeys\(visibleRows\.value\.flatMap/)
  assert.match(component, /:indeterminate="selectionOnPage\.partiallySelected"/)
  assert.match(component, /:aria-checked="selectionOnPage\.partiallySelected \? 'mixed'/)
  assert.match(component, /:disabled="selectionDisabled \|\| eligiblePageKeys\.length === 0"/)
  assert.match(component, /class="k-table__checkbox"\s+type="checkbox"/)
  assert.match(component, /class="k-table__selection-bar"/)
  assert.match(component, /class="k-table__selection-summary"[\s\S]*?{{ selectedCount }} selected/)
  assert.match(component, /name="selection-actions" :selectedKeys="normalizedSelectedKeys" :keys="normalizedSelectedKeys" :count="selectedCount"/)
  assert.match(component, /:disabled="clearSelectionDisabled" @click="clearSelectionFromToolbar"/)
  assert.match(component, /k-table__toolbar-stack/)
  assert.match(component, /:inert="selectionToolbarActive"/)
  assert.match(component, /:inert="!selectionToolbarActive"/)
  assert.match(component, /function checkboxIsVisibleInScrollRegion\(checkbox: HTMLInputElement, region: HTMLElement\): boolean/)
  assert.match(component, /checkbox && !checkbox\.disabled && region && checkboxIsVisibleInScrollRegion\(checkbox, region\)[\s\S]*?: region/)
  assert.match(component, /target\?\.focus\(\{ preventScroll: true \}\)/)
  assert.match(component, /class="k-table__selection-live" role="status" aria-live="polite"/)
  assert.match(component, /selectionAnnouncement\.value = count > 0[\s\S]*?'Selection cleared\.'/)
  assert.match(component, /watch\(\[currentQuery, filterSignature\], \(\) => clearSelection\(\), \{ flush: 'sync' \}\)/)
  assert.match(component, /props\.loaded !== true[\s\S]*?\|\| props\.loading[\s\S]*?\|\| props\.error[\s\S]*?\|\| props\.stale[\s\S]*?\|\| props\.selectionDisabled/)
  assert.match(component, /props\.paginationMode === 'server'/)
  assert.match(component, /duplicateRowKeys/)
  assert.match(component, /'a, button, input, label, select, textarea, summary/)
  assert.match(component, /k-table__selection-help[\s\S]*?:aria-describedby="selectionReasonID\(i\)"/)
  assert.match(component, /'k-table__row--selected': selectable && isRowSelected\(row\)/)
})
