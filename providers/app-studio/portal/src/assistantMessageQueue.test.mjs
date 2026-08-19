import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ appType: 'custom', cacheDir: '/tmp/faros-vite-assistant-message-queue', configFile: false, server: { middlewareMode: true, hmr: false } })
const queue = await vite.ssrLoadModule('/src/assistantMessageQueue.ts')
test.after(async () => vite.close())

const scope = {
  tenant: 'tenant-a',
  orgUUID: 'org-a',
  workspaceUUID: 'workspace-a',
  user: 'user-a',
  project: 'project-a',
  thread: 'thread-a',
}

function memoryStorage() {
  const values = new Map()
  return {
    values,
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  }
}

test('persists a bounded queue for the exact user, project, and thread scope', () => {
  const storage = memoryStorage()
  const now = Date.parse('2026-08-19T12:00:00Z')
  const messages = [
    { id: 'queued-1', content: 'Add a receipt screen', createdAt: new Date(now - 2_000).toISOString() },
    { id: 'queued-2', content: 'Then add tests', createdAt: new Date(now - 1_000).toISOString() },
  ]

  assert.equal(queue.writeAssistantMessageQueue(scope, messages, storage, now), true)
  assert.deepEqual(queue.readAssistantMessageQueue(scope, storage, now), messages)
  assert.deepEqual(queue.readAssistantMessageQueue({ ...scope, thread: 'thread-b' }, storage, now), [])
  assert.deepEqual(queue.readAssistantMessageQueue({ ...scope, user: 'user-b' }, storage, now), [])
  assert.equal(queue.assistantMessageQueueStorageKey({ ...scope, project: '' }), '')
})

test('drops malformed, duplicate, oversized, future, and expired queue entries', () => {
  const storage = memoryStorage()
  const key = queue.assistantMessageQueueStorageKey(scope)
  const now = Date.parse('2026-08-19T12:00:00Z')
  storage.setItem(key, JSON.stringify({
    version: 1,
    messages: [
      { id: 'valid', content: 'Keep me', createdAt: new Date(now - 1_000).toISOString() },
      { id: 'valid', content: 'Duplicate', createdAt: new Date(now - 900).toISOString() },
      { id: 'blank', content: ' ', createdAt: new Date(now - 800).toISOString() },
      { id: 'large', content: 'x'.repeat(queue.ASSISTANT_MESSAGE_QUEUE_MAX_CONTENT_LENGTH + 1), createdAt: new Date(now - 700).toISOString() },
      { id: 'future', content: 'Too early', createdAt: new Date(now + 60_001).toISOString() },
      { id: 'expired', content: 'Too old', createdAt: new Date(now - queue.ASSISTANT_MESSAGE_QUEUE_MAX_AGE_MS - 1).toISOString() },
    ],
  }))

  assert.deepEqual(queue.readAssistantMessageQueue(scope, storage, now), [
    { id: 'valid', content: 'Keep me', createdAt: new Date(now - 1_000).toISOString() },
  ])
  assert.equal(JSON.parse(storage.getItem(key)).messages.length, 1)

  storage.setItem(key, '{bad json')
  assert.deepEqual(queue.readAssistantMessageQueue(scope, storage, now), [])
  assert.equal(storage.getItem(key), null)
})

test('persists queueing mode for the exact conversation and defaults safely to enabled', () => {
  const storage = memoryStorage()
  assert.equal(queue.readAssistantQueueingEnabled(scope, storage), true)
  assert.equal(queue.writeAssistantQueueingEnabled(scope, false, storage), true)
  assert.equal(queue.readAssistantQueueingEnabled(scope, storage), false)
  assert.equal(queue.readAssistantQueueingEnabled({ ...scope, thread: 'thread-b' }, storage), true)

  const key = queue.assistantQueueingPreferenceStorageKey(scope)
  storage.setItem(key, JSON.stringify({ version: 1, queueingEnabled: 'no' }))
  assert.equal(queue.readAssistantQueueingEnabled(scope, storage), true)
  assert.equal(storage.getItem(key), null)
})

test('composer and queue controls expose queue-by-default and explicit steering', async () => {
  const [composer, app, queueView] = await Promise.all([
    readFile(new URL('./AssistantRichComposer.vue', import.meta.url), 'utf8'),
    readFile(new URL('./App.vue', import.meta.url), 'utf8'),
    readFile(new URL('./AssistantMessageQueue.vue', import.meta.url), 'utf8'),
  ])

  assert.match(composer, /\(event\.metaKey \|\| event\.ctrlKey\) \|\| !props\.queueingEnabled/)
  assert.match(app, /@submit\.prevent="sendMessage\(assistantActiveRunSubmitIntent\(\)\)"/)
  assert.match(app, /activeRunIntent === 'queue'[\s\S]*enqueueAssistantMessage\(content\)/)
  assert.match(app, /activeRunIntent === 'steer'[\s\S]*api\.steerAssistantTurn/)
  assert.match(app, /api\.steerAssistantTurn[\s\S]*messages\.value = messages\.value\.filter\(\(message\) => message\.id !== optimisticID\)[\s\S]*projectAssistantThreadItems\(items, projectName, true\)/)
  assert.match(app, /@steer="steerQueuedAssistantMessage"/)
  assert.match(app, /@edit="editQueuedAssistantMessage"/)
  assert.match(app, /@toggle-queueing="toggleAssistantQueueing"/)
  assert.doesNotMatch(queueView, /Up next/)
  assert.match(queueView, /Edit message/)
  assert.match(queueView, /Turn off queueing/)
  assert.match(queueView, /'Steer'/)
  assert.match(queueView, /@click="\$emit\('steer', message\)"/)
})
