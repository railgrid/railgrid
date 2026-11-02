import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import vm from 'node:vm'

const bootstrap = await readFile(new URL('../../public/provider-sandbox-bootstrap.js', import.meta.url), 'utf8')

function createFakeChannel() {
  const child = { onmessage: null, start() {}, postMessage(message) { queueMicrotask(() => hostListener?.(message)) } }
  let hostListener
  const host = {
    start() {}, close() {},
    on(name, listener) { if (name === 'message') hostListener = listener },
    postMessage(message) { queueMicrotask(() => child.onmessage?.({ data: message })) },
  }
  return { child, host }
}

function createSandbox() {
  const listeners = new Map()
  const element = { railgridContext: null }
  const root = { addEventListener() {}, replaceChildren() {} }
  const document = {
    documentElement: { className: '' },
    head: { append(node) { if (node.onload) queueMicrotask(node.onload) } },
    createElement(tag) {
      if (tag === 'style' || tag === 'script') return { textContent: '', onload: null, onerror: null }
      return element
    },
    querySelector() { return element },
    getElementById() { return root },
  }
  const window = {
    addEventListener(name, listener) { listeners.set(name, listener) },
    removeEventListener(name) { listeners.delete(name) },
  }
  const parent = {}
  const URLImpl = class extends URL {
    static createObjectURL() { return 'blob:provider-bundle' }
    static revokeObjectURL() {}
  }
  const context = vm.createContext({
    window, parent, document, URL: URLImpl, Blob, TextEncoder, URLSearchParams, FormData, Headers, Request, Response,
    ReadableStream, DOMException, Promise, Map, Set, Object, Array, String, TypeError,
    customElements: { whenDefined: async () => undefined },
  })
  vm.runInContext(bootstrap, context)
  const { child, host } = createFakeChannel()
  listeners.get('message')({
    source: parent, origin: 'https://hub.example', ports: [child],
    data: { type: 'railgrid-provider-init', nonce: 'test', hostOrigin: 'https://hub.example', context: { providerName: 'planner', theme: 'light' }, apiGroups: [], bytes: new ArrayBuffer(0) },
  })
  return { element, port: host, ready: new Promise(resolve => {
    const poll = () => element.railgridContext ? resolve() : setTimeout(poll, 0)
    poll()
  }) }
}

test('sandbox fetch exposes headers and first streamed chunk before EOF', async () => {
  const { element, port, ready } = createSandbox()
  await ready
  const received = []
  let sendChunk
  port.start()
  port.on('message', message => {
    received.push(message)
    if (message.type === 'fetch') {
      port.postMessage({ type: 'fetch-response', id: message.id, status: 200, statusText: 'OK', headers: [] })
      sendChunk = () => port.postMessage({ type: 'fetch-chunk', id: message.id, seq: 0, bytes: new TextEncoder().encode('first').buffer })
    }
  })
  const response = await element.railgridContext.fetch('/clusters/cluster-a/apis/planner.railgrid.ai/v1/plans')
  assert.equal(response.status, 200)
  assert.ok(response.body)
  const reader = response.body.getReader()
  const pendingRead = reader.read()
  assert.equal(typeof sendChunk, 'function')
  sendChunk()
  await new Promise(resolve => setTimeout(resolve, 0))
  const firstChunk = await Promise.race([
    pendingRead,
    new Promise((_, reject) => setTimeout(() => reject(new Error(`first chunk waited for EOF: ${JSON.stringify(received)}`)), 500)),
  ])
  assert.equal(new TextDecoder().decode(firstChunk.value), 'first')
  // The producer has not sent fetch-end; stream delivery must already have occurred.
  port.close()
})

test('aborting a pending sandbox fetch rejects it with AbortError', async () => {
  const { element, port, ready } = createSandbox()
  await ready
  port.start()
  port.on('message', message => {
    if (message.type === 'fetch-cancel') port.postMessage({ type: 'fetch-end', id: message.id })
  })
  const controller = new AbortController()
  const request = element.railgridContext.fetch('/clusters/cluster-a/apis/planner.railgrid.ai/v1/plans', { signal: controller.signal })
  controller.abort()
  await assert.rejects(request, error => error.name === 'AbortError')
  port.close()
})
