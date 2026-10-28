import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from '../../portal/node_modules/typescript/lib/typescript.js'
import { createRenderer, defineComponent, h } from '../../portal/node_modules/vue/index.mjs'

async function loadModule(name) {
  const source = readFileSync(new URL(name, import.meta.url), 'utf8')
  const output = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
  }).outputText.replace(/from ['"]vue['"]/g, `from '${new URL('../../portal/node_modules/vue/index.mjs', import.meta.url).href}'`)
  return import(`data:text/javascript;base64,${Buffer.from(output).toString('base64')}`)
}

const timestamp = await loadModule('./timestamp.ts')
const now = Date.parse('2026-10-28T12:00:00Z')

test('formats past and future relative time from the supplied clock', () => {
  assert.equal(timestamp.formatRelativeTimestamp('2026-10-24T12:00:00Z', { locale: 'en-US', nowMs: now }), '4 days ago')
  assert.equal(timestamp.formatRelativeTimestamp('2026-10-30T12:00:00Z', { locale: 'en-US', nowMs: now }), 'in 2 days')
  assert.equal(timestamp.formatRelativeTimestamp('2026-10-28T12:00:00Z', { locale: 'en-US', nowMs: now }), 'now')
  assert.equal(timestamp.formatRelativeTimestamp('2026-10-28T12:00:00Z', { locale: 'fr-FR', nowMs: now }), new Intl.RelativeTimeFormat('fr-FR', { numeric: 'auto' }).format(0, 'second'))
  assert.equal(timestamp.formatRelativeTimestamp('2026-10-28T10:00:00Z', { locale: 'fr-FR', nowMs: now }), new Intl.RelativeTimeFormat('fr-FR').format(-2, 'hour'))
})

test('exact timestamps include time, seconds, and timezone across DST ambiguity', () => {
  const first = timestamp.formatFullTimestamp('2026-11-01T06:30:07Z', { locale: 'en-US', timeZone: 'America/Chicago' })
  const second = timestamp.formatFullTimestamp('2026-11-01T07:30:07Z', { locale: 'en-US', timeZone: 'America/Chicago' })
  assert.match(first, /Nov.*1.*2026.*1:30:07.*CDT/)
  assert.match(second, /Nov.*1.*2026.*1:30:07.*CST/)
  assert.notEqual(first, second, 'the two occurrences of 1:30am have distinct exact labels')
  assert.equal(timestamp.timestampSortValue('2026-11-01T01:30:07-05:00'), Date.parse('2026-11-01T06:30:07Z'))
  assert.ok(timestamp.timestampSortValue('2026-11-01T01:30:07-05:00') < timestamp.timestampSortValue('2026-11-01T01:30:07-06:00'))
})

test('invalid and absent timestamps have no date or sort value; epoch zero remains valid', () => {
  for (const value of [undefined, null, '', '  ', 'not-a-date', '2026-02-31T12:00:00Z', '2026-02-29T12:00:00Z', '2026-10-28T24:00:00Z', '2026-10-28T12:00:00', '2026-10-28']) {
    assert.equal(timestamp.isValidTimestamp(value), false)
    assert.equal(timestamp.timestampSortValue(value), null)
    assert.equal(timestamp.formatFullTimestamp(value), '')
    assert.equal(timestamp.formatRelativeTimestamp(value), '')
  }
  assert.equal(timestamp.timestampSortValue('1970-01-01T00:00:00Z'), 0)
  assert.equal(timestamp.isValidTimestamp('2024-02-29T12:00:00Z'), true)
})

test('mounted timestamp consumers share a clock and release it on final unmount', async t => {
  const callbacks = new Map()
  let nextHandle = 1
  let nowMs = now
  t.mock.method(globalThis, 'setInterval', callback => {
    const handle = nextHandle++
    callbacks.set(handle, callback)
    return handle
  })
  t.mock.method(globalThis, 'clearInterval', handle => callbacks.delete(handle))
  t.mock.method(Date, 'now', () => nowMs)
  const { useReactiveTimestampNow } = await loadModule('./timestampClock.ts')
  assert.equal(callbacks.size, 0, 'importing the clock does not start an SSR/browser timer')
  const values = []
  const Consumer = defineComponent({ setup() { values.push(useReactiveTimestampNow()); return () => h('span') } })
  const renderer = createRenderer({
    createElement: () => ({}), createText: () => ({}), createComment: () => ({}),
    insert() {}, remove() {}, setText() {}, setElementText() {}, patchProp() {},
    parentNode: () => null, nextSibling: () => null,
  })
  const first = renderer.createApp(Consumer)
  const second = renderer.createApp(Consumer)
  first.mount({})
  second.mount({})
  assert.equal(callbacks.size, 1)
  assert.equal(values[0], values[1], 'all cells observe the same clock')
  nowMs += 60_000
  for (const callback of callbacks.values()) callback()
  assert.equal(values[0].value, nowMs)
  first.unmount()
  assert.equal(callbacks.size, 1, 'remaining table consumers retain the clock')
  second.unmount()
  assert.equal(callbacks.size, 0, 'leaving the table releases the clock')
  const third = renderer.createApp(Consumer)
  nowMs += 60_000
  third.mount({})
  assert.equal(values[2].value, nowMs, 'remount starts with the current time')
  assert.equal(callbacks.size, 1)
  third.unmount()
  assert.equal(callbacks.size, 0)
})
