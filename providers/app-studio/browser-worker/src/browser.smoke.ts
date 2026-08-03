import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import type { AddressInfo } from 'node:net'
import test from 'node:test'

import { PlaywrightInspector } from './inspect.js'

test('inspects a real page and classifies page errors', async (t) => {
  let socketUpgrades = 0
  const site = createServer((request, response) => {
    response.setHeader('content-type', 'text/html; charset=utf-8')
    if (request.url === '/broken') {
      response.end('<!doctype html><title>Broken</title><main><h1>Broken preview</h1><script>Promise.reject(new Error("render failed"))</script></main>')
      return
    }
    if (request.url === '/socket') {
      response.end('<!doctype html><title>Socket</title><main><h1>Socket preview</h1><script>new WebSocket(`ws://${location.host}/mutate`)</script></main>')
      return
    }
    response.end('<!doctype html><title>Canary</title><main><h1>Ready</h1><button>Save</button><script>console.error("advisory only")</script></main>')
  })
  site.on('upgrade', (_request, socket) => {
    socketUpgrades++
    socket.destroy()
  })
  await new Promise<void>((resolve) => site.listen(0, '127.0.0.1', resolve))
  t.after(() => site.close())
  const port = (site.address() as AddressInfo).port
  const inspector = new PlaywrightInspector()
  t.after(() => inspector.close())

  const healthy = await inspector.inspect({
    url: `http://127.0.0.1:${port}/?token=sensitive`,
    includeScreenshot: true,
    assertions: [
      { kind: 'text_present', text: 'Ready', exact: true },
      { kind: 'role_present', role: 'button', name: 'Save', exact: true },
      { kind: 'role_count', role: 'heading', min: 1, max: 1 },
    ],
  })
  assert.equal(healthy.status, 'succeeded')
  assert.equal(healthy.finalURL, `http://127.0.0.1:${port}/`)
  assert.match(healthy.snapshot, /Ready/)
  assert.ok(healthy.assertions.every((assertion) => assertion.passed))
  assert.ok(healthy.console.some((entry) => entry.level === 'error' && entry.message === 'advisory only'))
  assert.match(healthy.screenshot?.sha256 ?? '', /^[a-f0-9]{64}$/)
  assert.ok((healthy.screenshot?.base64.length ?? 0) > 100)

  const broken = await inspector.inspect({ url: `http://127.0.0.1:${port}/broken` })
  assert.equal(broken.status, 'failed')
  assert.equal(broken.failureKind, 'application')
  assert.ok(broken.console.some((entry) => entry.level === 'pageerror' && entry.message.includes('render failed')))

  const socket = await inspector.inspect({ url: `http://127.0.0.1:${port}/socket` })
  assert.equal(socket.status, 'succeeded')
  assert.equal(socket.failureKind, undefined)
  assert.ok(socket.network.some((entry) => entry.method === 'WEBSOCKET' && entry.failure === 'blocked WebSocket connection'))
  assert.equal(socketUpgrades, 0)
})
