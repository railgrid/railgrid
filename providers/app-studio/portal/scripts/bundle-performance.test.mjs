import assert from 'node:assert/strict'
import test from 'node:test'
import { readdir, readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
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
  for (const compiler of ['core', 'dom', 'sfc', 'ssr', 'vapor', 'vue2', 'future-compiler']) {
    for (const separator of ['/', '\\']) {
      const id = `/app/node_modules/@vue/compiler-${compiler}/dist/compiler.js`.replaceAll('/', separator)
      assert.throws(() => bundlePerformance().generateBundle.call(context, {}, {
        page: chunk('/app/src/App.vue'),
        shared: chunk(id),
      }), /must not ship Vue's template compiler/)
    }
  }
})

// Scan shipped portal sources and their canonical shared-kit inputs. Resolve
// from this file so the check works from both the repo and portal directories.
test('portal and shared kits do not use literal runtime template options', async () => {
  const roots = [
    '../src/',
    '../../../../provider-sdk/portalkit/',
    '../../../../provider-sdk/portalkit-vue/',
    '../../../../provider-sdk/agentkit/',
    '../../../../provider-sdk/agentkit-vue/',
  ]
  const offenders = []
  async function walk(directory) {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      if (entry.name === 'node_modules') continue
      const path = new URL(entry.name + (entry.isDirectory() ? '/' : ''), directory)
      if (entry.isDirectory()) await walk(path)
      else if (/\.(ts|js|mjs)$/.test(entry.name) && !/\.(test|spec)\./.test(entry.name)) {
        if (/\btemplate\s*:\s*[`'"]/.test(await readFile(path, 'utf8'))) {
          offenders.push(fileURLToPath(path))
        }
      }
    }
  }
  for (const root of roots) await walk(new URL(root, import.meta.url))
  assert.deepEqual(offenders, [], 'Use precompiled SFC templates or render functions; runtime-only Vue cannot compile template strings')
})
