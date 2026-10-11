import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ server: { middlewareMode: true, hmr: false }, appType: 'custom' })
const draftPolicy = await vite.ssrLoadModule('/src/assistantComposerDraftPolicy.ts')
const {
  ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS,
  assistantComposerDraftStorageKey,
  clearAssistantComposerDraft,
  removeAssistantComposerDraftAttachment,
  purgeAssistantComposerDraftsForIdentity,
  setAssistantComposerDraftActiveIdentity,
  readAssistantComposerDraft,
  transferAssistantComposerDraftToThread,
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

function memoryStorage({ quotaBytes = Infinity } = {}) {
  const values = new Map()
  return {
    values,
    get length() { return values.size },
    key: (index) => [...values.keys()][index] ?? null,
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => {
      let used = 0
      for (const [storedKey, storedValue] of values) {
        if (storedKey !== key) used += (storedKey.length + storedValue.length) * 2
      }
      if (used + (key.length + value.length) * 2 > quotaBytes) {
        throw Object.assign(new Error('quota exceeded'), { name: 'QuotaExceededError' })
      }
      values.set(key, value)
    },
    removeItem: (key) => values.delete(key),
  }
}

function saveStoredDraft(storage, ownerScope, content, savedAt) {
  const key = assistantComposerDraftStorageKey(ownerScope)
  storage.setItem(key, JSON.stringify({
    version: 1,
    savedAt,
    draft: { content, contentParts: [], skillIDs: [], resources: [], responseMode: 'default' },
  }))
  return key
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
  assert.equal(shouldHydrateAssistantComposerDraftForThread('canonical-thread', true, {
    threadID: 'requested-thread', postAccepted: true,
  }), true)
})

