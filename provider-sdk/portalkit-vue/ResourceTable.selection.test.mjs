import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from '../../portal/node_modules/typescript/lib/typescript.js'
import { computed, effectScope, nextTick, reactive, ref, watch } from '../../portal/node_modules/vue/index.mjs'

test('resource page background reads leave recovery idle and queued retries announce immediately', async () => {
  const source = readFileSync(new URL('./ResourcePage.vue', import.meta.url), 'utf8')
  const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
  const parsed = ts.createSourceFile('ResourcePage.ts', script, ts.ScriptTarget.Latest, true)
  const names = new Set(['explicitReadState', 'showInitialError', 'initialReadPending', 'retryRequested', 'retrying', 'refreshAnnouncement', 'ariaBusy'])
  const declarations = parsed.statements.filter(statement =>
    (ts.isVariableStatement(statement) && statement.declarationList.declarations.some(declaration => names.has(declaration.name.getText(parsed))))
    || (ts.isFunctionDeclaration(statement) && statement.name?.text === 'requestRetry')
    || statement.getText(parsed).startsWith('watch(() => props.loading,')
    || statement.getText(parsed).startsWith('watch(() => props.error,'),
  )
  const code = ts.transpileModule(declarations.map(statement => statement.getText(parsed)).join('\n'), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
  }).outputText
  const props = reactive({ loaded: true, loading: true, error: 'Refresh unavailable', refreshMode: 'background', title: 'Repository' })
  const emitted = []
  const scope = effectScope()
  const state = scope.run(() => runInNewContext(`${code}\n({ requestRetry, retrying, ariaBusy, showInitialError, initialReadPending, refreshAnnouncement })`, {
    computed, ref, watch, props, emit: event => emitted.push(event),
  }))
  try {
    assert.equal(state.retrying.value, false, 'a timer does not spin or disable recovery')
    assert.equal(state.refreshAnnouncement.value, 'Updating Repository…')
    state.requestRetry()
    state.requestRetry()
    assert.deepEqual(emitted, ['retry'])
    assert.equal(state.retrying.value, true)
    assert.equal(state.ariaBusy.value, true)
    assert.equal(state.refreshAnnouncement.value, 'Retrying Repository…', 'queued retry has immediate feedback')
    props.loading = false
    await nextTick()
    assert.equal(state.retrying.value, false)
    props.loaded = false
    assert.equal(state.showInitialError.value, true)
    state.requestRetry()
    assert.equal(state.initialReadPending.value, false, 'keep the useful error through retry')
    assert.equal(state.ariaBusy.value, true, 'busy feedback precedes caller acknowledgement')
    props.error = null
    props.loaded = true
    await nextTick()
    assert.equal(state.retrying.value, false)
    assert.equal(state.ariaBusy.value, false)
  } finally {
    scope.stop()
  }
})

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
  assert.match(component, /checkbox && !checkbox\.disabled && region && checkboxIsVisibleInScrollRegion\(checkbox, region\)/)
  assert.match(component, /region\.focus\(\{ preventScroll: true \}\)/)
  assert.match(component, /class="k-table__selection-live" role="status" aria-live="polite"/)
  assert.match(component, /selectionAnnouncement\.value = count > 0[\s\S]*?'Selection cleared\.'/)
  assert.match(component, /watch\(\[currentQuery, filterSignature\], \(\) => clearSelection\(\), \{ flush: 'sync' \}\)/)
  assert.match(component, /props\.loaded !== true[\s\S]*?\|\| props\.loading[\s\S]*?\|\| props\.error[\s\S]*?\|\| props\.stale[\s\S]*?\|\| props\.selectionDisabled/)
  assert.match(component, /props\.paginationMode === 'server'/)
  assert.match(component, /duplicateRowKeys/)
  assert.match(component, /'a, button, input, label, select, textarea, summary/)
  assert.match(component, /k-table__selection-help[\s\S]*?:aria-describedby="selectionReasonID\(i\)"/)
  assert.match(component, /'k-table__row--selected': selectionSurfaceVisible && isRowSelected\(row\)/)
})

