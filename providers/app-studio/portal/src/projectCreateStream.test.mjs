import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ appType: 'custom', optimizeDeps: { noDiscovery: true }, server: { middlewareMode: true, hmr: false } })
const { api } = await vite.ssrLoadModule('/src/api.ts')
test.after(() => vite.close())
const encoder = new TextEncoder()
let serial = 0
function context(streamFactory, calls) {
  const n = ++serial
  return { tenant: `cluster-${n}`, orgUUID: `org-${n}`, workspaceUUID: `workspace-${n}`, fetch: async (url, init) => {
    calls.push({ url: String(url), method: init?.method ?? 'GET' })
    if ((init?.method ?? 'GET') === 'GET') return new Response('{}', { status: 200 })
    return new Response(streamFactory(), { status: 200 })
  } }
}
function stream(first, failAfter = false) {
  let count = 0
  return new ReadableStream({ pull(controller) {
    if (count++ === 0) controller.enqueue(encoder.encode(first))
    else if (failAfter) controller.error(new Error('connection lost'))
    else controller.close()
  } })
}
test('created frame is terminal even if the connection subsequently fails', async () => {
  const calls = []
  const ctx = context(() => stream('event: created\ndata: {"name":"project-a","uid":"uid-a"}\n\n', true), calls)
  const project = await api.createProjectStream(ctx, { prompt: 'idea' }, () => {})
  assert.equal(project.name, 'project-a')
  assert.equal(calls.filter(call => call.method === 'POST').length, 1)
})
test('incomplete or invalid created frames and errors do not establish success', async () => {
  for (const data of ['event: created\ndata: {"name":"p","uid":"u"}', 'event: created\ndata: {"name":"p"}\n\n', 'event: error\ndata: {"message":"failed"}\n\n']) {
    const ctx = context(() => stream(data), [])
    await assert.rejects(api.createProjectStream(ctx, {}, () => {}))
  }
})
test('context invalidated during Studio setup cannot dispatch creation', async () => {
  let release
  const ready = new Promise(resolve => { release = resolve })
  const calls = []
  const ctx = context(() => stream(''), calls)
  const fetch = ctx.fetch
  ctx.fetch = async (url, init) => { if ((init?.method ?? 'GET') === 'GET') await ready; return fetch(url, init) }
  let current = true
  const result = api.createProjectStream(ctx, {}, () => {}, undefined, () => current)
  current = false
  release()
  await assert.rejects(result, /context changed/)
  assert.equal(calls.filter(call => call.method === 'POST').length, 0)
})
test('created frame wins over later frames in the same chunk', async () => {
  const ctx = context(() => stream('event: created\ndata: {"name":"project-a","uid":"uid-a"}\n\nevent: error\ndata: {"message":"late"}\n\n'), [])
  assert.equal((await api.createProjectStream(ctx, {}, () => {})).uid, 'uid-a')
})
test('cancelling stalled setup, fetch and reads ignores late results and allows a fresh setup', async () => {
  for (const stage of ['setup', 'fetch', 'read']) {
    let release
    let postSignal
    const calls = []
    const ctx = context(() => new ReadableStream({ start(controller) { release = () => controller.enqueue(encoder.encode('event: status\ndata: {"message":"late"}\n\n')) } }), calls)
    const fetch = ctx.fetch
    let block = true
    ctx.fetch = async (url, init) => {
      if ((init?.method ?? 'GET') === 'GET' && stage === 'setup' && block) {
        block = false
        await new Promise(resolve => { release = resolve })
      }
      if (init?.method === 'POST') {
        postSignal = init.signal
        if (stage === 'fetch') return new Promise(resolve => { release = () => resolve(new Response(stream(''))) })
      }
      return fetch(url, init)
    }
    const statuses = []
    const aborter = new AbortController()
    const result = api.createProjectStream(ctx, {}, status => statuses.push(status), aborter.signal)
    await new Promise(resolve => setTimeout(resolve, 0))
    aborter.abort()
    await assert.rejects(result, { name: 'AbortError' })
    if (postSignal) assert.equal(postSignal.aborted, true)
    release()
    await new Promise(resolve => setTimeout(resolve, 0))
    assert.deepEqual(statuses, [])
    if (stage === 'setup') {
      assert.equal(calls.filter(call => call.method === 'POST').length, 0)
      ctx.fetch = fetch
      await api.createProjectStream(ctx, {}, () => {}, AbortSignal.timeout(50)).catch(() => {})
      assert.equal(calls.filter(call => call.method === 'GET').length, 2)
    }
  }
})
test('a quiet stream stays pending until it completes', async () => {
  let finish
  const ctx = context(() => new ReadableStream({ start(controller) { finish = () => { controller.enqueue(encoder.encode('event: created\ndata: {"name":"p","uid":"u"}\n\n')); controller.close() } } }), [])
  let settled = false
  const result = api.createProjectStream(ctx, {}, () => {}).finally(() => { settled = true })
  await new Promise(resolve => setTimeout(resolve, 30))
  assert.equal(settled, false)
  finish()
  assert.equal((await result).name, 'p')
})
test('import cannot dispatch when its context changes during Studio setup', async () => {
  let release
  const ready = new Promise(resolve => { release = resolve })
  const calls = []
  const ctx = context(() => stream(''), calls)
  const fetch = ctx.fetch
  ctx.fetch = async (url, init) => { if ((init?.method ?? 'GET') === 'GET') await ready; return fetch(url, init) }
  let current = true
  const result = api.createProject(ctx, { existingRepositoryRef: 'repo' }, () => current)
  current = false
  release()
  await assert.rejects(result, /context changed/)
  assert.equal(calls.filter(call => call.method === 'POST').length, 0)
})

