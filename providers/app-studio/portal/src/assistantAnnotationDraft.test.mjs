import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ server: { middlewareMode: true, hmr: false }, appType: 'custom' })
const {
  ASSISTANT_ANNOTATION_DRAFT_MAX_AGE_MS,
  ASSISTANT_ANNOTATION_DRAFT_MAX_BYTES,
  assistantAnnotationDraftStorageKey,
  clearAssistantAnnotationDraft,
  readAssistantAnnotationDraft,
  writeAssistantAnnotationDraft,
} = await vite.ssrLoadModule('/src/assistantAnnotationDraft.ts')

test.after(async () => vite.close())

const scope = {
  tenant: 'tenant-a',
  orgUUID: 'org-a',
  workspaceUUID: 'workspace-a',
  user: 'user-a',
  project: 'project-a',
  thread: 'thread-a',
}
const annotation = {
  type: 'annotation',
  annotation: {
    id: 'annotation-a',
    comment: 'Make this clearer',
    documentID: 'document-a',
    pagePath: '/admin',
    viewport: { width: 1280, height: 720 },
    target: {
      tag: 'h1',
      name: 'Admin',
      locator: '#admin-title',
      locatorStrategy: 'css',
      rect: { x: 12, y: 24, width: 180, height: 40 },
    },
    anchor: { x: 0.25, y: 0.75 },
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

test('round trips only validated annotation parts for the exact project and thread scope', () => {
  const storage = memoryStorage()
  assert.equal(writeAssistantAnnotationDraft(scope, [
    { type: 'text', text: 'not persisted' },
    annotation,
  ], storage, 10_000), true)
  assert.deepEqual(readAssistantAnnotationDraft(scope, storage, 10_100), [annotation])
  assert.deepEqual(readAssistantAnnotationDraft({ ...scope, thread: 'thread-b' }, storage, 10_100), [])
  assert.deepEqual(readAssistantAnnotationDraft({ ...scope, user: 'user-b' }, storage, 10_100), [])
})

test('rejects missing scope, cross-scope envelopes, malformed annotations, duplicates, and expired drafts', () => {
  const storage = memoryStorage()
  assert.equal(assistantAnnotationDraftStorageKey({ ...scope, tenant: '' }), '')
  assert.equal(assistantAnnotationDraftStorageKey({ ...scope, tenant: 'x'.repeat(513) }), '')
  assert.equal(writeAssistantAnnotationDraft({ ...scope, tenant: '' }, [annotation], storage), false)

  const key = assistantAnnotationDraftStorageKey(scope)
  const validRaw = () => JSON.parse(JSON.stringify({
    version: 1,
    scope: JSON.stringify([scope.tenant, scope.orgUUID, scope.workspaceUUID, scope.user, scope.project, scope.thread]),
    savedAt: 10_000,
    annotations: [annotation],
  }))

  const crossScope = validRaw()
  crossScope.scope = 'different'
  storage.setItem(key, JSON.stringify(crossScope))
  assert.deepEqual(readAssistantAnnotationDraft(scope, storage, 10_100), [])
  assert.equal(storage.getItem(key), null)

  const malformed = validRaw()
  malformed.annotations[0].annotation.comment = ''
  storage.setItem(key, JSON.stringify(malformed))
  assert.deepEqual(readAssistantAnnotationDraft(scope, storage, 10_100), [])

  const invalidAnchor = validRaw()
  invalidAnchor.annotations[0].annotation.anchor.x = 1.01
  storage.setItem(key, JSON.stringify(invalidAnchor))
  assert.deepEqual(readAssistantAnnotationDraft(scope, storage, 10_100), [])

  const duplicate = validRaw()
  duplicate.annotations.push(duplicate.annotations[0])
  storage.setItem(key, JSON.stringify(duplicate))
  assert.deepEqual(readAssistantAnnotationDraft(scope, storage, 10_100), [])

  assert.equal(writeAssistantAnnotationDraft(scope, [annotation], storage, 10_000), true)
  assert.deepEqual(readAssistantAnnotationDraft(scope, storage, 10_000 + ASSISTANT_ANNOTATION_DRAFT_MAX_AGE_MS + 1), [])

  storage.setItem(key, 'x'.repeat(ASSISTANT_ANNOTATION_DRAFT_MAX_BYTES + 1))
  assert.deepEqual(readAssistantAnnotationDraft(scope, storage, 10_100), [])
  assert.equal(storage.getItem(key), null)
})

test('removes storage when annotations are cleared', () => {
  const storage = memoryStorage()
  assert.equal(writeAssistantAnnotationDraft(scope, [annotation], storage), true)
  assert.ok(storage.getItem(assistantAnnotationDraftStorageKey(scope)))
  assert.equal(writeAssistantAnnotationDraft(scope, [{ type: 'text', text: 'keep composing' }], storage), true)
  assert.equal(storage.getItem(assistantAnnotationDraftStorageKey(scope)), null)
  assert.equal(writeAssistantAnnotationDraft(scope, [annotation], storage), true)
  clearAssistantAnnotationDraft(scope, storage)
  assert.equal(storage.getItem(assistantAnnotationDraftStorageKey(scope)), null)
})
