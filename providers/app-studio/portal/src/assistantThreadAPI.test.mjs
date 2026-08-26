import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/faros-vite-assistant-thread-api',
  server: { middlewareMode: true, hmr: false },
})
const { api } = await vite.ssrLoadModule('/src/api.ts')
test.after(async () => vite.close())

const context = {
  token: 'token-1',
  basePath: '/ui/providers/app-studio',
}

function installRequestMocks(pages, calls) {
  const previousFetch = globalThis.fetch
  const previousStorage = globalThis.localStorage
  const storage = new Map([['faros:portal:tenant', JSON.stringify({ orgUUID: 'org-1', workspaceUUID: 'workspace-1' })]])
  globalThis.localStorage = {
    getItem(key) { return storage.get(key) ?? null },
  }
  globalThis.fetch = async (path) => {
    const url = new URL(String(path), 'https://app-studio.test')
    const cursor = url.searchParams.get('cursor') ?? ''
    calls.push(url)
    const page = pages(cursor)
    return {
      ok: true,
      async text() { return JSON.stringify(page) },
    }
  }
  return () => {
    globalThis.fetch = previousFetch
    if (previousStorage === undefined) delete globalThis.localStorage
    else globalThis.localStorage = previousStorage
  }
}

function thread(id) {
  return {
    id,
    title: id,
    status: 'idle',
    createdAt: '2026-08-26T00:00:00Z',
    updatedAt: '2026-08-26T00:00:00Z',
  }
}

test('follows every assistant-thread page so older pin/read state remains addressable', async () => {
  const calls = []
  const restore = installRequestMocks((cursor) => cursor
    ? { items: [thread('thread-51')], nextCursor: '' }
    : { items: Array.from({ length: 50 }, (_, index) => thread(`thread-${index + 1}`)), nextCursor: 'cursor-1' }, calls)
  try {
    const threads = await api.listAssistantThreads(context, 'demo')
    assert.equal(threads.length, 51)
    assert.equal(calls.length, 2)
    assert.equal(calls[0].searchParams.get('limit'), '500')
    assert.equal(calls[0].searchParams.get('includeArchived'), 'false')
    assert.equal(calls[1].searchParams.get('cursor'), 'cursor-1')
  } finally {
    restore()
  }
})

test('rejects repeated cursors and page chains beyond the bounded page limit', async () => {
  const repeatedCalls = []
  const restoreRepeated = installRequestMocks(() => ({ items: [thread('repeated')], nextCursor: 'same' }), repeatedCalls)
  try {
    await assert.rejects(
      () => api.listAssistantThreads(context, 'demo', true),
      /ProjectAssistantThreadPaginationError|cursor cycle detected/,
    )
    assert.equal(repeatedCalls.length, 2)
    assert.equal(repeatedCalls[0].searchParams.get('includeArchived'), 'true')
  } finally {
    restoreRepeated()
  }

  const boundedCalls = []
  let pageNumber = 0
  const restoreBounded = installRequestMocks(() => {
    pageNumber += 1
    return {
      items: [thread(`bounded-${pageNumber}`)],
      // The 100th page advertises an additional page, which must be rejected
      // instead of returning the first 100 pages as a normal partial result.
      nextCursor: pageNumber < 101 ? `next-${pageNumber}` : '',
    }
  }, boundedCalls)
  try {
    await assert.rejects(
      () => api.listAssistantThreads(context, 'demo'),
      /ProjectAssistantThreadPaginationError|page limit exceeded \(100\)/,
    )
    assert.equal(boundedCalls.length, 100)
  } finally {
    restoreBounded()
  }
})
