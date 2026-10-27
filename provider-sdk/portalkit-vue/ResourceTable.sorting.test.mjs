import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from '../../portal/node_modules/typescript/lib/typescript.js'
import { computed, reactive, ref } from '../../portal/node_modules/vue/index.mjs'

const component = readFileSync(new URL('./ResourceTable.vue', import.meta.url), 'utf8')
const styles = readFileSync(new URL('../portalkit/railgrid-ui.css', import.meta.url), 'utf8')
const tableSource = readFileSync(new URL('./table.ts', import.meta.url), 'utf8')
const tableModule = ts.transpileModule(tableSource, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText
const table = await import('data:text/javascript;base64,' + Buffer.from(tableModule).toString('base64'))
const plain = value => JSON.parse(JSON.stringify(value))

const script = component.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)?.[1]
assert.ok(script, 'ResourceTable has a script setup block')
const parsed = ts.createSourceFile('ResourceTable.ts', script, ts.ScriptTarget.Latest, true)

function extractDeclarations(variableNames, functionNames = []) {
  const variables = new Set(variableNames)
  const functions = new Set(functionNames)
  const statements = parsed.statements.filter(statement =>
    (ts.isVariableStatement(statement)
      && statement.declarationList.declarations.some(declaration => variables.has(declaration.name.getText(parsed))))
    || (ts.isFunctionDeclaration(statement) && functions.has(statement.name?.text)),
  )
  const foundVariables = new Set(statements
    .filter(ts.isVariableStatement)
    .flatMap(statement => statement.declarationList.declarations.map(declaration => declaration.name.getText(parsed))))
  const foundFunctions = new Set(statements
    .filter(ts.isFunctionDeclaration)
    .map(statement => statement.name?.text))
  for (const name of variables) assert.ok(foundVariables.has(name), name + ' exists in ResourceTable')
  for (const name of functions) assert.ok(foundFunctions.has(name), name + ' exists in ResourceTable')
  return ts.transpileModule(statements.map(statement => statement.getText(parsed)).join('\n'), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
  }).outputText
}

test('sorts typed values stably, keeps null and invalid values last, and leaves input untouched', () => {
  const rows = [
    { id: 'ten', value: 10 },
    { id: 'missing', value: null },
    { id: 'two-a', value: 2 },
    { id: 'nan', value: Number.NaN },
    { id: 'two-b', value: 2 },
    { id: 'infinity', value: Number.POSITIVE_INFINITY },
    { id: 'zero', value: 0 },
    { id: 'undefined', value: undefined },
  ]
  const before = [...rows]
  let accessorCalls = 0
  const ascending = table.sortTableRows(rows, { key: 'value', direction: 'asc' }, row => {
    accessorCalls += 1
    return row.value
  })
  assert.deepEqual(ascending.map(row => row.id), [
    'zero', 'two-a', 'two-b', 'ten', 'missing', 'nan', 'infinity', 'undefined',
  ])
  assert.equal(accessorCalls, rows.length, 'the sort accessor runs once per row')
  assert.deepEqual(rows, before, 'sorting does not mutate the source array')

  const descending = table.sortTableRows(rows, { key: 'value', direction: 'desc' }, row => row.value)
  assert.deepEqual(descending.map(row => row.id), [
    'ten', 'two-a', 'two-b', 'zero', 'missing', 'nan', 'infinity', 'undefined',
  ])
  assert.notEqual(descending, rows)
})

test('uses a transitive type order for mixed numeric and string values', () => {
  const rows = [
    { id: 'number-negative-ten', value: -10 },
    { id: 'string-negative-two', value: '-2' },
    { id: 'number-negative-two', value: -2 },
  ]
  assert.deepEqual(
    table.sortTableRows(rows, { key: 'value', direction: 'asc' }, row => row.value).map(row => row.id),
    ['number-negative-ten', 'number-negative-two', 'string-negative-two'],
  )
})

