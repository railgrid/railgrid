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
  messages = []
  closed = false
  throwOnPost = false
  postMessage(message) {
    if (this.throwOnPost) throw new Error('port is closed')
    this.messages.push(structuredClone(message))
  }
  close() { this.closed = true }
  start() {}
}
const channels = []
const readyPort = () => {
  const channel = { port1: new FakePort() }
  channels.push(channel)
  return channel.port1
}

const source = await readFile(new URL('./previewBridge.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const { PreviewBridgeController, PREVIEW_BRIDGE_ANNOTATION_PIN_HOVER, PREVIEW_BRIDGE_ANNOTATION_PIN_SELECTED } = await import(moduleURL)

// The controller mints the session ID itself and sends it in START; the
// bridge echoes it on every port message.
function startOf(port) {
  const start = port.messages.find((message) => message.type === 'railgrid.preview-bridge.start')
  assert.ok(start, 'controller must send START on the bridge-created port')
  return start
}

function connected(port, generation, path = '/app') {
  port.onmessage({ data: { type: 'railgrid.preview-bridge.connected', version: 1, sessionID: startOf(port).sessionID, generation, path } })
}

function dispatchReady(frameWindow, generation, origin = 'https://preview.example.test') {
  const port = readyPort()
  listeners.get('message')({
    source: frameWindow,
    origin,
    data: { type: 'railgrid.preview-bridge.ready', version: 1, documentID: generation },
    ports: [port],
  })
  return port
}

test('App preview keeps the origin-bound bridge and annotation contract without console-event transport', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const apiSource = await readFile(new URL('./api.ts', import.meta.url), 'utf8')
	assert.match(appSource, /if \(projectName\) \{[\s\S]*?previewBridgeController\.connect\(projectName\)/)
  assert.doesNotMatch(appSource, /Share console/)
  assert.doesNotMatch(appSource, /Console shared/)
  assert.doesNotMatch(appSource, /uploadEvents|uploadPreviewBridgeEvents|preview-bridge\.events/)
  assert.doesNotMatch(apiSource, /PreviewBridgeEvent|uploadPreviewBridgeEvents|preview-bridge\.events/)
  assert.doesNotMatch(apiSource, /preview-bridge-sessions|PreviewBridgeSession/, 'the bridge has no server-side session API')
  assert.doesNotMatch(appSource, /createPreviewBridgeSession|deletePreviewBridgeSession/)

  const recoveryStart = appSource.indexOf('function scheduleDevelopmentPreviewRecovery()')
	  const recoveryEnd = appSource.indexOf('function handleDevelopmentPreviewVisibilityChange()', recoveryStart)
	  const recoverySource = appSource.slice(recoveryStart, recoveryEnd)
	  assert.match(recoverySource, /previewBridgeController\.reconnect\(\)/)
	  assert.match(recoverySource, /developmentPreviewRecoveryAction/)
	  assert.match(recoverySource, /recoverDevelopmentPreviewDocument/)
	  assert.match(recoverySource, /authorizeDevelopmentPreview\(\{ force: true, preserveExistingPreview: true \}\)/)
	  assert.match(appSource, /:inert="!developmentPreviewFrameVisible"/)
	  assert.match(appSource, /v-else-if="!developmentPreviewFrameVisible"/)
})

test('binds only a READY from the exact iframe window and preview origin', async () => {
  const calls = []
  const frameWindow = {
    postMessage(message, origin, ports) {
      calls.push({ message, origin, ports })
    },
  }
  const states = []
  const controller = new PreviewBridgeController({
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState: (state) => states.push(state),
  })

  await controller.connect('project-a')
  assert.equal(calls[0].message.type, 'railgrid.preview-bridge.probe')
  const ready = { type: 'railgrid.preview-bridge.ready', version: 1, documentID: '826e6fa5-c38b-4bdb-8f8f-098198b74f65' }
  listeners.get('message')({ source: {}, origin: 'https://preview.example.test', data: ready, ports: [readyPort()] })
  listeners.get('message')({ source: frameWindow, origin: 'https://attacker.example.test', data: ready, ports: [readyPort()] })
  assert.equal(calls.length, 1)
  const malformedPort = readyPort()
  assert.doesNotThrow(() => listeners.get('message')({
    source: frameWindow,
    origin: 'https://preview.example.test',
    data: { ...ready, documentID: 42 },
    ports: [malformedPort],
  }))
  assert.equal(malformedPort.closed, true, 'malformed READY generations must fail closed')

  const handshakePort = readyPort()
  listeners.get('message')({ source: frameWindow, origin: 'https://preview.example.test', data: ready, ports: [handshakePort] })
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(calls.length, 1, 'START must stay on the bridge-created port')
  assert.equal(handshakePort.messages[0].type, 'railgrid.preview-bridge.start')
  assert.equal(handshakePort.messages[0].generation, ready.documentID)
  assert.match(handshakePort.messages[0].sessionID, /^[0-9a-f-]{36}$/)
  assert.equal(Object.hasOwn(handshakePort.messages[0], 'capability'), false)
  const replayPort = readyPort()
  listeners.get('message')({ source: frameWindow, origin: 'https://preview.example.test', data: ready, ports: [replayPort] })
  assert.equal(replayPort.closed, true, 'a replayed ready message must close its unused endpoint')
  assert.deepEqual(states, ['connecting'])
  controller.destroy()
})

test('keeps annotation mode behind the connected port and rebinds route-scoped pins on a new document', async () => {
  const frameWindow = { postMessage() {} }
  const controller = new PreviewBridgeController({
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState() {},
  })

  await controller.connect('project-a')
  assert.equal(controller.startAnnotationMode(), false, 'mode cannot start before bridge connected')
  const firstGeneration = '826e6fa5-c38b-4bdb-8f8f-098198b74f65'
  dispatchReady(frameWindow, firstGeneration)
  await new Promise((resolve) => setImmediate(resolve))
  const firstChannel = channels.at(-1)
  const reactiveRect = new Proxy({ x: 1, y: 2, width: 3, height: 4 }, {})
  const reactiveTarget = new Proxy({ locator: '#target', locatorStrategy: 'css', rect: reactiveRect, ancestors: new Proxy(['main'], {}) }, {})
  const initialPins = [{ id: 'a', number: 1, documentID: firstGeneration, pagePath: '/app', boundingRect: reactiveRect, target: reactiveTarget, anchor: new Proxy({ x: 0.25, y: 0.75 }, {}), comment: 'Make this blue' }]
  assert.equal(controller.setAnnotationPins(initialPins), false, 'desired pins should be retained while the bridge connects')
  connected(firstChannel.port1, firstGeneration)
  assert.equal(firstChannel.port1.messages.at(-1).type, 'railgrid.preview-bridge.annotation.pins')
  assert.equal(Object.hasOwn(firstChannel.port1.messages.at(-1).pins[0], 'comment'), false)
  assert.deepEqual(firstChannel.port1.messages.at(-1).pins[0].target, { locator: '#target', locatorStrategy: 'css', rect: { x: 1, y: 2, width: 3, height: 4 }, ancestors: ['main'] })
  assert.deepEqual(firstChannel.port1.messages.at(-1).pins[0].anchor, { x: 0.25, y: 0.75 })
  assert.equal(controller.startAnnotationMode(), true)
  assert.equal(firstChannel.port1.messages.at(-1).type, 'railgrid.preview-bridge.annotation.start')
  const messagesBeforeIdenticalPins = firstChannel.port1.messages.length
  assert.equal(controller.setAnnotationPins([{ ...initialPins[0], comment: 'Make this blue now' }]), true)
  assert.equal(firstChannel.port1.messages.length, messagesBeforeIdenticalPins, 'comment-only changes must not rebuild identical preview pins')
  const lastPinMessage = firstChannel.port1.messages.findLast((message) => message.type === 'railgrid.preview-bridge.annotation.pins')
  assert.equal(Object.hasOwn(lastPinMessage.pins[0], 'comment'), false)

  const secondGeneration = '5ac4b288-a1fa-4c99-936c-07467cd3cadb'
  dispatchReady(frameWindow, secondGeneration)
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(firstChannel.port1.messages.some((message) => message.type === 'railgrid.preview-bridge.annotation.stop'), true)
  const secondChannel = channels.at(-1)
  assert.equal(startOf(secondChannel.port1).generation, secondGeneration)
  assert.notEqual(startOf(secondChannel.port1).sessionID, startOf(firstChannel.port1).sessionID, 'each document gets a fresh session')
  connected(secondChannel.port1, secondGeneration, '/admin.html')
  assert.equal(secondChannel.port1.messages.at(-1).type, 'railgrid.preview-bridge.annotation.pins')
  assert.equal(secondChannel.port1.messages.at(-1).pins.length, 1)
  assert.equal(secondChannel.port1.messages.at(-1).pins[0].documentID, secondGeneration, 'transport must bind the pin to the connected document')
  assert.equal(secondChannel.port1.messages.at(-1).pins[0].pagePath, '/app', 'the annotated route must remain stable across document navigation')
  controller.destroy()
})

test('annotation commit synchronizes durable pins immediately', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(appSource, /assistantComposerParts\.value = draft\.annotationID[\s\S]*?: \[\.\.\.assistantComposerParts\.value, validatedPart\][\s\S]*syncDevelopmentPreviewAnnotationPins\(\)/)
  assert.match(appSource, /watch\([\s\S]*syncDevelopmentPreviewAnnotationPins,[\s\S]*\{ flush: 'post' \}/)
  assert.match(appSource, /onAnnotationPinsRendered: handleDevelopmentPreviewAnnotationPinsRendered/)
  assert.match(appSource, /const developmentPreviewAnnotationPinSignature = computed/)
  assert.match(appSource, /developmentPreviewAnnotationPinSignature,[\s\S]*syncDevelopmentPreviewAnnotationPins/)
  assert.doesNotMatch(appSource, /developmentPreviewAnnotations\.value\.map\(\(annotation\)[\s\S]*syncDevelopmentPreviewAnnotationPins/)
  assert.match(appSource, /pagePath: annotation\.pagePath/)
  assert.match(appSource, /documentID,[\s\S]*pagePath: annotation\.pagePath/)
  assert.doesNotMatch(appSource, /annotation\.documentID === developmentPreviewAnnotationDocumentID\.value && annotation\.target\.rect/)
  assert.match(appSource, /onAnnotationPinSelect: handleDevelopmentPreviewAnnotationPinSelect/)
  assert.match(appSource, /updateAssistantComposerAnnotation\(assistantComposerParts\.value, validatedPart\.annotation\)/)
  assert.match(appSource, /removeAssistantComposerAnnotation\(assistantComposerParts\.value, annotationID\)/)
  assert.match(appSource, /aria-label="Delete annotation"/)
})

test('hydrates composer drafts per active thread and clears only after accepted sends', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(appSource, /const activeAssistantComposerDraftScopeKey = computed\(\(\) => assistantComposerDraftStorageKey\(assistantComposerDraftScope\(\)\)\)/)
  assert.match(appSource, /watch\(activeAssistantComposerDraftScopeKey,[\s\S]*hydrateCurrentAssistantComposerDraft\(\)[\s\S]*\{ flush: 'sync' \}/)
  assert.match(appSource, /startPostAccepted = true\s+submissionOwner\.postAccepted = true\s+const requestedThreadID = thread\.id\s+const canonicalThreadID = canonical\.thread\.id\.trim\(\) \|\| requestedThreadID[\s\S]*?clearStoredAssistantComposerDraft\(projectName, requestedThreadID, sendContext, projectUID\)\s+commitAttachments\(turnContentParts, assistantComposerDraftScope\(projectName, submissionOwner\.threadID, sendContext, projectUID\)\)[\s\S]*?clearSelectedTurnAttachments\(\)/)
  assert.match(appSource, /function updateAssistantComposerParts\(parts: ProjectAssistantContentPart\[\]\) \{\s+assistantComposerParts\.value = parts\.slice\(0, MAX_ASSISTANT_COMPOSER_PARTS\)\s+persistCurrentAssistantComposerDraft\(\)/)
})

test('hot reload replaces the bridge port for the new document', async () => {
  const frameWindow = { postMessage() {} }
  const firstGeneration = '826e6fa5-c38b-4bdb-8f8f-098198b74f65'
  const secondGeneration = '5ac4b288-a1fa-4c99-936c-07467cd3cadb'
  const states = []
  const controller = new PreviewBridgeController({
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState: (state) => states.push(state),
  })

  await controller.connect('project-a')
  const firstPort = dispatchReady(frameWindow, firstGeneration)
  await new Promise((resolve) => setImmediate(resolve))
  connected(firstPort, firstGeneration)
  assert.equal(states.at(-1), 'connected')

  const secondPort = dispatchReady(frameWindow, secondGeneration)
  assert.equal(states.at(-1), 'connecting')
  const duplicatePort = dispatchReady(frameWindow, secondGeneration)
  assert.equal(duplicatePort.closed, true, 'duplicate READY must not start a second replacement')
  await new Promise((resolve) => setImmediate(resolve))
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(firstPort.closed, true, 'the previous document port is closed')
  assert.equal(secondPort.messages.at(-1).type, 'railgrid.preview-bridge.start')
  assert.equal(startOf(secondPort).generation, secondGeneration)
  connected(secondPort, secondGeneration)
  assert.equal(states.at(-1), 'connected')
  controller.destroy()
})

test('ignores port messages that do not carry the bound session', async () => {
  const frameWindow = { postMessage() {} }
  const generation = '826e6fa5-c38b-4bdb-8f8f-098198b74f65'
  const states = []
  const controller = new PreviewBridgeController({
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState: (state) => states.push(state),
  })
  await controller.connect('project-a')
  const port = dispatchReady(frameWindow, generation)
  port.onmessage({ data: { type: 'railgrid.preview-bridge.connected', version: 1, sessionID: 'guessed', generation, path: '/app' } })
  assert.equal(states.at(-1), 'connecting', 'a foreign session ID must not connect the bridge')
  port.onmessage({ data: { type: 'railgrid.preview-bridge.connected', version: 1, sessionID: startOf(port).sessionID, generation: 'other-document', path: '/app' } })
  assert.equal(states.at(-1), 'connecting', 'a foreign document must not connect the bridge')
  connected(port, generation)
  assert.equal(states.at(-1), 'connected')
  controller.destroy()
})

test('port failure closes the session and rejects oversized pin state explicitly', async () => {
  const frameWindow = { postMessage() {} }
  const generation = '826e6fa5-c38b-4bdb-8f8f-098198b74f65'
  const states = []
  const controller = new PreviewBridgeController({
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState: (state) => states.push(state),
  })

  await controller.connect('project-a')
  const port = dispatchReady(frameWindow, generation)
  await new Promise((resolve) => setImmediate(resolve))
  connected(port, generation)
  assert.equal(states.at(-1), 'connected')
  const acceptedPins = Array.from({ length: 64 }, (_, index) => ({
    id: `accepted-${index}`,
    number: index + 1,
    documentID: generation,
    pagePath: '/app',
    boundingRect: { x: 0, y: 0, width: 1, height: 1 },
    target: { locator: '#target', locatorStrategy: 'css' },
  }))
  assert.equal(controller.setAnnotationPins(acceptedPins), true, 'the controller must accept the full 64-pin envelope')
  assert.equal(port.messages.at(-1).pins.length, 64)
  const before = port.messages.length
  port.throwOnPost = true
  assert.equal(controller.setAnnotationPins(Array.from({ length: 65 }, (_, index) => ({
    id: `annotation-${index}`,
    number: index + 1,
    documentID: generation,
    boundingRect: { x: 0, y: 0, width: 1, height: 1 },
    target: { locator: '#target', locatorStrategy: 'css' },
  }))), false, 'oversized pin state must be rejected instead of truncated')
  assert.equal(port.messages.length, before, 'rejected pin state must not be sent')
  assert.equal(port.closed, false, 'validation should not close a healthy port')

  assert.equal(controller.setAnnotationPins([{
    id: 'annotation-1',
    number: 1,
    documentID: generation,
    boundingRect: { x: 0, y: 0, width: 1, height: 1 },
    target: { locator: '#target', locatorStrategy: 'css' },
  }]), false, 'a failed port must report an unsuccessful pin update')
  assert.equal(port.closed, true)
  assert.equal(states.at(-1), 'unavailable')
  assert.equal(controller.startAnnotationMode(), false)
  controller.destroy()
})

test('projects only current-document annotation selections and relays mode cancellation', async () => {
  const frameWindow = { postMessage() {} }
  const generation = '826e6fa5-c38b-4bdb-8f8f-098198b74f65'
  const selections = []
  const modes = []
  const renderedPins = []
  const pinHovers = []
  const pinSelections = []
  const controller = new PreviewBridgeController({
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState() {},
    onAnnotation: (selection) => selections.push(selection),
    onAnnotationMode: (active) => modes.push(active),
    onAnnotationPinsRendered: (documentID, pagePath, states) => renderedPins.push({ documentID, pagePath, states }),
    onAnnotationPinHover: (hover) => pinHovers.push(hover),
    onAnnotationPinSelect: (selection) => pinSelections.push(selection),
  })

  await controller.connect('project-a')
  dispatchReady(frameWindow, generation)
  await new Promise((resolve) => setImmediate(resolve))
  const channel = channels.at(-1)
  const envelope = {
    version: 1,
    sessionID: startOf(channel.port1).sessionID,
    generation,
    viewport: { width: 1024, height: 768 },
    anchor: { x: 0.25, y: 0.75 },
    path: '/settings',
    target: {
      tag: 'button',
      role: 'button',
      name: 'Save changes',
      text: 'Save changes',
      rect: { x: 4, y: 8, width: 120, height: 32 },
      ancestors: ['main'],
      locator: '#save',
      locatorStrategy: 'css',
    },
  }
  channel.port1.onmessage({ data: { ...envelope, type: 'railgrid.preview-bridge.annotation.mode', active: true } })
  assert.deepEqual(modes, [true])

  channel.port1.onmessage({
    data: { ...envelope, type: 'railgrid.preview-bridge.annotation.selected', documentID: 'stale-document' },
  })
  assert.equal(selections.length, 0, 'a selection from an older document must not reach the composer')

  channel.port1.onmessage({ data: { ...envelope, type: 'railgrid.preview-bridge.annotation.selected', documentID: generation } })
  assert.deepEqual(selections, [{
    documentID: generation,
    pagePath: '/settings',
    viewport: { width: 1024, height: 768 },
    target: envelope.target,
    anchor: { x: 0.25, y: 0.75 },
  }])

  channel.port1.onmessage({
    data: { ...envelope, type: 'railgrid.preview-bridge.annotation.selected', documentID: generation, anchor: { x: 1.01, y: 0.5 } },
  })
  assert.equal(selections.length, 1, 'an out-of-element anchor must not reach the composer')

  channel.port1.onmessage({
    data: {
      ...envelope,
      type: 'railgrid.preview-bridge.annotation.pins-rendered',
      documentID: generation,
      pins: [{ id: ' annotation-1 ', resolved: true }, { id: '', resolved: true }],
    },
  })
  assert.deepEqual(renderedPins, [{
    documentID: generation,
    pagePath: '/settings',
    states: [{ id: 'annotation-1', resolved: true }],
  }])

  channel.port1.onmessage({
    data: {
      ...envelope,
      type: PREVIEW_BRIDGE_ANNOTATION_PIN_HOVER,
      documentID: generation,
      id: ' annotation-1 ',
      active: true,
      rect: { x: 12, y: 24, width: 120, height: 32 },
      comment: 'must never cross the bridge',
    },
  })
  channel.port1.onmessage({
    data: {
      ...envelope,
      type: PREVIEW_BRIDGE_ANNOTATION_PIN_HOVER,
      documentID: generation,
      id: 'annotation-1',
      active: false,
      rect: { x: 12, y: 24, width: 120, height: 32 },
    },
  })
  channel.port1.onmessage({
    data: {
      ...envelope,
      type: PREVIEW_BRIDGE_ANNOTATION_PIN_HOVER,
      documentID: 'stale-document',
      id: 'annotation-1',
      active: true,
      rect: { x: 12, y: 24, width: 120, height: 32 },
    },
  })
  assert.deepEqual(pinHovers, [
    { id: 'annotation-1', active: true, pagePath: '/settings', rect: { x: 12, y: 24, width: 120, height: 32 } },
    { id: 'annotation-1', active: false, pagePath: '/settings', rect: { x: 12, y: 24, width: 120, height: 32 } },
  ])
  assert.equal(Object.hasOwn(pinHovers[0], 'comment'), false)

  channel.port1.onmessage({
    data: {
      ...envelope,
      type: PREVIEW_BRIDGE_ANNOTATION_PIN_SELECTED,
      documentID: generation,
      id: ' annotation-1 ',
      rect: { x: 12, y: 24, width: 120, height: 32 },
      viewport: { width: 1024, height: 768 },
      comment: 'must never cross the bridge',
    },
  })
  channel.port1.onmessage({
    data: {
      ...envelope,
      type: PREVIEW_BRIDGE_ANNOTATION_PIN_SELECTED,
      documentID: 'stale-document',
      id: 'annotation-1',
      rect: { x: 12, y: 24, width: 120, height: 32 },
      viewport: { width: 1024, height: 768 },
    },
  })
  assert.deepEqual(pinSelections, [
    { id: 'annotation-1', pagePath: '/settings', rect: { x: 12, y: 24, width: 120, height: 32 }, viewport: { width: 1024, height: 768 } },
  ])

  channel.port1.onmessage({ data: { ...envelope, type: 'railgrid.preview-bridge.annotation.cancelled' } })
  assert.deepEqual(modes, [true, false])
  controller.destroy()
})

test('a preview without the bridge reports unavailable rather than connected', async () => {
  const originalSetTimeout = fakeWindow.setTimeout
  const originalClearTimeout = fakeWindow.clearTimeout
  const timers = new Map()
  let nextTimer = 1
  fakeWindow.setTimeout = (callback, delay) => {
    const id = nextTimer++
    timers.set(id, { callback, delay })
    return id
  }
  fakeWindow.clearTimeout = (id) => timers.delete(id)
  const states = []
  const frameWindow = { postMessage() {} }
  const controller = new PreviewBridgeController({
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState: (state) => states.push(state),
  })
  try {
    await controller.connect('project-a')
    const timeout = [...timers.values()].find((timer) => timer.delay === 3_000)
    assert.ok(timeout, 'connect must bound the wait for READY')
    timeout.callback()
    assert.deepEqual(states, ['connecting', 'unavailable'])
  } finally {
    controller.destroy()
    fakeWindow.setTimeout = originalSetTimeout
    fakeWindow.clearTimeout = originalClearTimeout
  }
})

test('the newest connect or disconnect wins', async () => {
  const frameWindow = { postMessage() {} }
  const states = []
  const controller = new PreviewBridgeController({
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState: (state) => states.push(state),
  })

  await controller.connect('initial')
  const initialPort = dispatchReady(frameWindow, '826e6fa5-c38b-4bdb-8f8f-098198b74f65')
  connected(initialPort, '826e6fa5-c38b-4bdb-8f8f-098198b74f65')

  const staleDisconnect = controller.disconnect()
  const latestConnect = controller.connect('latest')
  await Promise.all([staleDisconnect, latestConnect])
  assert.equal(initialPort.closed, true)
  assert.equal(states.at(-1), 'connecting', 'a stale disconnect must not clear the newer connection')

  const latestPort = dispatchReady(frameWindow, '5ac4b288-a1fa-4c99-936c-07467cd3cadb')
  connected(latestPort, '5ac4b288-a1fa-4c99-936c-07467cd3cadb')
  assert.equal(states.at(-1), 'connected')
  controller.destroy()
})
