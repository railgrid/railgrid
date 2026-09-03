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
  assert.match(source, /class="app-studio-touch-target[^\"]*focus-visible:ring-2[^\"]*" @click="loadTree">Retry refresh<\/button>/)
})

test('adapts the explorer to one pane on mobile and exposes a complete keyboard tree', () => {
  assert.match(source, /flex-col md:flex-row/)
  assert.match(source, /mobileTreeOpen \? 'flex' : 'hidden md:flex'/)
  assert.match(source, /mobileTreeOpen \? 'hidden md:flex' : 'flex'/)
  assert.match(source, /aria-label="Back to workspace files"/)
  assert.match(source, /ref="mobileBackRef"[\s\S]*aria-label="Back to workspace files"/)
  assert.match(source, /role="tree"/)
  assert.match(source, /role="treeitem"/)
  assert.match(source, /:aria-level="row\.depth \+ 1"/)
  assert.match(source, /:aria-posinset="row\.posInSet"/)
  assert.match(source, /:aria-setsize="row\.setSize"/)
  assert.match(source, /:aria-expanded="row\.node\.dir/)
  assert.match(source, /:aria-selected="!row\.node\.dir/)
  assert.match(source, /event\.key === 'ArrowDown'/)
  assert.match(source, /event\.key === 'ArrowRight'/)
  assert.match(source, /event\.key === 'Home'/)
})