test('non-success responses expose bounded Status details without assuming creation was rejected', async () => {
  const ctx = context(() => stream(''), [])
  const fetch = ctx.fetch
  ctx.fetch = async (url, init) => init?.method === 'POST'
    ? new Response(JSON.stringify({ message: 'Access denied: select a workspace', reason: 'Forbidden' }), { status: 403, headers: { 'Content-Type': 'application/json' } })
    : fetch(url, init)
  await assert.rejects(api.createProjectStream(ctx, {}, () => {}), error => {
    assert.equal(error.name, 'ProjectAPIRequestError')
    assert.equal(error.status, 403)
    assert.equal(error.reason, 'Forbidden')
    assert.equal(error.message, 'Access denied: select a workspace')
    return true
  })
  ctx.fetch = async (url, init) => init?.method === 'POST'
    ? new Response('x'.repeat(30000), { status: 502, headers: { 'Content-Type': 'text/plain' } })
    : fetch(url, init)
  await assert.rejects(api.createProjectStream(ctx, {}, () => {}), error => error.message.length === 1000)
})

test('cancelling a stalled error response stops waiting', async () => {
  const ctx = context(() => stream(''), [])
  const fetch = ctx.fetch
  ctx.fetch = async (url, init) => init?.method === 'POST'
    ? new Response(new ReadableStream({ pull() {} }), { status: 503 })
    : fetch(url, init)
  const aborter = new AbortController()
  const result = api.createProjectStream(ctx, {}, () => {}, aborter.signal)
  await new Promise(resolve => setTimeout(resolve, 0))
  aborter.abort()
  await assert.rejects(result, { name: 'AbortError' })
})

test('stalled and broken error bodies fall back to HTTP status', async () => {
  for (const body of [
    new ReadableStream({ pull() {} }),
    new ReadableStream({ pull(controller) { controller.error(new Error('broken body')) } }),
  ]) {
    const ctx = context(() => stream(''), [])
    const fetch = ctx.fetch
    ctx.fetch = async (url, init) => init?.method === 'POST'
      ? new Response(body, { status: 503, statusText: 'Unavailable' })
      : fetch(url, init)
    await assert.rejects(api.createProjectStream(ctx, {}, () => {}), error => {
      assert.equal(error.status, 503)
      assert.match(error.message, /503 Unavailable/)
      return true
    })
  }
})

test('decodes a Status message split across UTF-8 chunks', async () => {
  const bytes = encoder.encode(JSON.stringify({ message: 'Accès refusé', reason: 'Forbidden' }))
  const split = bytes.indexOf(0xc3) + 1
  const body = new ReadableStream({ start(controller) {
    controller.enqueue(bytes.subarray(0, split))
    controller.enqueue(bytes.subarray(split))
    controller.close()
  } })
  const ctx = context(() => stream(''), [])
  const fetch = ctx.fetch
  ctx.fetch = async (url, init) => init?.method === 'POST'
    ? new Response(body, { status: 403 })
    : fetch(url, init)
  await assert.rejects(api.createProjectStream(ctx, {}, () => {}), { message: 'Accès refusé' })
})