test('client mode sorts the filtered result before pagination while server mode preserves supplied rows', () => {
  const code = extractDeclarations(['sortedRows', 'visibleRows'])

  const allRows = [
    { id: 'three', state: 'ready', score: 3 },
    { id: 'one', state: 'ready', score: 1 },
    { id: 'skip', state: 'paused', score: 0 },
    { id: 'two', state: 'ready', score: 2 },
  ]
  const serverMode = ref(false)
  const clientProps = reactive({ rows: allRows, columns: [{ key: 'score', sortable: true }] })
  const client = runInNewContext(code + '\n({ sortedRows, visibleRows })', {
    computed,
    props: clientProps,
    currentSort: ref({ key: 'score', direction: 'asc' }),
    isServerPagination: computed(() => serverMode.value),
    filteredRows: computed(() => table.filterTableRows(clientProps.rows, 'ready', ['state'], {})),
    paginationEnabled: ref(true),
    currentPage: ref(2),
    currentPageSize: ref(2),
    sortTableRows: table.sortTableRows,
    paginateTableRows: table.paginateTableRows,
  })
  assert.deepEqual(client.sortedRows.value.map(row => row.id), ['one', 'two', 'three'])
  assert.deepEqual(client.visibleRows.value.map(row => row.id), ['three'], 'the second page comes from the sorted filtered set')

  const serverRows = [
    { id: 'three', score: 3 },
    { id: 'one', score: 1 },
    { id: 'two', score: 2 },
  ]
  serverMode.value = true
  const serverProps = reactive({ rows: serverRows, columns: [{ key: 'score', sortable: true }] })
  const server = runInNewContext(code + '\n({ visibleRows })', {
    computed,
    props: serverProps,
    currentSort: ref({ key: 'score', direction: 'asc' }),
    isServerPagination: computed(() => serverMode.value),
    filteredRows: computed(() => serverProps.rows),
    paginationEnabled: ref(true),
    currentPage: ref(2),
    currentPageSize: ref(1),
    sortTableRows: table.sortTableRows,
    paginateTableRows: table.paginateTableRows,
  })
  assert.equal(server.visibleRows.value, serverProps.rows, 'server mode renders the exact supplied page array')
  assert.deepEqual(server.visibleRows.value.map(row => row.id), ['three', 'one', 'two'])
})

test('controlled and default sort state emit page-one snapshots without clearing selection', () => {
  const code = extractDeclarations(
    ['localSort', 'currentSort', 'sortingConfigured'],
    ['cloneTableSort', 'snapshotSort', 'nextSortDirection', 'sortActionLabel', 'sortAriaValue', 'emitChange', 'setSort', 'toggleSort'],
  )
  const props = reactive({
    sort: undefined,
    defaultSort: { key: 'name', direction: 'asc' },
    page: undefined,
    columns: [
      { key: 'name', label: 'Name', sortable: true },
      { key: 'createdAt', label: 'Created', sortable: true, sortDefaultDirection: 'desc' },
    ],
  })
  const emitted = []
  const resets = []
  const state = runInNewContext(code + '\n({ localSort, currentSort, sortingConfigured, nextSortDirection, sortActionLabel, sortAriaValue, toggleSort, emitChange })', {
    props,
    ref,
    computed,
    page: ref(4),
    currentPageSize: ref(25),
    currentQuery: ref(''),
    snapshotFilters: () => ({}),
    resetCursorHistory: () => resets.push('reset'),
    emit: (event, payload) => emitted.push({ event, payload }),
  })

  assert.deepEqual(plain(state.currentSort.value), { key: 'name', direction: 'asc' }, 'defaultSort initializes uncontrolled state')
  assert.equal(state.sortAriaValue(props.columns[0]), 'ascending')
  assert.equal(state.sortAriaValue(props.columns[1]), undefined, 'only the active header exposes aria-sort')
  assert.equal(state.sortActionLabel(props.columns[0]), 'Sort by Name descending')
  state.toggleSort(props.columns[0])
  assert.deepEqual(plain(state.currentSort.value), { key: 'name', direction: 'desc' })
  assert.equal(resets.length, 1, 'the sort action clears cursor history')
  assert.deepEqual(emitted.map(event => event.event), ['update:sort', 'update:page', 'change'])
  assert.deepEqual(plain(emitted[2].payload), {
    reason: 'sort',
    page: 1,
    pageSize: 25,
    query: '',
    filters: {},
    cursor: null,
    sort: { key: 'name', direction: 'desc' },
  })
  assert.ok(!emitted.some(event => event.event === 'update:selectedKeys'), 'sorting preserves selection')

  emitted.length = 0
  state.toggleSort(props.columns[1])
  assert.deepEqual(plain(state.currentSort.value), { key: 'createdAt', direction: 'desc' }, 'new columns use their configured initial direction')
  state.toggleSort(props.columns[1])
  assert.deepEqual(plain(state.currentSort.value), { key: 'createdAt', direction: 'asc' }, 'the active column toggles in two directions')

  const controlledProps = reactive({
    sort: { key: 'createdAt', direction: 'desc' },
    defaultSort: undefined,
    page: 4,
    columns: props.columns,
  })
  const controlledEvents = []
  const controlled = runInNewContext(code + '\n({ currentSort, toggleSort })', {
    props: controlledProps,
    ref,
    computed,
    page: ref(4),
    currentPageSize: ref(10),
    currentQuery: ref('q'),
    snapshotFilters: () => ({ state: 'ready' }),
    resetCursorHistory: () => {},
    emit: (event, payload) => controlledEvents.push({ event, payload }),
  })
  controlled.toggleSort(controlledProps.columns[1])
  assert.deepEqual(controlledEvents.map(event => event.event), ['update:sort', 'update:page', 'change'])
  assert.deepEqual(plain(controlledEvents[2].payload), {
    reason: 'sort',
    page: 1,
    pageSize: 10,
    query: 'q',
    filters: { state: 'ready' },
    cursor: null,
    sort: { key: 'createdAt', direction: 'asc' },
  })
  assert.deepEqual(plain(controlled.currentSort.value), { key: 'createdAt', direction: 'desc' }, 'controlled sort waits for its prop update')
  controlledProps.sort = { key: 'createdAt', direction: 'asc' }
  assert.deepEqual(plain(controlled.currentSort.value), { key: 'createdAt', direction: 'asc' })

  const legacyProps = reactive({ sort: undefined, defaultSort: undefined, columns: [{ key: 'name', sortable: false }] })
  const legacyEvents = []
  const legacy = runInNewContext(code + '\n({ emitChange, sortingConfigured })', {
    props: legacyProps,
    ref,
    computed,
    page: ref(2),
    currentPageSize: ref(10),
    currentQuery: ref(''),
    snapshotFilters: () => ({}),
    resetCursorHistory: () => {},
    emit: (event, payload) => legacyEvents.push({ event, payload }),
  })
  assert.equal(legacy.sortingConfigured.value, false)
  legacy.emitChange('page', 2, 'cursor')
  assert.ok(!Object.hasOwn(legacyEvents[0].payload, 'sort'), 'legacy change payloads keep their prior shape')
})