test('first-thread drafts and ready receipts transfer from the provisional project scope', () => {
  const storage = memoryStorage()
  const provisional = { ...scope, thread: '' }
  const provisionalKey = assistantComposerDraftStorageKey(provisional)
  const draft = {
    content: 'Send this with the uploaded file',
    contentParts: [{ type: 'text', text: 'Send this with the uploaded file' }, attachment],
    skillIDs: ['sql-review'],
    resources: [resource],
    responseMode: 'plan',
  }
  assert.ok(provisionalKey, 'the no-thread composer receives a project and owner-scoped key')
  const attachmentOnlyDraft = { ...draft, content: '', contentParts: [attachment] }
  assert.equal(writeAssistantComposerDraft(provisional, attachmentOnlyDraft, storage, 9_900), true)
  assert.equal(removeAssistantComposerDraftAttachment(provisionalKey, attachment.attachment.id, storage, 9_950), true)
  assert.deepEqual(readAssistantComposerDraft(provisional, storage, 9_960).contentParts, [], 'late deletion accepts exactly the empty final thread segment')
  assert.equal(writeAssistantComposerDraft(provisional, draft, storage, 10_000), true)
  assert.deepEqual(readAssistantComposerDraft(provisional, storage, 10_100), draft)

  assert.equal(transferAssistantComposerDraftToThread(provisional, scope, storage, 10_200), true)
  assert.equal(storage.getItem(provisionalKey), null, 'the provisional draft cannot reappear after navigation')
  assert.deepEqual(readAssistantComposerDraft(scope, storage, 10_300), draft)
  assert.deepEqual(readAssistantComposerDraft(provisional, storage, 10_300), {
    content: '', contentParts: [], skillIDs: [], resources: [], responseMode: 'default',
  })
  assert.equal(transferAssistantComposerDraftToThread(scope, { ...scope, thread: 'thread-b' }, storage), false)
  assert.equal(transferAssistantComposerDraftToThread(provisional, { ...scope, user: 'user-b' }, storage), false)
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

test('uses a bounded owner-scoped memory fallback when session storage writes fail', () => {
  const storage = memoryStorage()
  storage.setItem = () => { throw new Error('quota exceeded') }
  const ownerScope = { ...scope, thread: 'thread-memory-fallback' }
  const key = assistantComposerDraftStorageKey(ownerScope)
  const draft = {
    content: 'Keep this while I switch threads',
    contentParts: [attachment],
    skillIDs: [],
    resources: [],
    responseMode: 'default',
  }
  assert.equal(writeAssistantComposerDraft(ownerScope, draft, storage, 10_000), true)
  assert.equal(storage.getItem(key), null)
  assert.equal(readAssistantComposerDraft(ownerScope, storage, 10_100).content, draft.content)
  assert.equal(readAssistantComposerDraft({ ...ownerScope, thread: 'thread-other' }, storage, 10_100).content, '')

  // A late attachment deletion must update the fallback entry too, even while
  // the persisted copy is unavailable, so a later successful read cannot
  // resurrect the removed receipt.
  assert.equal(removeAssistantComposerDraftAttachment(key, 'receipt-a', storage, 10_200), true)
  assert.deepEqual(readAssistantComposerDraft(ownerScope, storage, 10_300).contentParts, [])
})

test('bounds the in-memory fallback by evicting its least-recently-used scoped draft', () => {
  for (let index = 0; index < 33; index++) {
    const ownerScope = { ...scope, thread: `thread-fallback-${index}` }
    assert.equal(writeAssistantComposerDraft(ownerScope, {
      content: `draft ${index}`, contentParts: [], skillIDs: [], resources: [], responseMode: 'default',
    }, null, 10_000), true)
  }
  assert.equal(readAssistantComposerDraft({ ...scope, thread: 'thread-fallback-0' }, null, 10_100).content, '')
  assert.equal(readAssistantComposerDraft({ ...scope, thread: 'thread-fallback-32' }, null, 10_100).content, 'draft 32')
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

test('prunes expired and malformed owned entries while leaving other origin data alone', () => {
  const storage = memoryStorage()
  const now = 1_000_000_000
  const expiredKey = saveStoredDraft(storage, { ...scope, thread: 'expired' }, 'old receipt', now - ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS - 1)
  const malformedKey = 'railgrid:app-studio:assistant-composer-draft:v1:broken'
  storage.setItem(malformedKey, '{not json')
  const legacyExpiredKey = saveLegacyDraft(storage, {
    savedAt: now - ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS - 1,
  }, { ...scope, thread: '' })
  const unrelatedKey = 'railgrid:another-provider:settings'
  storage.setItem(unrelatedKey, 'keep this untouched')
  const currentKey = saveStoredDraft(storage, scope, 'current draft', now - 100)

  assert.equal(readAssistantComposerDraft(scope, storage, now).content, 'current draft')
  assert.equal(storage.getItem(expiredKey), null)
  assert.equal(storage.getItem(malformedKey), null)
  assert.equal(storage.getItem(legacyExpiredKey), null)
  assert.ok(storage.getItem(currentKey))
  assert.equal(storage.getItem(unrelatedKey), 'keep this untouched')
})

test('bounds persistent draft count and evicts the oldest scoped entry first', () => {
  const storage = memoryStorage()
  const oldKeys = []
  for (let index = 0; index < 32; index++) {
    oldKeys.push(saveStoredDraft(storage, { ...scope, thread: `stored-${index}` }, `draft ${index}`, 1_000 + index))
  }
  const unrelatedKey = 'railgrid:another-provider:cache'
  storage.setItem(unrelatedKey, 'preserve')
  const current = { ...scope, thread: 'new-current' }
  assert.equal(writeAssistantComposerDraft(current, {
    content: 'newest draft', contentParts: [], skillIDs: [], resources: [], responseMode: 'default',
  }, storage, 2_000), true)

  const ownedKeys = [...storage.values.keys()].filter((key) => key.startsWith('railgrid:app-studio:assistant-composer-draft:v1:'))
  assert.equal(ownedKeys.length, 32)
  assert.equal(storage.getItem(oldKeys[0]), null)
  assert.ok(storage.getItem(oldKeys[31]))
  assert.ok(storage.getItem(assistantComposerDraftStorageKey(current)))
  assert.equal(storage.getItem(unrelatedKey), 'preserve')
})

test('bounds persistent bytes using conservative Web Storage accounting and keeps newest drafts', () => {
  const storage = memoryStorage()
  const oldKeys = []
  for (let index = 0; index < 4; index++) {
    oldKeys.push(saveStoredDraft(storage, { ...scope, thread: `large-${index}` }, 'x'.repeat(400_000), 1_000 + index))
  }
  const current = { ...scope, thread: 'large-current' }
  assert.equal(writeAssistantComposerDraft(current, {
    content: 'x'.repeat(400_000), contentParts: [], skillIDs: [], resources: [], responseMode: 'default',
  }, storage, 2_000), true)

  const ownedKeys = [...storage.values.keys()].filter((key) => key.startsWith('railgrid:app-studio:assistant-composer-draft:v1:'))
  const storedBytes = ownedKeys.reduce((sum, key) => sum + (key.length + storage.getItem(key).length) * 2, 0)
  assert.ok(storedBytes <= 2_800_000, `owned drafts use ${storedBytes} bytes`)
  assert.equal(storage.getItem(oldKeys[0]), null)
  assert.equal(storage.getItem(oldKeys[1]), null)
  assert.ok(storage.getItem(oldKeys[2]))
  assert.ok(storage.getItem(oldKeys[3]))
  assert.ok(storage.getItem(assistantComposerDraftStorageKey(current)))
})

test('a quota-limited session store falls back without deleting unrelated origin data', () => {
  const storage = memoryStorage({ quotaBytes: 1_500_000 })
  const unrelatedKey = 'railgrid:another-provider:large-cache'
  storage.setItem(unrelatedKey, 'u'.repeat(700_000))
  const ownerScope = { ...scope, thread: 'quota-fallback' }
  const currentDraft = {
    content: 'c'.repeat(100_000), contentParts: [attachment], skillIDs: [], resources: [], responseMode: 'default',
  }
  assert.equal(writeAssistantComposerDraft(ownerScope, currentDraft, storage, 10_000), true)
  assert.equal(storage.getItem(assistantComposerDraftStorageKey(ownerScope)), null)
  assert.equal(readAssistantComposerDraft(ownerScope, storage, 10_100).content, currentDraft.content)
  assert.equal(storage.getItem(unrelatedKey), 'u'.repeat(700_000))
})

test('identity purge clears only that user tenant namespace in storage and memory and fences stale operations', () => {
  const storage = memoryStorage()
  const identityA = { tenant: scope.tenant, orgUUID: scope.orgUUID, workspaceUUID: scope.workspaceUUID, user: scope.user }
  const scopeB = { ...scope, user: 'user-b' }
  const identityB = { ...identityA, user: scopeB.user }
  const keyA = saveStoredDraft(storage, scope, 'alice draft', 10_000)
  const keyB = saveStoredDraft(storage, scopeB, 'bob draft', 10_000)
  const legacyScope = { ...scope, thread: '' }
  const legacyKeyA = saveLegacyDraft(storage, { savedAt: 10_000 }, legacyScope)
  const unrelatedKey = 'railgrid:another-provider:setting'
  storage.setItem(unrelatedKey, 'leave me')

  const memoryScopeA = { ...scope, thread: 'memory-a' }
  const memoryScopeB = { ...scopeB, thread: 'memory-b' }
  const draft = { content: 'memory draft', contentParts: [], skillIDs: [], resources: [], responseMode: 'default' }
  assert.equal(writeAssistantComposerDraft(memoryScopeA, draft, null, 10_000), true)
  assert.equal(writeAssistantComposerDraft(memoryScopeB, draft, null, 10_000), true)

  const removed = purgeAssistantComposerDraftsForIdentity(identityA, storage)
  assert.ok(removed >= 3)
  assert.equal(storage.getItem(keyA), null)
  assert.equal(storage.getItem(legacyKeyA), null, 'the provisional empty-thread legacy key is purged by its identity prefix')
  assert.ok(storage.getItem(keyB))
  assert.equal(storage.getItem(unrelatedKey), 'leave me')
  assert.equal(readAssistantComposerDraft(memoryScopeA, null, 10_100).content, '')
  assert.equal(readAssistantComposerDraft(memoryScopeB, null, 10_100).content, 'memory draft')

  try {
    setAssistantComposerDraftActiveIdentity(identityA)
    assert.equal(readAssistantComposerDraft(scopeB, storage, 10_100).content, '')
    assert.equal(writeAssistantComposerDraft(scopeB, draft, storage, 10_100), false)
    setAssistantComposerDraftActiveIdentity(identityB)
    assert.equal(readAssistantComposerDraft(scopeB, storage, 10_100).content, 'bob draft')
    setAssistantComposerDraftActiveIdentity(null)
    assert.equal(readAssistantComposerDraft(scopeB, storage, 10_100).content, '')
    assert.equal(removeAssistantComposerDraftAttachment(keyB, 'receipt-a', storage, 10_100), false)
  } finally {
    setAssistantComposerDraftActiveIdentity(undefined)
  }
})

test('an expired attachment receipt draft is removed before it can be submitted again', () => {
  const storage = memoryStorage()
  const key = saveStoredDraft(storage, scope, '', 10_000)
  const envelope = JSON.parse(storage.getItem(key))
  envelope.draft.contentParts = [attachment]
  storage.setItem(key, JSON.stringify(envelope))
  const expiredAt = 10_000 + ASSISTANT_COMPOSER_DRAFT_MAX_AGE_MS + 1
  assert.deepEqual(readAssistantComposerDraft(scope, storage, expiredAt).contentParts, [])
  assert.equal(storage.getItem(key), null)
})
