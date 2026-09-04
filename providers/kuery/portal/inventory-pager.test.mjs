import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('./src/inventory-pager.ts', import.meta.url), 'utf8')
const output = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } }).outputText
const pager = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(output)}`)

const change = (reason, overrides = {}) => ({ reason, page: 1, pageSize: 50, query: '', filters: {}, cursor: null, ...overrides })

test('first, next, and previous pages preserve opaque cursor history', () => {
  let state = pager.createInventoryPager(50)
  let begun = pager.beginInventoryRequest(state); state = begun.state
  assert.deepEqual(begun.request, { id: 1, pageSize: 50, cursor: null, filters: { edge: undefined, kind: undefined, namespace: undefined, name: undefined } })
  const token = 'opaque/+== token.do-not-parse'
  state = pager.applyInventoryPage(state, 1, { nextCursor: token, total: 120, hasNext: true })
  state = pager.changeInventoryPager(state, change('page', { page: 2, cursor: token }))
  assert.equal(state.cursor, token)
  state = pager.applyInventoryPage(pager.beginInventoryRequest(state).state, 2, { nextCursor: 'page-3', total: 120, hasNext: true })
  state = pager.changeInventoryPager(state, change('page', { page: 1, cursor: null }))
  assert.equal(state.cursor, null)
  assert.equal(state.pageCursors[1], token)
})

test('query, filter, and page-size changes reset page and cursor state', () => {
  const seeded = { ...pager.createInventoryPager(50), page: 3, cursor: 'three', nextCursor: 'four', pageCursors: [null, 'two', 'three'] }
  for (const update of [
    change('query', { query: 'api', cursor: 'ignored' }),
    change('filter', { filters: { edge: 'edge-a', kind: 'Pod' }, cursor: 'ignored' }),
    change('page-size', { pageSize: 100, cursor: 'ignored' }),
  ]) {
    const state = pager.changeInventoryPager(seeded, update)
    assert.equal(state.page, 1); assert.equal(state.cursor, null); assert.deepEqual(state.pageCursors, [null])
  }
})

test('free-text kind and namespace filters stay exact, trim input, and reset pagination', () => {
  const seeded = { ...pager.createInventoryPager(50), page: 3, cursor: 'three', pageCursors: [null, 'two', 'three'] }
  const state = pager.changeInventoryPager(seeded, change('filter', {
    query: '  api  ',
    filters: { edge: 'edge-a', kind: '  CustomWidget  ', namespace: '  tenant-only  ' },
    cursor: 'ignored',
  }))

  assert.equal(state.page, 1)
  assert.equal(state.cursor, null)
  assert.deepEqual(state.pageCursors, [null])
  assert.deepEqual(state.filters, { edge: 'edge-a', kind: 'CustomWidget', namespace: 'tenant-only' })

  const begun = pager.beginInventoryRequest(state)
  assert.deepEqual(begun.request.filters, {
    edge: 'edge-a', kind: 'CustomWidget', namespace: 'tenant-only', name: 'api',
  })
})

test('count is authoritative for terminal pages and incomplete never invents navigation', () => {
  let state = { ...pager.createInventoryPager(50), page: 3, cursor: 'three', requestID: 7 }
  state = pager.applyInventoryPage(state, 7, { nextCursor: 'engine-token', total: 120, hasNext: true })
  assert.equal(state.pageInfo.hasNext, false)
  assert.equal(state.pageInfo.nextCursor, null)
  assert.equal(state.paginationGap, false)
})

test('count exposes a pagination gap only when more rows exist without a continuation', () => {
  let state = { ...pager.createInventoryPager(50), page: 2, cursor: 'two', requestID: 4 }
  state = pager.applyInventoryPage(state, 4, { nextCursor: null, total: 120, hasNext: false })
  assert.equal(state.pageInfo.hasNext, false)
  assert.equal(state.paginationGap, true)

  state = pager.applyInventoryPage(state, 4, { nextCursor: 'opaque-three', total: 120, hasNext: false })
  assert.equal(state.pageInfo.hasNext, true, 'authoritative count plus an opaque token permits navigation')
  assert.equal(state.paginationGap, false)
})

test('stale request identities cannot replace current page metadata', () => {
  let state = pager.createInventoryPager()
  state = pager.beginInventoryRequest(state).state
  state = pager.beginInventoryRequest(state).state
  const unchanged = pager.applyInventoryPage(state, 1, { nextCursor: 'stale', total: 999, hasNext: true })
  assert.equal(unchanged, state)
  assert.equal(pager.isCurrentInventoryRequest(state, 1), false)
  assert.equal(pager.isCurrentInventoryRequest(state, 2), true)
})
