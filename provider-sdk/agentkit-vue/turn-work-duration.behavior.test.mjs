import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from '../../portal/node_modules/typescript/lib/typescript.js'
import { createRenderer, h, nextTick, shallowRef } from '../../portal/node_modules/vue/index.mjs'

const vueURL = new URL('../../portal/node_modules/vue/index.mjs', import.meta.url).href

function moduleURL(path, imports) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  let { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
  })
  for (const [specifier, url] of Object.entries(imports)) {
    outputText = outputText.replace(`from '${specifier}'`, `from '${url}'`)
  }
  return `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
}

test('preserves a run estimate across replacement messages and settles on measured work', async t => {
  let nowMs = 100_000
  let tick
  t.mock.method(Date, 'now', () => nowMs)
  t.mock.method(globalThis, 'setInterval', callback => {
    assert.equal(tick, undefined)
    tick = callback
    return 1
  })
  t.mock.method(globalThis, 'clearInterval', () => { tick = undefined })

  const clockURL = moduleURL('./clock.ts', { vue: vueURL })
  const { useTurnWorkDuration } = await import(moduleURL('./turn-work-duration.ts', { vue: vueURL, './clock': clockURL }))
  const renderer = createRenderer({
    createElement: () => ({}), insert() {}, remove() {}, patchProp() {},
    parentNode: () => null, nextSibling: () => null,
  })
  const turn = shallowRef({ id: 'run-1', status: 'running', measuredMs: 16_000 })
  let duration
  const app = renderer.createApp({
    setup() {
      duration = useTurnWorkDuration(() => turn.value)
      return () => h('span')
    },
  })
  app.mount({})
  t.after(() => app.unmount())

  const advance = (ms) => {
    nowMs += ms
    tick?.()
  }
  const update = async (status, measuredMs, id = 'run-1') => {
    turn.value = { id, status, measuredMs }
    await nextTick()
  }

  advance(2_000)
  assert.equal(duration.value, 18_000)
  await update('running') // The previous segment has gone and its replacement has no measurement yet.
  assert.equal(duration.value, 18_000)
  await update('running', 17_000)
  assert.equal(duration.value, 18_000)

  await update('waiting', 17_000)
  assert.equal(duration.value, undefined)
  assert.equal(tick, undefined)
  advance(30_000)
  await update('running', 17_000)
  advance(1_000)
  assert.equal(duration.value, 19_000)

  await update('stopping', 17_000)
  advance(2_000)
  assert.equal(duration.value, 19_000)
  await update('completed', 17_000)
  assert.equal(duration.value, 17_000)

  await update('running', 5_000)
  advance(1_000)
  await update('running', undefined, 'run-2')
  assert.equal(duration.value, undefined)
  advance(1_000)
  assert.equal(duration.value, 1_000)
})