test('cursor pager carries sort snapshots and resets cursor history when sorting changes', () => {
  const initial = table.createCursorPager({
    page: 3,
    pageSize: 10,
    pageCursors: [null, 'cursor-2', 'cursor-3'],
    cursor: 'cursor-3',
    sort: { key: 'name', direction: 'asc' },
  })
  assert.deepEqual(table.cursorPagerRequest(initial).sort, { key: 'name', direction: 'asc' })

  const changed = table.updateCursorPager(initial, { sort: { key: 'updatedAt', direction: 'desc' } })
  assert.equal(changed.page, 1)
  assert.equal(changed.cursor, null)
  assert.deepEqual(changed.pageCursors, [null])
  assert.deepEqual(changed.sort, { key: 'updatedAt', direction: 'desc' })
  const filtered = table.updateCursorPager(changed, { query: 'latest' })
  assert.deepEqual(filtered.sort, changed.sort, 'query changes preserve the current sort')
  assert.equal(filtered.cursor, null)
  const cleared = table.updateCursorPager(filtered, { sort: null })
  assert.equal(cleared.sort, null)
  assert.equal(cleared.page, 1)

  const legacy = table.createCursorPager()
  assert.ok(!Object.hasOwn(legacy, 'sort'))
  assert.ok(!Object.hasOwn(table.cursorPagerRequest(legacy), 'sort'))
})

test('sortable headers expose direction, next-action names, and usable shared targets', () => {
  assert.match(component, /:aria-sort="sortAriaValue\(col\)"/)
  assert.match(component, /:aria-label="sortActionLabel\(col\)"/)
  assert.match(component, /@click="toggleSort\(col\)"/)
  assert.match(component, /<th v-for="col in visibleColumns"[^>]*scope="col"/)
  assert.match(component, /<ArrowUp[^>]*aria-hidden="true"/)
  assert.match(component, /<ArrowDown[^>]*aria-hidden="true"/)
  assert.match(component, /<ArrowUpDown[^>]*aria-hidden="true"/)
  assert.match(styles, /\.k-table__sort-button[\s\S]*?min-height: 24px;/)
  assert.match(styles, /@media \(pointer: coarse\), \(any-pointer: coarse\)\s*\{\s*\.k-table__sort-button \{ min-height: 44px; min-width: 44px; \}/)
})