test('empty inventory visibility preserves filtered and off-page selection controls', () => {
  const script = component.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
  const parsed = ts.createSourceFile('ResourceTable.ts', script, ts.ScriptTarget.Latest, true)
  const names = ['confirmedEmptyInventory', 'selectionSurfaceVisible', 'renderedColumnCount', 'showControls']
  const declarations = names.map(name => {
    const statement = parsed.statements.find(node => ts.isVariableStatement(node) &&
      node.declarationList.declarations.some(declaration => declaration.name.getText(parsed) === name))
    assert.ok(statement, `${name} exists in production`)
    return statement.getText(parsed)
  })
  const context = {
    computed: getter => ({ get value() { return getter() } }),
    props: { loaded: true, rows: [], selectable: true, loading: false },
    activeFilters: { value: false }, isServerPagination: { value: false },
    serverTotal: { value: null }, serverHasNext: { value: false },
    selectedCount: { value: 0 }, visibleColumns: { value: [{ key: 'app' }, { key: 'user' }] },
    hasConfiguredControls: { value: true },
  }
  const code = ts.transpileModule(declarations.join('\n'), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
  }).outputText
  const state = runInNewContext(`${code}\n({ selectionSurfaceVisible, renderedColumnCount, showControls })`, context)
  assert.equal(state.selectionSurfaceVisible.value, false, 'empty inventory has no selection gutter')
  assert.equal(state.renderedColumnCount.value, 2, 'empty content spans only visible data columns')
  assert.equal(state.showControls.value, false, 'empty client inventory has no blank search toolbar')
  context.props.loading = true
  assert.equal(state.showControls.value, false, 'an authoritative empty snapshot keeps the same toolbar geometry during refresh')
  context.props.loading = false

  context.props.rows = [{ app: 'Reports' }]
  assert.equal(state.selectionSurfaceVisible.value, true)
  assert.equal(state.renderedColumnCount.value, 3)
  assert.equal(state.showControls.value, true)

  context.props.rows = []
  context.activeFilters.value = true
  assert.equal(state.showControls.value, true, 'a zero-match query can be cleared')
  assert.equal(state.selectionSurfaceVisible.value, true, 'filtering does not change the selection column layout')

  context.activeFilters.value = false
  context.isServerPagination.value = true
  context.serverTotal.value = 0
  assert.equal(state.selectionSurfaceVisible.value, false, 'known empty server inventory omits selection')
  context.selectedCount.value = 1
  assert.equal(state.selectionSurfaceVisible.value, true, 'off-page selection retains Clear and actions')
  context.selectedCount.value = 0
  context.serverTotal.value = null
  assert.equal(state.selectionSurfaceVisible.value, true, 'an empty page does not prove the server inventory is empty')

  assert.match(component, /v-if="showControls \|\| selectionSurfaceVisible" class="k-table__toolbar-stack"/)
  assert.match(component, /<thead v-if="!confirmedEmptyInventory \|\| selectedCount > 0"/)
  assert.match(component, /<th v-if="selectionSurfaceVisible" class="k-table__heading k-table__selection-heading"/)
  assert.match(component, /<span v-if="selectable" class="k-table__selection-live"/)
})

test('table retries keep useful errors, announce progress immediately, and serialize activation', async () => {
  const script = component.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
  const parsed = ts.createSourceFile('ResourceTable.ts', script, ts.ScriptTarget.Latest, true)
  const names = new Set(['explicitReadState', 'retryRequested', 'retrying', 'showInitialError', 'initialReadPending', 'ariaBusy', 'tableAriaLabel', 'refreshAnnouncement'])
  const declarations = parsed.statements.filter(statement =>
    (ts.isVariableStatement(statement) && statement.declarationList.declarations.some(declaration => names.has(declaration.name.getText(parsed))))
    || (ts.isFunctionDeclaration(statement) && statement.name?.text === 'requestRetry')
    || statement.getText(parsed).startsWith('watch(() => props.loading,')
    || statement.getText(parsed).startsWith('watch(() => props.error,'),
  )
  const code = ts.transpileModule(declarations.map(statement => statement.getText(parsed)).join('\n'), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
  }).outputText
  const props = reactive({ loaded: false, loading: false, error: 'Connection unavailable', refreshMode: 'foreground', ariaLabel: 'Repositories' })
  const emitted = []
  const scope = effectScope()
  const state = scope.run(() => runInNewContext(`${code}\n({ requestRetry, retrying, ariaBusy, showInitialError, initialReadPending, refreshAnnouncement })`, {
    computed, ref, watch, props, filterPending: ref(false), emit: event => emitted.push(event),
  }))
  try {
    state.requestRetry()
    state.requestRetry()
    assert.deepEqual(emitted, ['retry'], 'double activation emits one foreground read')
    assert.equal(state.retrying.value, true)
    assert.equal(state.ariaBusy.value, true, 'queued user retry exposes immediate busy feedback')
    assert.equal(state.refreshAnnouncement.value, 'Retrying repositories…')
    assert.equal(state.showInitialError.value, true, 'the useful initial error remains visible')
    assert.equal(state.initialReadPending.value, false, 'retry does not replace the error with a skeleton')
    props.loading = true
    await nextTick()
    props.loading = false
    await nextTick()
    assert.equal(state.retrying.value, false, 'failed retry can be invoked again after the caller settles')
    assert.equal(state.ariaBusy.value, false)
    props.loaded = true
    props.error = null
    props.loading = true
    await nextTick()
    assert.equal(state.refreshAnnouncement.value, 'Refreshing repositories…')
    props.refreshMode = 'background'
    assert.equal(state.refreshAnnouncement.value, 'Updating repositories…')
    props.error = 'Background connection unavailable'
    assert.equal(state.refreshAnnouncement.value, 'Updating repositories…', 'timer refresh is not announced as a user retry')
    assert.equal(state.retrying.value, false, 'timer refresh does not spin or disable recovery actions')
  } finally { scope.stop() }
  assert.equal((component.match(/:disabled="retrying" :aria-busy="retrying \|\| undefined" @click="requestRetry"/g) || []).length, 2)
  assert.match(component, /class="k-table__empty-label" role="status" aria-live="polite"/)
})

test('searchable table facets leave their teleported search field in source Tab order', () => {
  const filter = readFileSync(new URL('./ResourceTableFilter.vue', import.meta.url), 'utf8')
  const script = filter.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
  const parsed = ts.createSourceFile('ResourceTableFilter.ts', script, ts.ScriptTarget.Latest, true)
  const handler = parsed.statements.find(statement => ts.isFunctionDeclaration(statement) && statement.name?.text === 'onSearchKeydown')
  assert.ok(handler)
  const code = ts.transpileModule(handler.getText(parsed), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  const events = []
  const keydown = runInNewContext(`${code}\nonSearchKeydown`, {
    closeFilter: () => events.push('close'), trigger: { value: { focus: () => events.push('focus source trigger') } },
  })
  keydown({ key: 'Tab', preventDefault: () => events.push('blocked native navigation') })
  assert.deepEqual(events, ['close', 'focus source trigger'], 'popup closes and native Tab starts from the source anchor')
})
