import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ server: { middlewareMode: true, hmr: false }, appType: 'custom' })
const {
  ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS,
  assistantComposerDraftStorageKey,
  clearAssistantComposerDraft,
  readAssistantComposerDraft,
  writeAssistantComposerDraft,
} = await vite.ssrLoadModule('/src/assistantComposerDraft.ts')

test.after(async () => vite.close())

const scope = {
  tenant: 'tenant-a',
  orgUUID: 'org-a',
  workspaceUUID: 'workspace-a',
  user: 'user-a',
  project: 'project-a',
  projectUID: 'project-uid-a',
  thread: 'thread-a',
}
const resource = {
  provider: 'databricks',
  resourceRef: {
    apiVersion: 'databricks.railgrid.ai/v1alpha1',
    kind: 'Table',
    resource: 'tables',
    name: 'taxi-trips',
  },
}
const attachment = {
  type: 'attachment',
  attachment: {
    id: 'receipt-a',
    filename: 'notes.txt',
    contentType: 'text/plain',
    sizeBytes: 5,
    sha256: 'a'.repeat(64),
    createdAt: new Date(10_000).toISOString(),
  },
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

test('stores a complete composer draft only for its exact user, project UID, and thread', () => {
  const storage = memoryStorage()
  const draft = {
    content: 'Review this data and screenshot',
    contentParts: [
      { type: 'text', text: 'Review this data and screenshot' },
      { type: 'skill', skillID: 'sql-review' },
      { type: 'resource', resourceIndex: 0 },
      attachment,
    ],
    skillIDs: ['sql-review'],
    resources: [resource],
    responseMode: 'plan',
  }
  assert.equal(writeAssistantComposerDraft(scope, draft, storage, 10_000), true)
  assert.deepEqual(readAssistantComposerDraft(scope, storage, 10_100), draft)
  assert.deepEqual(readAssistantComposerDraft({ ...scope, user: 'user-b' }, storage, 10_100).contentParts, [])
  assert.deepEqual(readAssistantComposerDraft({ ...scope, projectUID: 'project-uid-b' }, storage, 10_100).contentParts, [])
  assert.deepEqual(readAssistantComposerDraft({ ...scope, thread: 'thread-b' }, storage, 10_100).contentParts, [])
})

test('ready receipts persist as content parts while pending upload state is never stored', () => {
  const storage = memoryStorage()
  const draft = {
    content: 'Attach the file',
    contentParts: [attachment],
    skillIDs: [],
    resources: [],
    responseMode: 'default',
    attachmentsPending: true,
  }
  assert.equal(writeAssistantComposerDraft(scope, draft, storage, 10_000), true)
  const restored = readAssistantComposerDraft(scope, storage, 10_100)
  assert.equal(restored.content, 'Attach the file')
  assert.deepEqual(restored.contentParts, [attachment])
  assert.equal('attachmentsPending' in restored, false)
})

test('rejects incomplete scopes, malformed drafts, and expired drafts', () => {
  const storage = memoryStorage()
  assert.equal(assistantComposerDraftStorageKey({ ...scope, projectUID: '' }), '')
  assert.equal(writeAssistantComposerDraft({ ...scope, projectUID: '' }, {
    content: 'x', contentParts: [], skillIDs: [], resources: [], responseMode: 'default',
  }, storage), false)

  const key = assistantComposerDraftStorageKey(scope)
  const validEnvelope = () => JSON.parse(JSON.stringify({
    version: 1,
    savedAt: 10_000,
    draft: { content: 'hello', contentParts: [], skillIDs: [], resources: [], responseMode: 'default' },
  }))

  const malformed = validEnvelope()
  malformed.draft.resources = [{ provider: '', resourceRef: {} }]
  storage.setItem(key, JSON.stringify(malformed))
  assert.deepEqual(readAssistantComposerDraft(scope, storage, 10_100).contentParts, [])

  assert.equal(writeAssistantComposerDraft(scope, {
    content: 'hello', contentParts: [], skillIDs: [], resources: [], responseMode: 'default',
  }, storage, 10_000), true)
  assert.equal(readAssistantComposerDraft(scope, storage, 10_000 + ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS + 1).content, '')
})

test('removes an empty default draft and can explicitly clear a saved response mode', () => {
  const storage = memoryStorage()
  const key = assistantComposerDraftStorageKey(scope)
  assert.equal(writeAssistantComposerDraft(scope, {
    content: '', contentParts: [], skillIDs: [], resources: [], responseMode: 'review',
  }, storage), true)
  assert.ok(storage.getItem(key))
  assert.equal(writeAssistantComposerDraft(scope, {
    content: '', contentParts: [], skillIDs: [], resources: [], responseMode: 'default',
  }, storage), true)
  assert.equal(storage.getItem(key), null)

  assert.equal(writeAssistantComposerDraft(scope, {
    content: 'draft', contentParts: [], skillIDs: [], resources: [], responseMode: 'default',
  }, storage), true)
  clearAssistantComposerDraft(scope, storage)
  assert.equal(storage.getItem(key), null)
})
