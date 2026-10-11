import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ server: { middlewareMode: true, hmr: false }, appType: 'custom' })
const draftPolicy = await vite.ssrLoadModule('/src/assistantComposerDraftPolicy.ts')
const {
  ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS,
  assistantComposerDraftStorageKey,
  clearAssistantComposerDraft,
  readAssistantComposerDraft,
  writeAssistantComposerDraft,
} = await vite.ssrLoadModule('/src/assistantComposerDraft.ts')
const {
  assistantComposerDraftMatches,
  shouldHydrateAssistantComposerDraftForThread,
} = draftPolicy

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
const annotation = {
  type: 'annotation',
  annotation: {
    id: 'annotation-a',
    comment: 'Make this clearer',
    documentID: 'document-a',
    pagePath: '/admin',
    viewport: { width: 1280, height: 720 },
    target: {
      tag: 'h1', name: 'Admin', locator: '#admin-title', locatorStrategy: 'css',
      rect: { x: 12, y: 24, width: 180, height: 40 },
    },
    anchor: { x: 0.25, y: 0.75 },
  },
}
const liveProjectScope = { ...scope, projectCreatedAt: new Date(5_000).toISOString() }

function saveLegacyDraft(storage, overrides = {}, legacyScope = scope, namespace = 'railgrid') {
  const parts = [legacyScope.tenant, legacyScope.orgUUID, legacyScope.workspaceUUID, legacyScope.user, legacyScope.project, legacyScope.thread]
  const key = `${namespace}:app-studio:assistant-annotation-draft:v1:${parts.map(encodeURIComponent).join(':')}`
  storage.setItem(key, JSON.stringify({ version: 1, scope: JSON.stringify(parts), savedAt: 10_000, annotations: [annotation], ...overrides }))
  return key
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

test('first-send thread handoff keeps the in-flight composer state and still hydrates independent navigation', () => {
  const owner = { threadID: 'thread-new', postAccepted: false }
  assert.equal(shouldHydrateAssistantComposerDraftForThread('thread-new', true, owner), false)
  assert.equal(shouldHydrateAssistantComposerDraftForThread('thread-other', true, owner), true)
  assert.equal(shouldHydrateAssistantComposerDraftForThread('thread-new', false, owner), true)
  assert.equal(shouldHydrateAssistantComposerDraftForThread('thread-new', true, { ...owner, postAccepted: true }), true)
})

test('composer draft comparisons include selections, response mode, and structured parts', () => {
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
  assert.equal(assistantComposerDraftMatches(draft, structuredClone(draft)), true)
  assert.equal(assistantComposerDraftMatches(draft, { ...draft, content: 'A newer draft' }), false)
  assert.equal(assistantComposerDraftMatches(draft, { ...draft, content: '', contentParts: [] }, true), true)
  assert.equal(assistantComposerDraftMatches(draft, { ...draft, responseMode: 'review' }, true), false)
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

test('recovers legacy annotations into the exact live Project draft without extending their expiry', () => {
  const storage = memoryStorage()
  const legacyKey = saveLegacyDraft(storage)
  const restored = readAssistantComposerDraft(liveProjectScope, storage, 10_100)
  assert.deepEqual(restored.contentParts, [annotation])
  assert.equal(restored.content, '')
  assert.equal(storage.getItem(legacyKey), null)
  const migrated = JSON.parse(storage.getItem(assistantComposerDraftStorageKey(scope)))
  assert.equal(migrated.savedAt, 10_000)
  assert.deepEqual(readAssistantComposerDraft(scope, storage, 10_200), restored)
  assert.deepEqual(readAssistantComposerDraft(scope, storage, 10_000 + ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS + 1).contentParts, [])
})

test('never adopts a legacy draft into a recreated Project or an unknown Project lifetime', () => {
  const storage = memoryStorage()
  const legacyKey = saveLegacyDraft(storage)
  assert.deepEqual(readAssistantComposerDraft(scope, storage, 10_100).contentParts, [])
  assert.ok(storage.getItem(legacyKey), 'wait for live Project metadata instead of destroying the draft')
  assert.deepEqual(readAssistantComposerDraft({ ...liveProjectScope, projectCreatedAt: 'invalid' }, storage, 10_100).contentParts, [])
  assert.deepEqual(readAssistantComposerDraft({
    ...liveProjectScope, projectUID: 'recreated-project-uid', projectCreatedAt: new Date(10_050).toISOString(),
  }, storage, 10_100).contentParts, [])
  assert.equal(storage.getItem(assistantComposerDraftStorageKey({ ...scope, projectUID: 'recreated-project-uid' })), null)
})

test('recovers pre-rename drafts and chooses the newest valid legacy namespace', () => {
  const storage = memoryStorage()
  const oldKey = saveLegacyDraft(storage, {}, scope, 'faros')
  assert.deepEqual(readAssistantComposerDraft(liveProjectScope, storage, 10_100).contentParts, [annotation])
  assert.equal(storage.getItem(oldKey), null)
  clearAssistantComposerDraft(scope, storage)
  const newerAnnotation = { ...annotation, annotation: { ...annotation.annotation, comment: 'A later draft' } }
  saveLegacyDraft(storage, { savedAt: 10_050, annotations: [newerAnnotation] }, scope, 'faros')
  const renamedKey = saveLegacyDraft(storage)
  assert.deepEqual(readAssistantComposerDraft(liveProjectScope, storage, 10_100).contentParts, [newerAnnotation])
  assert.equal(storage.getItem(oldKey), null)
  assert.equal(storage.getItem(renamedKey), null)
  clearAssistantComposerDraft(scope, storage)
  saveLegacyDraft(storage, {}, scope, 'faros')
  saveLegacyDraft(storage, { scope: 'invalid' })
  assert.deepEqual(readAssistantComposerDraft(liveProjectScope, storage, 10_100).contentParts, [annotation])
})

test('legacy recovery preserves user and thread boundaries and prioritizes an existing composer draft', () => {
  const storage = memoryStorage()
  const legacyKey = saveLegacyDraft(storage)
  for (const change of [{ user: 'another-user' }, { thread: 'another-thread' }, { workspaceUUID: 'another-workspace' }]) {
    assert.deepEqual(readAssistantComposerDraft({ ...liveProjectScope, ...change }, storage, 10_100).contentParts, [])
  }
  assert.ok(storage.getItem(legacyKey))
  const newerDraft = { content: 'Newer draft', contentParts: [], skillIDs: [], resources: [], responseMode: 'plan' }
  assert.equal(writeAssistantComposerDraft(scope, newerDraft, storage, 10_050), true)
  saveLegacyDraft(storage, { savedAt: 10_090 })
  assert.deepEqual(readAssistantComposerDraft(liveProjectScope, storage, 10_100), newerDraft)
  assert.equal(storage.getItem(legacyKey), null, 'reading a current draft retires legacy entries')
  assert.deepEqual(readAssistantComposerDraft(liveProjectScope, storage, 10_050 + ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS + 1).contentParts, [])
})

test('rejects invalid legacy envelopes, expired annotations, duplicate IDs, and oversized drafts', () => {
  for (const overrides of [
    { scope: 'another scope' },
    { savedAt: 10_000 + ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS + 100_000 },
    { savedAt: 4_000 },
    { annotations: [annotation, annotation] },
    { annotations: [{ type: 'text', text: 'not an annotation' }] },
    { annotations: [{ ...annotation, annotation: { id: 'incomplete' } }] },
    { padding: 'x'.repeat(1_100_000) },
    { version: 2 },
  ]) {
    const storage = memoryStorage()
    const key = saveLegacyDraft(storage, overrides)
    assert.deepEqual(readAssistantComposerDraft(liveProjectScope, storage, 10_100).contentParts, [])
    assert.equal(storage.getItem(key), null)
  }
  const storage = memoryStorage()
  const key = saveLegacyDraft(storage)
  assert.deepEqual(readAssistantComposerDraft(liveProjectScope, storage, 10_000 + ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS + 1).contentParts, [])
  assert.equal(storage.getItem(key), null)
})

test('retains recoverable legacy annotations when persistence is unavailable', () => {
  const storage = memoryStorage()
  const key = saveLegacyDraft(storage)
  storage.setItem = () => { throw new Error('quota exceeded') }
  assert.deepEqual(readAssistantComposerDraft(liveProjectScope, storage, 10_100).contentParts, [annotation])
  assert.ok(storage.getItem(key))
})

test('clearing or replacing a draft retires legacy annotations so they cannot reappear', () => {
  const storage = memoryStorage()
  const key = saveLegacyDraft(storage)
  clearAssistantComposerDraft(scope, storage)
  assert.equal(storage.getItem(key), null)
  saveLegacyDraft(storage)
  assert.equal(writeAssistantComposerDraft(scope, { content: '', contentParts: [], skillIDs: [], resources: [], responseMode: 'default' }, storage, 10_100), true)
  assert.equal(storage.getItem(key), null)
  assert.deepEqual(readAssistantComposerDraft(liveProjectScope, storage, 10_200).contentParts, [])
})
