import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const listeners = new Map()
const fakeWindow = {
  location: { origin: 'https://console.example.test' },
  addEventListener(type, listener) { listeners.set(type, listener) },
  removeEventListener(type) { listeners.delete(type) },
  setTimeout,
  clearTimeout,
}
globalThis.window = fakeWindow

class FakePort {
  onmessage = null
  close() {}
  start() {}
}
const channels = []
globalThis.MessageChannel = class {
  port1 = new FakePort()
  port2 = new FakePort()
  constructor() {
    channels.push(this)
  }
}

const source = await readFile(new URL('./previewConsole.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const { PreviewConsoleController, takePreviewConsoleUploadBatch } = await import(moduleURL)

test('App preview automatically shares console evidence without a capture control', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const apiSource = await readFile(new URL('./api.ts', import.meta.url), 'utf8')
  assert.match(appSource, /if \(projectName\) void previewConsoleController\.connect\(projectName\)/)
  assert.doesNotMatch(appSource, /Share console/)
  assert.doesNotMatch(appSource, /Console shared/)
  assert.doesNotMatch(apiSource, /userConsent/)
})

test('transfers the capability only to the exact iframe window and preview origin', async () => {
  const calls = []
  const frameWindow = {
    postMessage(message, origin, ports) {
      calls.push({ message, origin, ports })
    },
  }
  const states = []
  const controller = new PreviewConsoleController({
    api: {
      async createSession(_project, generation) {
        return {
          status: 'available',
          sessionID: 'session-1',
          generation,
          capability: 'signed',
          previewOrigin: 'https://preview.example.test',
          portalOrigin: 'https://console.example.test',
          expiresAt: new Date(Date.now() + 60_000).toISOString(),
        }
      },
      async uploadEvents() {},
      async deleteSession() {},
    },
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState: (state) => states.push(state),
  })

  await controller.connect('project-a')
  assert.equal(calls[0].message.type, 'faros.preview-console.probe')
  const ready = { type: 'faros.preview-console.ready', version: 1, documentID: '826e6fa5-c38b-4bdb-8f8f-098198b74f65' }
  listeners.get('message')({ source: {}, origin: 'https://preview.example.test', data: ready })
  listeners.get('message')({ source: frameWindow, origin: 'https://attacker.example.test', data: ready })
  assert.equal(calls.length, 1)

  listeners.get('message')({ source: frameWindow, origin: 'https://preview.example.test', data: ready })
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(calls.length, 2)
  assert.equal(calls[1].origin, 'https://preview.example.test')
  assert.equal(calls[1].message.capability, 'signed')
  assert.equal(calls[1].ports.length, 1)
  listeners.get('message')({ source: frameWindow, origin: 'https://preview.example.test', data: ready })
  assert.equal(calls.length, 2, 'a replayed ready message must not receive another capability')
  assert.deepEqual(states, ['connecting'])
  controller.destroy()
})

test('treats a missing feature endpoint as disabled rather than connected', async () => {
  const states = []
  const frameWindow = { postMessage() {} }
  const controller = new PreviewConsoleController({
    api: {
      async createSession() {
        throw Object.assign(new Error('not found'), { status: 404 })
      },
      async uploadEvents() {},
      async deleteSession() {},
    },
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState: (state) => states.push(state),
  })
  await controller.connect('project-a')
  listeners.get('message')({
    source: frameWindow,
    origin: 'https://preview.example.test',
    data: { type: 'faros.preview-console.ready', version: 1, documentID: '826e6fa5-c38b-4bdb-8f8f-098198b74f65' },
  })
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(states, ['connecting', 'disabled'])
  controller.destroy()
})

test('a stale connect cannot replace the newest project after session deletion', async () => {
  const frameWindow = { postMessage() {} }
  const createdProjects = []
  let resolveDelete
  const controller = new PreviewConsoleController({
    api: {
      async createSession(project, generation) {
        createdProjects.push(project)
        return {
          status: 'available',
          sessionID: `session-${project}`,
          generation,
          capability: 'signed',
          previewOrigin: 'https://preview.example.test',
          portalOrigin: 'https://console.example.test',
          expiresAt: new Date(Date.now() + 60_000).toISOString(),
        }
      },
      async uploadEvents() {},
      async deleteSession() {
        await new Promise((resolve) => { resolveDelete = resolve })
      },
    },
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState() {},
  })

  await controller.connect('initial')
  listeners.get('message')({
    source: frameWindow,
    origin: 'https://preview.example.test',
    data: { type: 'faros.preview-console.ready', version: 1, documentID: '826e6fa5-c38b-4bdb-8f8f-098198b74f65' },
  })
  await new Promise((resolve) => setImmediate(resolve))

  const staleConnect = controller.connect('stale')
  const latestConnect = controller.connect('latest')
  await latestConnect
  resolveDelete()
  await staleConnect
  listeners.get('message')({
    source: frameWindow,
    origin: 'https://preview.example.test',
    data: { type: 'faros.preview-console.ready', version: 1, documentID: '5ac4b288-a1fa-4c99-936c-07467cd3cadb' },
  })
  await new Promise((resolve) => setImmediate(resolve))

  assert.deepEqual(createdProjects, ['initial', 'latest'])
  controller.destroy()
})

test('a stale disconnect cannot clear a newer automatic connection', async () => {
  const frameWindow = { postMessage() {} }
  const createdProjects = []
  let resolveDelete
  const controller = new PreviewConsoleController({
    api: {
      async createSession(project, generation) {
        createdProjects.push(project)
        return {
          status: 'available',
          sessionID: `session-${project}`,
          generation,
          capability: 'signed',
          previewOrigin: 'https://preview.example.test',
          portalOrigin: 'https://console.example.test',
          expiresAt: new Date(Date.now() + 60_000).toISOString(),
        }
      },
      async uploadEvents() {},
      async deleteSession() {
        await new Promise((resolve) => { resolveDelete = resolve })
      },
    },
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState() {},
  })

  await controller.connect('initial')
  listeners.get('message')({
    source: frameWindow,
    origin: 'https://preview.example.test',
    data: { type: 'faros.preview-console.ready', version: 1, documentID: '826e6fa5-c38b-4bdb-8f8f-098198b74f65' },
  })
  await new Promise((resolve) => setImmediate(resolve))

  const staleDisconnect = controller.disconnect()
  const latestConnect = controller.connect('latest')
  await latestConnect
  resolveDelete()
  await staleDisconnect
  listeners.get('message')({
    source: frameWindow,
    origin: 'https://preview.example.test',
    data: { type: 'faros.preview-console.ready', version: 1, documentID: '5ac4b288-a1fa-4c99-936c-07467cd3cadb' },
  })
  await new Promise((resolve) => setImmediate(resolve))

  assert.deepEqual(createdProjects, ['initial', 'latest'])
  controller.destroy()
})

test('constructs upload batches by both event count and serialized byte size', () => {
  const generation = '826e6fa5-c38b-4bdb-8f8f-098198b74f65'
  const pending = Array.from({ length: 20 }, (_, index) => ({
    sequence: index + 1,
    documentID: generation,
    level: 'log',
    message: 'x'.repeat(1_800),
  }))
  const batch = takePreviewConsoleUploadBatch(pending, generation, 7, 5_000)
  assert.equal(batch.length, 2)
  assert.equal(pending.length, 18)
  const encoded = new TextEncoder().encode(JSON.stringify({
    generation,
    protocolVersion: 1,
    droppedCount: 7,
    events: batch,
  }))
  assert.ok(encoded.byteLength <= 5_000)
})

test('reports dropped events even when no event is uploadable', async () => {
  const generation = '826e6fa5-c38b-4bdb-8f8f-098198b74f65'
  const uploads = []
  const frameWindow = { postMessage() {} }
  const controller = new PreviewConsoleController({
    api: {
      async createSession() {
        return {
          status: 'available',
          sessionID: 'session-drops',
          generation,
          capability: 'signed',
          previewOrigin: 'https://preview.example.test',
          portalOrigin: 'https://console.example.test',
          expiresAt: new Date(Date.now() + 60_000).toISOString(),
        }
      },
      async uploadEvents(_project, _session, _generation, events, droppedCount) {
        uploads.push({ events, droppedCount })
      },
      async deleteSession() {},
    },
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState() {},
  })

  await controller.connect('project-a')
  listeners.get('message')({
    source: frameWindow,
    origin: 'https://preview.example.test',
    data: { type: 'faros.preview-console.ready', version: 1, documentID: generation },
  })
  await new Promise((resolve) => setImmediate(resolve))
  const channel = channels.at(-1)
  channel.port1.onmessage({
    data: {
      type: 'faros.preview-console.events',
      version: 1,
      sessionID: 'session-drops',
      generation,
      droppedCount: 0,
      events: [{
        sequence: 1,
        documentID: generation,
        level: 'log',
        message: 'x'.repeat(3_000),
      }],
    },
  })
  await new Promise((resolve) => setTimeout(resolve, 550))

  assert.deepEqual(uploads, [{ events: [], droppedCount: 1 }])
  controller.destroy()
})
