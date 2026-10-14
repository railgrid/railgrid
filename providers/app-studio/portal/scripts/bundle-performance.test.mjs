import assert from 'node:assert/strict'
import test from 'node:test'
import { bundlePerformance } from './bundle-performance.mjs'

const context = { error(message) { throw new Error(message) }, info() {} }
const chunk = (id) => ({ type: 'chunk', modules: { [id]: { renderedLength: 100 } } })

test('allows precompiled application and Vue runtime modules', () => {
  bundlePerformance().generateBundle.call(context, {}, {
    page: chunk('/app/src/App.vue'),
    runtime: chunk('/app/node_modules/@vue/runtime-dom/dist/runtime-dom.esm-bundler.js'),
    css: { type: 'asset', source: 'body {}' },
  })
})

test('rejects compiler dependencies even in shared or lazy chunks', () => {
  for (const compiler of ['core', 'dom', 'sfc', 'ssr']) {
    for (const separator of ['/', '\\']) {
      const id = `/app/node_modules/@vue/compiler-${compiler}/dist/compiler.js`.replaceAll('/', separator)
      assert.throws(() => bundlePerformance().generateBundle.call(context, {}, {
        page: chunk('/app/src/App.vue'),
        shared: chunk(id),
      }), /must not ship Vue's template compiler/)
    }
  }
})
