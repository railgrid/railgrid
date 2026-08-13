import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'

const source = await readFile(new URL('./CodeExplorer.vue', import.meta.url), 'utf8')
const vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
const { codeExplorerTreeState } = await vite.ssrLoadModule('/src/CodeExplorer.vue')
test.after(async () => vite.close())

test('distinguishes initial tree hydration from refresh and does not show a false empty state', () => {
  assert.match(source, /class="shimmer h-4 rounded bg-surface-overlay"/)
  assert.match(source, /Loading workspace files…/)
  assert.match(source, /treeState === 'refreshing'/)
  assert.match(source, /treeState === 'refresh-error'/)
  assert.match(source, /Showing the last loaded tree\./)
  assert.match(source, /treeState === 'empty'/)
  assert.match(source, /role="alert"/)
})

test('guards tree and file responses with request serials and current project checks', () => {
  assert.match(source, /let treeRequestSerial = 0/)
  assert.match(source, /let fileRequestSerial = 0/)
  assert.match(source, /serial !== treeRequestSerial \|\| !isCurrentProject\(projectName, requestContext\)/)
  assert.match(source, /serial !== fileRequestSerial \|\| !isCurrentProject\(projectName, requestContext\)/)
  assert.match(source, /selectedPath\.value !== path/)
  assert.match(source, /treeRequestSerial\+\+/)
  assert.match(source, /fileRequestSerial\+\+/)
})

test('keeps a cached tree visible and reports refresh failure separately from initial failure', () => {
  assert.equal(codeExplorerTreeState(true, false, null), 'initial-loading')
  assert.equal(codeExplorerTreeState(false, false, 'Could not load files.'), 'initial-error')
  assert.equal(codeExplorerTreeState(true, true, null), 'refreshing')
  assert.equal(codeExplorerTreeState(false, true, 'Could not refresh files.'), 'refresh-error')
  assert.equal(codeExplorerTreeState(false, true, null), 'ready')
})
