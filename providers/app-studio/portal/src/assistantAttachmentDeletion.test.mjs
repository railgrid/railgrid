import assert from 'node:assert/strict'
import {fileURLToPath} from 'node:url'
import path from 'node:path'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  root: path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..'),
  server: { middlewareMode: true, hmr: false, ws: false },
  appType: 'custom',
})
const draftModule = await vite.ssrLoadModule('/src/assistantComposerDraft.ts')
const deletionModule = await vite.ssrLoadModule('/src/assistantAttachmentDeletion.ts')
const {
  assistantComposerDraftStorageKey,
  readAssistantComposerDraft,
  removeAssistantComposerDraftAttachment,
  writeAssistantComposerDraft,
} = draftModule
const {reconcileAssistantAttachmentChips, removeScopedAssistantAttachment, shouldCleanupAssistantAttachmentOnScopeSwitch} = deletionModule

test.after(async () => vite.close())

const ownerThread = {
  tenant: 'tenant-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a', user: 'user-a',
  project: 'project-a', projectUID: 'project-uid-a', thread: 'thread-a',
}
const otherThread = {...ownerThread, thread: 'thread-b'}
const otherProject = {...ownerThread, project: 'project-b', projectUID: 'project-uid-b'}
const receipt = {
  type: 'attachment',
  attachment: {
    id: 'receipt-owner', filename: 'report.txt', contentType: 'text/plain', sizeBytes: 8,
    sha256: 'a'.repeat(64), createdAt: new Date(10_000).toISOString(),
  },
}
const otherReceipt = {
  type: 'attachment',
  attachment: {
    ...receipt.attachment, id: 'receipt-other', filename: 'other.txt', sha256: 'b'.repeat(64),
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

function draft(content, contentParts) {
  return {content, contentParts, skillIDs: [], resources: [], responseMode: 'default'}
}

function deferred() {
  let resolve
  let reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return {promise, resolve, reject}
}

test('late thread-switch success removes only the receipt from its captured owner draft', async () => {
  const storage = memoryStorage()
  const ownerKey = assistantComposerDraftStorageKey(ownerThread)
  assert.equal(writeAssistantComposerDraft(ownerThread, draft('owner text', [{type: 'text', text: 'owner text'}, receipt]), storage, 10_000), true)
  assert.equal(writeAssistantComposerDraft(otherThread, draft('other text', [otherReceipt]), storage, 10_000), true)

  const deletion = deferred()
  let activeScope = ownerThread
  let activeMutationCount = 0
  let successScopeWasActive
  let failure
  const pending = removeScopedAssistantAttachment({
    deleteAttachment: () => deletion.promise,
    isNotFoundError: () => false,
    isOwnerScopeCurrent: () => activeScope === ownerThread,
    removeCurrentReceipt: () => { activeMutationCount += 1 },
    removeOwnerDraftReceipt: () => removeAssistantComposerDraftAttachment(ownerKey, receipt.attachment.id, storage, 20_000),
    onFailure: (error) => { failure = error },
    onSuccess: (ownerScopeIsCurrent) => { successScopeWasActive = ownerScopeIsCurrent },
  })
  activeScope = otherThread
  const latestOwnerDraft = draft('new owner text', [
    {type: 'text', text: 'new owner text'}, otherReceipt, receipt,
  ])
  const latestOtherDraft = draft('new current-thread text', [
    {type: 'text', text: 'new current-thread text'}, otherReceipt,
  ])
  assert.equal(writeAssistantComposerDraft(ownerThread, latestOwnerDraft, storage, 15_000), true)
  assert.equal(writeAssistantComposerDraft(otherThread, latestOtherDraft, storage, 15_000), true)
  deletion.resolve()

  assert.equal(await pending, true)
  assert.equal(failure, undefined)
  assert.equal(activeMutationCount, 0)
  assert.equal(successScopeWasActive, false)
  assert.deepEqual(readAssistantComposerDraft(ownerThread, storage, 20_100), draft('new owner text', [
    {type: 'text', text: 'new owner text'}, otherReceipt,
  ]))
  assert.deepEqual(readAssistantComposerDraft(otherThread, storage, 20_100), latestOtherDraft)
  activeScope = ownerThread
  assert.deepEqual(readAssistantComposerDraft(activeScope, storage, 20_100), draft('new owner text', [
    {type: 'text', text: 'new owner text'}, otherReceipt,
  ]))
})

test('late project-switch success updates the captured project draft and leaves active project data alone', async () => {
  const storage = memoryStorage()
  const ownerKey = assistantComposerDraftStorageKey(ownerThread)
  assert.equal(writeAssistantComposerDraft(ownerThread, draft('project A notes', [receipt]), storage, 10_000), true)
  assert.equal(writeAssistantComposerDraft(otherProject, draft('project B notes', [otherReceipt]), storage, 10_000), true)

  const deletion = deferred()
  let activeScope = ownerThread
  let activeMutationCount = 0
  let failure
  const pending = removeScopedAssistantAttachment({
    deleteAttachment: () => deletion.promise,
    isNotFoundError: () => false,
    isOwnerScopeCurrent: () => activeScope === ownerThread,
    removeCurrentReceipt: () => { activeMutationCount += 1 },
    removeOwnerDraftReceipt: () => removeAssistantComposerDraftAttachment(ownerKey, receipt.attachment.id, storage, 30_000),
    onFailure: (error) => { failure = error },
    onSuccess: () => {},
  })
  activeScope = otherProject
  const latestOwnerDraft = draft('new project A notes', [
    {type: 'text', text: 'new project A notes'}, otherReceipt, receipt,
  ])
  const latestOtherDraft = draft('new project B notes', [
    {type: 'text', text: 'new project B notes'}, otherReceipt,
  ])
  assert.equal(writeAssistantComposerDraft(ownerThread, latestOwnerDraft, storage, 25_000), true)
  assert.equal(writeAssistantComposerDraft(otherProject, latestOtherDraft, storage, 25_000), true)
  deletion.resolve()

  assert.equal(await pending, true)
  assert.equal(failure, undefined)
  assert.equal(activeMutationCount, 0)
  assert.deepEqual(readAssistantComposerDraft(ownerThread, storage, 30_100), draft('new project A notes', [
    {type: 'text', text: 'new project A notes'}, otherReceipt,
  ]))
  assert.deepEqual(readAssistantComposerDraft(otherProject, storage, 30_100), latestOtherDraft)
})

test('active-scope success updates live parts without a storage-key write', async () => {
  const deletion = deferred()
  let activeScope = ownerThread
  let currentParts = [
    {type: 'text', text: 'initial owner text'}, receipt,
  ]
  let storedMutationCount = 0
  let successScopeWasActive
  const pending = removeScopedAssistantAttachment({
    deleteAttachment: () => deletion.promise,
    isNotFoundError: () => false,
    isOwnerScopeCurrent: () => activeScope === ownerThread,
    removeCurrentReceipt: () => { currentParts = currentParts.filter((part) => part.type !== 'attachment' || part.attachment.id !== receipt.attachment.id) },
    removeOwnerDraftReceipt: () => { storedMutationCount += 1; return true },
    onFailure: (error) => { throw error },
    onSuccess: (ownerScopeIsCurrent) => { successScopeWasActive = ownerScopeIsCurrent },
  })
  activeScope = otherThread
  activeScope = ownerThread
  currentParts = [{type: 'text', text: 'new owner text'}, receipt, otherReceipt]
  deletion.resolve()

  assert.equal(await pending, true)
  assert.deepEqual(currentParts, [{type: 'text', text: 'new owner text'}, otherReceipt])
  assert.equal(storedMutationCount, 0)
  assert.equal(successScopeWasActive, true)
})

test('failed delete across thread and project switches retains a retry path and owner draft', async () => {
  for (const switchedScope of [otherThread, otherProject]) {
    const storage = memoryStorage()
    const ownerKey = assistantComposerDraftStorageKey(ownerThread)
    const ownerDraft = draft('keep this owner text', [{type: 'text', text: 'keep this owner text'}, receipt])
    assert.equal(writeAssistantComposerDraft(ownerThread, ownerDraft, storage, 10_000), true)

    const chip = {status: 'deleting', retryAction: undefined}
    let activeScope = ownerThread
    let activeMutationCount = 0
    let storedMutationCount = 0
    let successCount = 0
    const error = Object.assign(new Error('temporary network failure'), {status: 503})
    const pending = removeScopedAssistantAttachment({
      deleteAttachment: async () => { throw error },
      isNotFoundError: (candidate) => candidate?.status === 404,
      isOwnerScopeCurrent: () => activeScope === ownerThread,
      removeCurrentReceipt: () => { activeMutationCount += 1 },
      removeOwnerDraftReceipt: () => { storedMutationCount += 1; return removeAssistantComposerDraftAttachment(ownerKey, receipt.attachment.id, storage, 40_000) },
      onFailure: (candidate) => { chip.status = 'error'; chip.retryAction = 'delete'; chip.error = candidate.message },
      onSuccess: () => { successCount += 1 },
    })
    activeScope = switchedScope

    assert.equal(await pending, false)
    assert.equal(chip.status, 'error')
    assert.equal(chip.retryAction, 'delete')
    assert.equal(chip.error, 'temporary network failure')
    assert.equal(activeMutationCount, 0)
    assert.equal(storedMutationCount, 0)
    assert.equal(successCount, 0)
    assert.equal(shouldCleanupAssistantAttachmentOnScopeSwitch(chip.status, chip.retryAction), false)
    assert.equal(shouldCleanupAssistantAttachmentOnScopeSwitch('deleting'), false)
    assert.equal(shouldCleanupAssistantAttachmentOnScopeSwitch('error', 'upload'), true)
    assert.deepEqual(readAssistantComposerDraft(ownerThread, storage, 40_100), ownerDraft)

    activeScope = ownerThread
    let ownerParts = ownerDraft.contentParts
    const retried = await removeScopedAssistantAttachment({
      deleteAttachment: async () => {},
      isNotFoundError: () => false,
      isOwnerScopeCurrent: () => activeScope === ownerThread,
      removeCurrentReceipt: () => {
        ownerParts = ownerParts.filter((part) => part.type !== 'attachment' || part.attachment.id !== receipt.attachment.id)
        assert.equal(writeAssistantComposerDraft(ownerThread, {...ownerDraft, contentParts: ownerParts}, storage, 41_000), true)
      },
      removeOwnerDraftReceipt: () => assert.fail('active retry should update current owner state'),
      onFailure: (candidate) => { throw candidate },
      onSuccess: () => { successCount += 1 },
    })
    assert.equal(retried, true)
    assert.equal(successCount, 1)
    assert.deepEqual(readAssistantComposerDraft(ownerThread, storage, 41_100), draft('keep this owner text', [{type: 'text', text: 'keep this owner text'}]))
  }
})

test('chip reconciliation retains the owner chip across a pre-settlement switch and failure', async () => {
  const chip = {
    clientID: 'receipt:receipt-owner', receipt: receipt.attachment,
    projectName: ownerThread.project, draftScopeKey: 'owner-scope', status: 'ready',
  }
  let chips = [chip]
  const deletion = deferred()
  let activeScope = 'owner-scope'
  let failure
  chip.status = 'deleting'
  const pending = removeScopedAssistantAttachment({
    deleteAttachment: () => deletion.promise,
    isNotFoundError: () => false,
    isOwnerScopeCurrent: () => activeScope === 'owner-scope',
    removeCurrentReceipt: () => assert.fail('scope switched before completion'),
    removeOwnerDraftReceipt: () => assert.fail('failed delete must preserve the owner receipt'),
    onFailure: (error) => { chip.status = 'error'; chip.retryAction = 'delete'; chip.error = error.message; failure = error },
    onSuccess: () => assert.fail('failed delete cannot report success'),
  })

  activeScope = 'other-scope'
  chips = reconcileAssistantAttachmentChips(chips, activeScope, new Set())
  assert.deepEqual(chips, [chip], 'pending owner chip must survive while hidden on another thread')
  assert.deepEqual(chips.filter((candidate) => candidate.draftScopeKey === activeScope), [])

  activeScope = 'owner-scope'
  chips = reconcileAssistantAttachmentChips(chips, activeScope, new Set([receipt.attachment.id]))
  assert.deepEqual(chips, [chip], 'returning before DELETE settles must reuse the in-flight owner chip')
  assert.equal(chips[0].status, 'deleting')

  activeScope = 'other-scope'
  deletion.reject(new Error('temporary network failure'))
  assert.equal(await pending, false)
  assert.match(failure.message, /temporary network failure/)
  assert.equal(chip.retryAction, 'delete')
  chips = reconcileAssistantAttachmentChips(chips, activeScope, new Set())
  assert.deepEqual(chips, [chip], 'failed owner chip must survive the inactive-scope reconciliation')

  activeScope = 'owner-scope'
  chips = reconcileAssistantAttachmentChips(chips, activeScope, new Set([receipt.attachment.id]))
  assert.deepEqual(chips, [chip], 'returning after failure must restore the same chip with its retry action')
  assert.equal(chips[0].status, 'error')
  assert.equal(chips[0].retryAction, 'delete')
})

test('already-missing server receipt still clears only its saved owner draft', async () => {
  const storage = memoryStorage()
  const ownerKey = assistantComposerDraftStorageKey(ownerThread)
  assert.equal(writeAssistantComposerDraft(ownerThread, draft('owner text', [receipt]), storage, 10_000), true)
  const notFound = Object.assign(new Error('missing'), {status: 404})
  let successCount = 0
  const result = await removeScopedAssistantAttachment({
    deleteAttachment: async () => { throw notFound },
    isNotFoundError: (error) => error?.status === 404,
    isOwnerScopeCurrent: () => false,
    removeCurrentReceipt: () => assert.fail('inactive owner scope must not mutate current parts'),
    removeOwnerDraftReceipt: () => removeAssistantComposerDraftAttachment(ownerKey, receipt.attachment.id, storage, 50_000),
    onFailure: (error) => { throw error },
    onSuccess: () => { successCount += 1 },
  })

  assert.equal(result, true)
  assert.equal(successCount, 1)
  assert.deepEqual(readAssistantComposerDraft(ownerThread, storage, 50_100), draft('owner text', []))
})

test('owner-keyed storage removal preserves every other draft field and rejects unrelated keys', () => {
  const storage = memoryStorage()
  const key = assistantComposerDraftStorageKey(ownerThread)
  const value = {
    content: 'keep this text',
    contentParts: [{type: 'text', text: 'keep this text'}, receipt],
    skillIDs: ['sql-review'],
    resources: [],
    responseMode: 'plan',
  }
  assert.equal(writeAssistantComposerDraft(ownerThread, value, storage, 10_000), true)
  assert.equal(removeAssistantComposerDraftAttachment(key, receipt.attachment.id, storage, 60_000), true)
  assert.deepEqual(readAssistantComposerDraft(ownerThread, storage, 60_100), {
    ...value,
    contentParts: [{type: 'text', text: 'keep this text'}],
  })
  const before = storage.getItem(key)
  assert.equal(removeAssistantComposerDraftAttachment('unrelated:key', receipt.attachment.id, storage, 60_200), false)
  assert.equal(storage.getItem(key), before)
})
