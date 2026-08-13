import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
})
test.after(async () => vite?.close())

test('renders a compact thread workbench with active selection and creation affordance', async () => {
  const { default: ThreadsWorkbench } = await vite.ssrLoadModule('/src/ThreadsWorkbench.vue')
  const html = await renderToString(createSSRApp(ThreadsWorkbench, {
    threads: [
      { id: 'thread-1', title: 'Plan the release', status: 'idle', createdAt: '2026-08-06T00:00:00Z', updatedAt: '2026-08-06T00:00:00Z' },
      { id: 'thread-2', status: 'active', createdAt: '2026-08-06T00:00:00Z', updatedAt: '2026-08-06T00:00:00Z' },
    ],
    activeThreadID: 'thread-1',
  }))
  assert.match(html, />Threads</)
  assert.match(html, /Plan the release/)
  assert.match(html, /New thread/)
  assert.match(html, /aria-current="page"/)
})

test('keeps rename/delete confirmation and orchestration in the shared component contract', async () => {
  const source = await readFile(new URL('./ThreadsWorkbench.vue', import.meta.url), 'utf8')
  assert.match(source, /confirmDialog\(/)
  assert.match(source, /emit\('rename'/)
  assert.match(source, /emit\('delete'/)
  assert.match(source, /@submit\.prevent="commitRename"/)
  assert.match(source, /title="Delete thread"/)
  assert.match(source, /:disabled="disabled \|\| busy/)
  assert.match(source, /defineExpose\(\{ focusActiveThread \}\)/)
  assert.match(source, /button\[aria-current="page"\].*\.focus\(\)/)
})

test('reserves the thread list while hydrating and marks only the selected row busy', async () => {
  const { default: ThreadsWorkbench } = await vite.ssrLoadModule('/src/ThreadsWorkbench.vue')
  const loadingHTML = await renderToString(createSSRApp(ThreadsWorkbench, {
    threads: [],
    activeThreadID: '',
    loading: true,
  }))
  assert.match(loadingHTML, /Loading threads/)
  assert.match(loadingHTML, /shimmer/)
  assert.doesNotMatch(loadingHTML, /No threads yet/)

  const selectingHTML = await renderToString(createSSRApp(ThreadsWorkbench, {
    threads: [
      { id: 'thread-1', title: 'Selected later', status: 'idle', createdAt: '2026-08-06T00:00:00Z', updatedAt: '2026-08-06T00:00:00Z' },
      { id: 'thread-2', title: 'Current', status: 'idle', createdAt: '2026-08-06T00:00:00Z', updatedAt: '2026-08-06T00:00:00Z' },
    ],
    activeThreadID: 'thread-2',
    selectingThreadID: 'thread-1',
  }))
  assert.match(selectingHTML, /aria-busy="true"/)
  assert.match(selectingHTML, /Loading thread/)
  assert.match(selectingHTML, /Current/)
  assert.match(selectingHTML, /aria-current="page"/)
  assert.doesNotMatch(selectingHTML, /aria-current="page"[^>]*disabled/)
})
