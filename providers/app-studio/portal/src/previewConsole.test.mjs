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

const source = await readFile(new URL('./previewConsole.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const { PreviewConsoleController, PREVIEW_CONSOLE_ANNOTATION_PIN_HOVER, PREVIEW_CONSOLE_ANNOTATION_PIN_SELECTED, takePreviewConsoleUploadBatch } = await import(moduleURL)

function dispatchReady(frameWindow, generation, origin = 'https://preview.example.test') {
  const port = readyPort()
  listeners.get('message')({
    source: frameWindow,
    origin,
    data: { type: 'faros.preview-console.ready', version: 1, documentID: generation },
    ports: [port],
  })
  return port
}

test('App preview automatically shares console evidence without a capture control', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const apiSource = await readFile(new URL('./api.ts', import.meta.url), 'utf8')
	assert.match(appSource, /if \(projectName\) \{[\s\S]*?previewConsoleController\.connect\(projectName\)/)
  assert.doesNotMatch(appSource, /Share console/)
  assert.doesNotMatch(appSource, /Console shared/)
  assert.doesNotMatch(apiSource, /userConsent/)
  assert.match(apiSource, /portalInstanceID[\s\S]*\{ generation, protocolVersion: 1, portalInstanceID \}/)
  assert.match(apiSource, /timeoutMS: 8_000/)
  assert.match(apiSource, /timeoutMS: 5_000/)
  assert.match(apiSource, /timeoutMS: 3_000/)
  assert.equal((apiSource.match(/timeoutMessage: PREVIEW_CONSOLE_TIMEOUT_MESSAGE/g) ?? []).length, 3)
  assert.match(apiSource, /PREVIEW_CONSOLE_TIMEOUT_MESSAGE = 'preview console request timed out'/)

  const recoveryStart = appSource.indexOf('function scheduleDevelopmentPreviewRecovery()')
	  const recoveryEnd = appSource.indexOf('function handleDevelopmentPreviewVisibilityChange()', recoveryStart)
	  const recoverySource = appSource.slice(recoveryStart, recoveryEnd)
	  assert.match(recoverySource, /previewConsoleController\.reconnect\(\)/)
	  assert.match(recoverySource, /developmentPreviewRecoveryAction/)
	  assert.match(recoverySource, /recoverDevelopmentPreviewDocument/)
	  assert.match(recoverySource, /authorizeDevelopmentPreview\(\{ force: true, preserveExistingPreview: true \}\)/)
	  assert.match(appSource, /v-if="developmentPreviewRecoveryError && !developmentPreviewFrameLoaded"/)
	  assert.match(appSource, /!developmentPreviewFrameLoaded && \(developmentPreviewDocumentState === 'connecting'/)
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
  assert.equal(calls.length, 1, 'the capability must stay on the bridge-created port')
  assert.equal(handshakePort.messages[0].type, 'faros.preview-console.start')
  assert.equal(handshakePort.messages[0].capability, 'signed')
  const replayPort = readyPort()
  listeners.get('message')({ source: frameWindow, origin: 'https://preview.example.test', data: ready, ports: [replayPort] })
  assert.equal(replayPort.closed, true, 'a replayed ready message must close its unused endpoint')
  assert.deepEqual(states, ['connecting'])
  controller.destroy()
})

test('keeps annotation mode behind the authenticated port and rebinds route-scoped pins on a new document', async () => {
  const frameWindow = { postMessage() {} }
  const documents = []
  const controller = new PreviewConsoleController({
    api: {
      async createSession(_project, generation) {
        documents.push(generation)
        return {
          status: 'available',
          sessionID: `session-${documents.length}`,
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
  firstChannel.port1.onmessage({ data: { type: 'faros.preview-console.connected', version: 1, sessionID: 'session-1', generation: firstGeneration, path: '/app' } })
  assert.equal(firstChannel.port1.messages.at(-1).type, 'faros.preview-console.annotation.pins')
  assert.equal(Object.hasOwn(firstChannel.port1.messages.at(-1).pins[0], 'comment'), false)
  assert.deepEqual(firstChannel.port1.messages.at(-1).pins[0].target, { locator: '#target', locatorStrategy: 'css', rect: { x: 1, y: 2, width: 3, height: 4 }, ancestors: ['main'] })
  assert.deepEqual(firstChannel.port1.messages.at(-1).pins[0].anchor, { x: 0.25, y: 0.75 })
  assert.equal(controller.startAnnotationMode(), true)
  assert.equal(firstChannel.port1.messages.at(-1).type, 'faros.preview-console.annotation.start')
  const messagesBeforeIdenticalPins = firstChannel.port1.messages.length
  assert.equal(controller.setAnnotationPins([{ ...initialPins[0], comment: 'Make this blue now' }]), true)
  assert.equal(firstChannel.port1.messages.length, messagesBeforeIdenticalPins, 'comment-only changes must not rebuild identical preview pins')
  const lastPinMessage = firstChannel.port1.messages.findLast((message) => message.type === 'faros.preview-console.annotation.pins')
  assert.equal(Object.hasOwn(lastPinMessage.pins[0], 'comment'), false)

  const secondGeneration = '5ac4b288-a1fa-4c99-936c-07467cd3cadb'
  dispatchReady(frameWindow, secondGeneration)
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(firstChannel.port1.messages.some((message) => message.type === 'faros.preview-console.annotation.stop'), true)
  assert.deepEqual(documents, [firstGeneration, secondGeneration])
  const secondChannel = channels.at(-1)
  secondChannel.port1.onmessage({ data: { type: 'faros.preview-console.connected', version: 1, sessionID: 'session-2', generation: secondGeneration, path: '/admin.html' } })
  assert.equal(secondChannel.port1.messages.at(-1).type, 'faros.preview-console.annotation.pins')
  assert.equal(secondChannel.port1.messages.at(-1).pins.length, 1)
  assert.equal(secondChannel.port1.messages.at(-1).pins[0].documentID, secondGeneration, 'transport must bind the pin to the authenticated document')
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

test('hydrates annotation drafts per active thread and clears only after accepted sends', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(appSource, /watch\(activeAssistantAnnotationDraftScopeKey,[\s\S]*hydrateCurrentAssistantAnnotationDraft\(\)[\s\S]*\{ flush: 'post' \}/)
  assert.match(appSource, /startPostAccepted = true\s+const requestedThreadID = thread\.id\s+const canonicalThreadID = canonical\.thread\.id\.trim\(\) \|\| requestedThreadID[\s\S]*?clearStoredAssistantAnnotationDraft\(projectName, requestedThreadID\)\s+commitAttachments\(turnContentParts\)\s+clearSelectedTurnAttachments\(\)/)
  assert.match(appSource, /assistantComposerParts\.value = turnContentParts[\s\S]*persistCurrentAssistantAnnotationDraft\(turnContentParts\)/)
})

test('hot reload replaces only the local bridge without waiting for remote deletion', async () => {
  const frameWindow = { postMessage() {} }
  const firstGeneration = '826e6fa5-c38b-4bdb-8f8f-098198b74f65'
  const secondGeneration = '5ac4b288-a1fa-4c99-936c-07467cd3cadb'
  const states = []
  const created = []
  let deleteCount = 0
  const controller = new PreviewConsoleController({
    api: {
      async createSession(_project, generation) {
        created.push(generation)
        return {
          status: 'available',
          sessionID: `session-${created.length}`,
          generation,
          capability: 'signed',
          previewOrigin: 'https://preview.example.test',
          portalOrigin: 'https://console.example.test',
          expiresAt: new Date(Date.now() + 60_000).toISOString(),
        }
      },
      async uploadEvents() {},
      async deleteSession() {
        deleteCount++
      },
    },
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState: (state) => states.push(state),
  })

  await controller.connect('project-a')
  const firstPort = dispatchReady(frameWindow, firstGeneration)
  await new Promise((resolve) => setImmediate(resolve))
  firstPort.onmessage({ data: { type: 'faros.preview-console.connected', version: 1, sessionID: 'session-1', generation: firstGeneration, path: '/app' } })
  assert.equal(states.at(-1), 'connected')

  const secondPort = dispatchReady(frameWindow, secondGeneration)
  assert.equal(states.at(-1), 'connecting')
  const duplicatePort = dispatchReady(frameWindow, secondGeneration)
  assert.equal(duplicatePort.closed, true, 'duplicate READY must not start a second replacement')
  await new Promise((resolve) => setImmediate(resolve))
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(created, [firstGeneration, secondGeneration])
  assert.equal(deleteCount, 0, 'same-tab bridge replacement must not wait on best-effort DELETE')
  assert.equal(secondPort.messages.at(-1).type, 'faros.preview-console.start')
  secondPort.onmessage({ data: { type: 'faros.preview-console.connected', version: 1, sessionID: 'session-2', generation: secondGeneration, path: '/app' } })
  assert.equal(states.at(-1), 'connected')
  controller.destroy()
})

test('uses a stable, distinct portal instance ID for each App Studio tab', async () => {
  const frameWindow = { postMessage() {} }
  const created = []
  const buildController = () => new PreviewConsoleController({
    api: {
      async createSession(project, generation, portalInstanceID) {
        created.push({ project, generation, portalInstanceID })
        return {
          status: 'available',
          sessionID: `session-${created.length}`,
          generation,
          capability: 'signed',
          previewOrigin: 'https://preview.example.test',
          portalOrigin: 'https://console.example.test',
          expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
        }
      },
      async uploadEvents() {},
      async deleteSession() {},
    },
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState() {},
  })

  const first = buildController()
  await first.connect('project-a')
  dispatchReady(frameWindow, '826e6fa5-c38b-4bdb-8f8f-098198b74f65')
  await new Promise((resolve) => setImmediate(resolve))

  const second = buildController()
  await second.connect('project-a')
  dispatchReady(frameWindow, '5ac4b288-a1fa-4c99-936c-07467cd3cadb')
  await new Promise((resolve) => setImmediate(resolve))

  assert.equal(created.length, 2)
  assert.match(created[0].portalInstanceID, /^[0-9a-f-]{36}$/)
  assert.match(created[1].portalInstanceID, /^[0-9a-f-]{36}$/)
  assert.notEqual(created[0].portalInstanceID, created[1].portalInstanceID)
  second.destroy()
  first.destroy()
})

test('renews the console bridge before the 15-minute boundary without reloading the iframe', async () => {
  const originalNow = Date.now
  const originalSetTimeout = fakeWindow.setTimeout
  const originalClearTimeout = fakeWindow.clearTimeout
  const clock = Date.parse('2026-08-17T12:00:00Z')
  const timers = new Map()
  let nextTimer = 1
  Date.now = () => clock
  fakeWindow.setTimeout = (callback, delay) => {
    const id = nextTimer++
    timers.set(id, { callback, delay })
    return id
  }
  fakeWindow.clearTimeout = (id) => timers.delete(id)

  const frameMessages = []
  const frameWindow = { postMessage(message) { frameMessages.push(message) } }
  const created = []
  const deleted = []
  const generation = '826e6fa5-c38b-4bdb-8f8f-098198b74f65'
  const controller = new PreviewConsoleController({
    api: {
      async createSession(project, currentGeneration, portalInstanceID) {
        created.push({ project, generation: currentGeneration, portalInstanceID })
        return {
          status: 'available',
          sessionID: `session-${created.length}`,
          generation: currentGeneration,
          capability: 'signed',
          previewOrigin: 'https://preview.example.test',
          portalOrigin: 'https://console.example.test',
          expiresAt: new Date(clock + 15 * 60_000).toISOString(),
        }
      },
      async uploadEvents() {},
      async deleteSession(_project, sessionID) { deleted.push(sessionID) },
    },
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState() {},
  })

  try {
    await controller.connect('project-a')
    dispatchReady(frameWindow, generation)
    await new Promise((resolve) => setImmediate(resolve))
    const firstPort = channels.at(-1).port1
    firstPort.onmessage({ data: { type: 'faros.preview-console.connected', version: 1, sessionID: 'session-1', generation, path: '/app' } })
    const renewal = [...timers.values()].find((timer) => timer.delay === 14 * 60_000 + 30_000)
    assert.ok(renewal, 'renewal should be scheduled 30 seconds before the 15-minute expiry')

    renewal.callback()
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(frameMessages.filter((message) => message.type === 'faros.preview-console.probe').length, 2)
    assert.deepEqual(deleted, [], 'renewal must not block on deleting its prior session')

    dispatchReady(frameWindow, generation)
    await new Promise((resolve) => setImmediate(resolve))
    assert.equal(created.length, 2)
    assert.equal(created[0].portalInstanceID, created[1].portalInstanceID, 'renewal must retain the tab identity')
  } finally {
    await controller.disconnect()
    controller.destroy()
    Date.now = originalNow
    fakeWindow.setTimeout = originalSetTimeout
    fakeWindow.clearTimeout = originalClearTimeout
  }
})

test('port failure closes the session and rejects oversized pin state explicitly', async () => {
  const frameWindow = { postMessage() {} }
  const generation = '826e6fa5-c38b-4bdb-8f8f-098198b74f65'
  const states = []
  const deleted = []
  const controller = new PreviewConsoleController({
    api: {
      async createSession(_project, currentGeneration) {
        return {
          status: 'available',
          sessionID: 'session-failure',
          generation: currentGeneration,
          capability: 'signed',
          previewOrigin: 'https://preview.example.test',
          portalOrigin: 'https://console.example.test',
          expiresAt: new Date(Date.now() + 60_000).toISOString(),
        }
      },
      async uploadEvents() {},
      async deleteSession(_project, sessionID) { deleted.push(sessionID) },
    },
    getFrame: () => ({ src: 'https://preview.example.test/app', contentWindow: frameWindow }),
    onState: (state) => states.push(state),
  })

  await controller.connect('project-a')
  const port = dispatchReady(frameWindow, generation)
  await new Promise((resolve) => setImmediate(resolve))
  port.onmessage({ data: { type: 'faros.preview-console.connected', version: 1, sessionID: 'session-failure', generation, path: '/app' } })
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
  assert.deepEqual(deleted, ['session-failure'])
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
  const controller = new PreviewConsoleController({
    api: {
      async createSession(_project, documentID) {
        return {
          status: 'available',
          sessionID: 'session-selection',
          generation: documentID,
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
    sessionID: 'session-selection',
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
  channel.port1.onmessage({ data: { ...envelope, type: 'faros.preview-console.annotation.mode', active: true } })
  assert.deepEqual(modes, [true])

  channel.port1.onmessage({
    data: { ...envelope, type: 'faros.preview-console.annotation.selected', documentID: 'stale-document' },
  })
  assert.equal(selections.length, 0, 'a selection from an older document must not reach the composer')

  channel.port1.onmessage({ data: { ...envelope, type: 'faros.preview-console.annotation.selected', documentID: generation } })
  assert.deepEqual(selections, [{
    documentID: generation,
    pagePath: '/settings',
    viewport: { width: 1024, height: 768 },
    target: envelope.target,
    anchor: { x: 0.25, y: 0.75 },
  }])

  channel.port1.onmessage({
    data: { ...envelope, type: 'faros.preview-console.annotation.selected', documentID: generation, anchor: { x: 1.01, y: 0.5 } },
  })
  assert.equal(selections.length, 1, 'an out-of-element anchor must not reach the composer')

  channel.port1.onmessage({
    data: {
      ...envelope,
      type: 'faros.preview-console.annotation.pins-rendered',
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
      type: PREVIEW_CONSOLE_ANNOTATION_PIN_HOVER,
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
      type: PREVIEW_CONSOLE_ANNOTATION_PIN_HOVER,
      documentID: generation,
      id: 'annotation-1',
      active: false,
      rect: { x: 12, y: 24, width: 120, height: 32 },
    },
  })
  channel.port1.onmessage({
    data: {
      ...envelope,
      type: PREVIEW_CONSOLE_ANNOTATION_PIN_HOVER,
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
      type: PREVIEW_CONSOLE_ANNOTATION_PIN_SELECTED,
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
      type: PREVIEW_CONSOLE_ANNOTATION_PIN_SELECTED,
      documentID: 'stale-document',
      id: 'annotation-1',
      rect: { x: 12, y: 24, width: 120, height: 32 },
      viewport: { width: 1024, height: 768 },
    },
  })
  assert.deepEqual(pinSelections, [
    { id: 'annotation-1', pagePath: '/settings', rect: { x: 12, y: 24, width: 120, height: 32 }, viewport: { width: 1024, height: 768 } },
  ])

  channel.port1.onmessage({ data: { ...envelope, type: 'faros.preview-console.annotation.cancelled' } })
  assert.deepEqual(modes, [true, false])
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
  dispatchReady(frameWindow, '826e6fa5-c38b-4bdb-8f8f-098198b74f65')
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
  dispatchReady(frameWindow, '826e6fa5-c38b-4bdb-8f8f-098198b74f65')
  await new Promise((resolve) => setImmediate(resolve))

  const staleConnect = controller.connect('stale')
  const latestConnect = controller.connect('latest')
  await latestConnect
  resolveDelete()
  await staleConnect
  dispatchReady(frameWindow, '5ac4b288-a1fa-4c99-936c-07467cd3cadb')
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
  dispatchReady(frameWindow, '826e6fa5-c38b-4bdb-8f8f-098198b74f65')
  await new Promise((resolve) => setImmediate(resolve))

  const staleDisconnect = controller.disconnect()
  const latestConnect = controller.connect('latest')
  await latestConnect
  resolveDelete()
  await staleDisconnect
  dispatchReady(frameWindow, '5ac4b288-a1fa-4c99-936c-07467cd3cadb')
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
  dispatchReady(frameWindow, generation)
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
