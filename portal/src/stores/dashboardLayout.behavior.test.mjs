import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import ts from 'typescript'
import * as Vue from 'vue'

const code = ts.transpileModule(readFileSync(new URL('./dashboardLayout.ts', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText

function fixture(saved) {
  const storage = new Map(saved ? [['railgrid-dashboard-layout:ws', JSON.stringify(saved)]] : [])
  const requests = []
  const timers = new Map()
  let timer = 0
  const previous = globalThis.localStorage
  globalThis.localStorage = { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value) }
  const modules = {
    vue: Vue,
    pinia: { defineStore: (_, setup) => setup },
    '@/auth/session': { authFetch: (url, options) => new Promise(resolve => requests.push({ url, options, resolve })) },
  }
  const exports = {}
  new Function('require', 'exports', 'setTimeout', 'clearTimeout', code)(id => modules[id], exports,
    callback => { timers.set(++timer, callback); return timer }, id => timers.delete(id))
  const store = exports.useDashboardLayoutStore()
  return { store, requests, catalogTileID: exports.ADD_PROVIDER_TILE_ID,
    saved: () => JSON.parse(storage.get('railgrid-dashboard-layout:ws')),
    flushWrites() { for (const callback of timers.values()) callback(); timers.clear() },
    cleanup() { if (previous === undefined) delete globalThis.localStorage; else globalThis.localStorage = previous },
  }
}
const tile = (i, w = 3, y = 0) => ({ i, x: 0, y, w, h: 2 })
const dto = (w = 1) => ({ gridColumns: 0, tiles: [{ name: 'agents', x: 0, y: 0, w, h: 2 }], hidden: [] })
const answer = async (request, body) => { request.resolve({ ok: true, json: async () => body }); await Promise.resolve(); await Vue.nextTick() }

test('catalog tile follows the last provider across gaps, compaction, and grid widths', () => {
  const f = fixture({ cols: 0, hidden: [], tiles: [
    tile('agents', 1), { ...tile('code', 1), x: 2 }, tile('edges', 1, 2),
  ] })
  try {
    const { store, catalogTileID } = f
    const names = ['agents', 'code', 'edges']
    const expectSlot = (x, y) => assert.deepEqual(store.catalogTile.value, {
      i: catalogTileID, x, y, w: 1, h: 2,
    })
    store.sync('org', 'ws', names, 3)
    expectSlot(1, 2)
    assert.deepEqual(store.layout.value.map(t => t.i), names)

    const [agents, code, edges] = store.layout.value
    agents.h = 4
    code.h = 6
    edges.w = 2
    edges.y = 4
    expectSlot(0, 6)
    code.h = 4
    expectSlot(2, 4)
    edges.x = 1
    expectSlot(0, 6)
    edges.w = 1
    edges.y = 0
    expectSlot(0, 4) // The rightmost tile in the row reaches the grid edge.

    store.sync('org', 'ws', names, 1)
    // Simulate the grid settling the collision after clamping to one column.
    store.layout.value[1].y = 2
    store.layout.value[2].y = 4
    expectSlot(0, 6)

    for (const name of names) store.hide(name)
    expectSlot(0, 0)
  } finally { f.cleanup() }
})

test('saved columns and responsive layout use the same bounded grid width', () => {
  const f = fixture({ cols: 6, tiles: [tile('agents', 6)], hidden: [] })
  try {
    f.store.sync('org', 'ws', ['agents'], 3)
    assert.equal(f.store.columns.value, 3)
    assert.equal(f.store.layout.value[0].w, 3)
    f.store.sync('org', 'ws', ['agents'], 1)
    assert.equal(f.store.columns.value, 1)
    assert.equal(f.store.layout.value[0].w, 1)
    f.store.sync('org', 'ws', ['agents'], 6)
    assert.equal(f.store.columns.value, 6)
    assert.equal(f.store.layout.value[0].w, 6)
    assert.equal(f.saved().tiles[0].w, 6)
  } finally { f.cleanup() }
})

test('catalog tile visibility is saved per workspace without saving its position', async () => {
  const f = fixture()
  try {
    const { store, catalogTileID } = f
    const hasCatalogTile = () => store.catalogTile.value !== null
    store.sync('org', 'ws', ['agents'], 3)
    assert.ok(hasCatalogTile())
    store.hide(catalogTileID)
    assert.equal(hasCatalogTile(), false)
    assert.deepEqual(store.addable.value, [catalogTileID])
    assert.deepEqual(f.saved().hidden, [catalogTileID])
    f.flushWrites()
    assert.deepEqual(JSON.parse(f.requests.at(-1).options.body).hidden, [catalogTileID])
    await answer(f.requests.at(-1), {})

    store.sync('org', 'other', ['agents'], 3)
    assert.ok(hasCatalogTile())
    store.sync('org', 'ws', ['agents'], 3)
    assert.equal(hasCatalogTile(), false)
    await answer(f.requests.at(-1), { tiles: [], hidden: [] })
    assert.equal(hasCatalogTile(), false, 'an empty hub response keeps cached visibility preferences')

    store.unhide(catalogTileID)
    assert.ok(hasCatalogTile())
    assert.deepEqual(store.addable.value, [])
    store.persist()
    assert.deepEqual(f.saved().tiles.map(t => t.i), ['agents'])
  } finally { f.cleanup() }
})

test('hiding a tile on a phone does not save clamped widths over desktop geometry', () => {
  const f = fixture({ cols: 0, tiles: [tile('agents'), tile('code', 2, 2)], hidden: [] })
  try {
    f.store.sync('org', 'ws', ['agents', 'code'], 1)
    f.store.hide('code')
    assert.equal(f.saved().tiles.find(t => t.i === 'agents').w, 3)
    assert.equal(f.saved().tiles.find(t => t.i === 'code').w, 2)
    f.store.sync('org', 'ws', ['agents', 'code'], 3)
    f.store.unhide('code')
    assert.equal(f.store.layout.value.find(t => t.i === 'agents').w, 3)
    assert.equal(f.store.layout.value.find(t => t.i === 'code').w, 2)
  } finally { f.cleanup() }
})

test('an older preference response cannot shrink a newer restored layout', async () => {
  const f = fixture()
  try {
    f.store.sync('org', 'ws', ['agents'], 1)
    f.store.sync('org', 'ws', ['agents'], 3)
    await answer(f.requests[1], dto(3))
    await answer(f.requests[0], dto(1))
    assert.equal(f.store.layout.value[0].w, 3)
  } finally { f.cleanup() }
})

test('a preference read started before a user resize cannot undo that resize', async () => {
  const f = fixture()
  try {
    f.store.sync('org', 'ws', ['agents'], 3)
    f.store.layout.value[0].w = 2
    f.store.persist()
    await answer(f.requests[0], dto(1))
    assert.equal(f.store.layout.value[0].w, 2)
    assert.equal(f.saved().tiles[0].w, 2)
  } finally { f.cleanup() }
})

test('leaving and returning to the same workspace fences its previous read', async () => {
  const f = fixture()
  try {
    f.store.sync('org', 'ws', ['agents'], 3)
    f.store.sync('org', 'other', ['agents'], 3)
    f.store.sync('org', 'ws', ['agents'], 3)
    await answer(f.requests[2], dto(3))
    await answer(f.requests[0], dto(1))
    assert.equal(f.store.layout.value[0].w, 3)
  } finally { f.cleanup() }
})

test('responsive sync cannot reload old preferences while a local resize is saving', async () => {
  const f = fixture()
  try {
    f.store.sync('org', 'ws', ['agents'], 3)
    await answer(f.requests[0], dto(1))
    f.store.layout.value[0].w = 2
    f.store.persist()
    f.store.sync('org', 'ws', ['agents'], 1)
    assert.equal(f.requests.length, 1, 'do not read the older server layout during the debounce')
    f.flushWrites()
    assert.equal(f.requests[1].options.method, 'PUT')
    f.store.sync('org', 'ws', ['agents'], 3)
    assert.equal(f.store.layout.value[0].w, 2)
    assert.equal(f.requests.length, 2, 'wait for the pending PUT before reading preferences')
    await answer(f.requests[1], {})
    f.store.sync('org', 'ws', ['agents'], 3)
    await answer(f.requests[2], dto(2))
    assert.equal(f.store.layout.value[0].w, 2)
  } finally { f.cleanup() }
})
