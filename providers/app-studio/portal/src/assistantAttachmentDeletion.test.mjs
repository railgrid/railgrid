import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
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
const {
  bindAssistantAttachmentChipsToDraftScope,
  commitScopedAssistantAttachmentChips,
  reconcileAssistantAttachmentChips,
  removeScopedAssistantAttachment,
  shouldCleanupAssistantAttachmentOnScopeSwitch,
} = deletionModule

test.after(async () => vite.close())

const ownerScope = {
  tenant: 'tenant-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a', user: 'user-a',
  project: 'project-a', projectUID: 'project-uid-a', thread: 'thread-a',
}
const otherThread = { ...ownerScope, thread: 'thread-b' }
const otherProject = { ...ownerScope, project: 'project-b', projectUID: 'project-uid-b' }
const receiptPart = {
  type: 'attachment',
  attachment: {
    id: 'receipt-owner', filename: 'report.txt', contentType: 'text/plain', sizeBytes: 8,
    sha256: 'a'.repeat(64), createdAt: new Date(10_000).toISOString(),
  },
}
const otherReceiptPart = {
  type: 'attachment',
  attachment: { ...receiptPart.attachment, id: 'receipt-other', filename: 'other.txt', sha256: 'b'.repeat(64) },
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

function draft(content, contentParts, overrides = {}) {
  return { content, contentParts, skillIDs: [], resources: [], responseMode: 'default', ...overrides }
}

function deferred() {
  let resolve
  let reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

test('late delete updates the latest owner draft and leaves the active thread untouched', async () => {
  const storage = memoryStorage()
  const ownerKey = assistantComposerDraftStorageKey(ownerScope)
  assert.equal(writeAssistantComposerDraft(ownerScope, draft('owner text', [{ type: 'text', text: 'owner text' }, receiptPart]), storage, 10_000), true)
  assert.equal(writeAssistantComposerDraft(otherThread, draft('other text', [otherReceiptPart]), storage, 10_000), true)

  const deletion = deferred()
  let activeScope = ownerScope
  let currentParts = [{ type: 'text', text: 'owner text' }, receiptPart]
  let storedPartsMutationCount = 0
  let successScopeWasActive
  const pending = removeScopedAssistantAttachment({
    deleteAttachment: () => deletion.promise,
    isNotFoundError: () => false,
    isOwnerScopeCurrent: () => activeScope === ownerScope,
    removeCurrentReceipt: () => { currentParts = currentParts.filter((part) => part.type !== 'attachment' || part.attachment.id !== receiptPart.attachment.id) },
    removeOwnerDraftReceipt: () => {
      storedPartsMutationCount += 1
      return removeAssistantComposerDraftAttachment(ownerKey, receiptPart.attachment.id, storage, 20_000)
    },
    onFailure: (error) => { throw error },
    onSuccess: (ownerScopeIsCurrent) => { successScopeWasActive = ownerScopeIsCurrent },
  })

  activeScope = otherThread
  currentParts = draft('other text', [otherReceiptPart]).contentParts
  const latestOwnerDraft = draft('new owner text', [
    { type: 'text', text: 'new owner text' }, otherReceiptPart, receiptPart,
  ], { skillIDs: ['review'], responseMode: 'plan' })
  const latestOtherDraft = draft('new current-thread text', [{ type: 'text', text: 'new current-thread text' }, otherReceiptPart])
  assert.equal(writeAssistantComposerDraft(ownerScope, latestOwnerDraft, storage, 15_000), true)
  assert.equal(writeAssistantComposerDraft(otherThread, latestOtherDraft, storage, 15_000), true)
  currentParts = latestOtherDraft.contentParts
  deletion.resolve()

  assert.equal(await pending, true)
  assert.equal(successScopeWasActive, false)
  assert.equal(storedPartsMutationCount, 1)
  assert.deepEqual(currentParts, latestOtherDraft.contentParts)
  assert.deepEqual(readAssistantComposerDraft(ownerScope, storage, 20_100), draft('new owner text', [
    { type: 'text', text: 'new owner text' }, otherReceiptPart,
  ], { skillIDs: ['review'], responseMode: 'plan' }))
  assert.deepEqual(readAssistantComposerDraft(otherThread, storage, 20_100), latestOtherDraft)
})

test('active deletion updates live parts and does not perform a second storage write', async () => {
  let currentParts = [{ type: 'text', text: 'keep this' }, receiptPart, otherReceiptPart]
  let storedMutationCount = 0
  let successScopeWasActive
  const result = await removeScopedAssistantAttachment({
    deleteAttachment: async () => {},
    isNotFoundError: () => false,
    isOwnerScopeCurrent: () => true,
    removeCurrentReceipt: () => { currentParts = currentParts.filter((part) => part.type !== 'attachment' || part.attachment.id !== receiptPart.attachment.id) },
    removeOwnerDraftReceipt: () => { storedMutationCount += 1; return true },
    onFailure: (error) => { throw error },
    onSuccess: (ownerScopeIsCurrent) => { successScopeWasActive = ownerScopeIsCurrent },
  })

  assert.equal(result, true)
  assert.equal(successScopeWasActive, true)
  assert.equal(storedMutationCount, 0)
  assert.deepEqual(currentParts, [{ type: 'text', text: 'keep this' }, otherReceiptPart])
})

test('failed deletion preserves owner draft and retry state across thread and project switches', async () => {
  const storage = memoryStorage()
  const ownerKey = assistantComposerDraftStorageKey(ownerScope)
  const ownerDraft = draft('keep this owner text', [{ type: 'text', text: 'keep this owner text' }, receiptPart])
  assert.equal(writeAssistantComposerDraft(ownerScope, ownerDraft, storage, 10_000), true)
  const chip = { status: 'deleting', retryAction: undefined }
  let activeScope = ownerScope
  let currentMutationCount = 0
  let storedMutationCount = 0
  let successCount = 0
  const error = Object.assign(new Error('temporary network failure'), { status: 503 })
  const deletion = deferred()
  const pending = removeScopedAssistantAttachment({
    deleteAttachment: () => deletion.promise,
    isNotFoundError: (candidate) => candidate?.status === 404,
    isOwnerScopeCurrent: () => activeScope === ownerScope,
    removeCurrentReceipt: () => { currentMutationCount += 1 },
    removeOwnerDraftReceipt: () => { storedMutationCount += 1; return removeAssistantComposerDraftAttachment(ownerKey, receiptPart.attachment.id, storage, 40_000) },
    onFailure: (candidate) => { chip.status = 'error'; chip.retryAction = 'delete'; chip.error = candidate.message },
    onSuccess: () => { successCount += 1 },
  })
  activeScope = otherProject
  deletion.reject(error)
  const result = await pending

  assert.equal(result, false)
  assert.equal(chip.status, 'error')
  assert.equal(chip.retryAction, 'delete')
  assert.match(chip.error, /temporary network failure/)
  assert.equal(currentMutationCount, 0)
  assert.equal(storedMutationCount, 0)
  assert.equal(successCount, 0)
  assert.equal(shouldCleanupAssistantAttachmentOnScopeSwitch(chip.status, chip.retryAction), false)
  assert.deepEqual(readAssistantComposerDraft(ownerScope, storage, 40_100), ownerDraft)
})

test('404 is idempotent and removes only the saved owner receipt after navigation', async () => {
  const storage = memoryStorage()
  const ownerKey = assistantComposerDraftStorageKey(ownerScope)
  assert.equal(writeAssistantComposerDraft(ownerScope, draft('owner text', [receiptPart, otherReceiptPart]), storage, 10_000), true)
  const missing = Object.assign(new Error('not found'), { status: 404 })
  let activeMutationCount = 0
  const result = await removeScopedAssistantAttachment({
    deleteAttachment: async () => { throw missing },
    isNotFoundError: (error) => error?.status === 404,
    isOwnerScopeCurrent: () => false,
    removeCurrentReceipt: () => { activeMutationCount += 1 },
    removeOwnerDraftReceipt: () => removeAssistantComposerDraftAttachment(ownerKey, receiptPart.attachment.id, storage, 50_000),
    onFailure: (error) => { throw error },
    onSuccess: () => {},
  })

  assert.equal(result, true)
  assert.equal(activeMutationCount, 0)
  assert.deepEqual(readAssistantComposerDraft(ownerScope, storage, 50_100), draft('owner text', [otherReceiptPart]))
})

test('late delete completion updates the owner draft but skips UI callbacks after unmount', async () => {
  const deletion = deferred()
  let mounted = true
  let ownerDraftUpdated = false
  let uiCallbackCount = 0
  const pending = removeScopedAssistantAttachment({
    deleteAttachment: () => deletion.promise,
    isNotFoundError: () => false,
    isOwnerScopeCurrent: () => mounted,
    shouldUpdateComposer: () => mounted,
    removeCurrentReceipt: () => { uiCallbackCount += 1 },
    removeOwnerDraftReceipt: () => { ownerDraftUpdated = true; return true },
    onFailure: () => { uiCallbackCount += 1 },
    onSuccess: () => { uiCallbackCount += 1 },
  })
  mounted = false
  deletion.resolve()

  assert.equal(await pending, true)
  assert.equal(ownerDraftUpdated, true)
  assert.equal(uiCallbackCount, 0)

  const failure = await removeScopedAssistantAttachment({
    deleteAttachment: async () => { throw new Error('offline') },
    isNotFoundError: () => false,
    isOwnerScopeCurrent: () => mounted,
    shouldUpdateComposer: () => mounted,
    removeCurrentReceipt: () => { uiCallbackCount += 1 },
    removeOwnerDraftReceipt: () => true,
    onFailure: () => { uiCallbackCount += 1 },
  })
  assert.equal(failure, false)
  assert.equal(uiCallbackCount, 0)
})

test('scope reconciliation keeps only current receipts and owner-bound delete states', () => {
  const chips = [
    { clientID: 'current', draftScopeKey: 'active', status: 'ready', receipt: { id: 'kept' } },
    { clientID: 'stale-ready', draftScopeKey: 'active', status: 'ready', receipt: { id: 'stale' } },
    { clientID: 'owner-ready', draftScopeKey: 'owner', status: 'ready', receipt: { id: 'hidden' } },
    { clientID: 'owner-deleting', draftScopeKey: 'owner', status: 'deleting' },
    { clientID: 'owner-delete-error', draftScopeKey: 'owner', status: 'error', retryAction: 'delete' },
    { clientID: 'owner-upload-error', draftScopeKey: 'owner', status: 'error', retryAction: 'upload' },
  ]
  assert.deepEqual(
    reconcileAssistantAttachmentChips(chips, 'active', new Set(['kept'])).map((chip) => chip.clientID),
    ['current', 'owner-ready', 'owner-deleting', 'owner-delete-error'],
  )
  assert.equal(shouldCleanupAssistantAttachmentOnScopeSwitch('deleting'), false)
  assert.equal(shouldCleanupAssistantAttachmentOnScopeSwitch('error', 'delete'), false)
  assert.equal(shouldCleanupAssistantAttachmentOnScopeSwitch('error', 'upload'), true)
  assert.equal(shouldCleanupAssistantAttachmentOnScopeSwitch('ready', undefined, false), true)
})

test('first-send scope adoption binds only selected ready receipts from the matching project', () => {
  const chips = [
    { clientID: 'selected', projectName: 'project-a', draftScopeKey: '', status: 'ready', receipt: { id: 'receipt-selected' } },
    { clientID: 'other-project', projectName: 'project-b', draftScopeKey: '', status: 'ready', receipt: { id: 'receipt-other-project' } },
    { clientID: 'unselected', projectName: 'project-a', draftScopeKey: '', status: 'ready', receipt: { id: 'receipt-unselected' } },
    { clientID: 'committed', projectName: 'project-a', draftScopeKey: '', status: 'ready', committed: true, receipt: { id: 'receipt-committed' } },
    { clientID: 'uploading', projectName: 'project-a', draftScopeKey: '', status: 'uploading' },
  ]
  const adopted = bindAssistantAttachmentChipsToDraftScope(
    chips, '', 'thread-a-draft-key', 'project-a', ['receipt-selected', 'receipt-committed'],
  )
  assert.equal(adopted[0].draftScopeKey, 'thread-a-draft-key')
  assert.equal(adopted[1].draftScopeKey, '')
  assert.equal(adopted[2].draftScopeKey, '')
  assert.equal(adopted[3].draftScopeKey, '')
  assert.equal(adopted[4].draftScopeKey, '')
})

test('accepted receipt commits require the exact owner, project, and receipt ID', () => {
  const chips = [
    { clientID: 'owner-match', projectName: 'project-a', draftScopeKey: 'scope-thread-a', status: 'ready', receipt: { id: 'receipt-a' } },
    { clientID: 'owner-near-match', projectName: 'project-a', draftScopeKey: 'scope-thread-a', status: 'ready', receipt: { id: 'receipt-a-extra' } },
    { clientID: 'wrong-project', projectName: 'project-b', draftScopeKey: 'scope-thread-a', status: 'ready', receipt: { id: 'receipt-a' } },
    { clientID: 'wrong-thread', projectName: 'project-a', draftScopeKey: 'scope-thread-b', status: 'ready', receipt: { id: 'receipt-a' } },
  ]
  const accepted = commitScopedAssistantAttachmentChips(chips, 'scope-thread-a', 'project-a', [' receipt-a '])
  assert.deepEqual(accepted.map((chip) => Boolean(chip.committed)), [true, false, false, false])
})

test('delayed acceptance commits only its captured owner after navigation', () => {
  const ownerScopeKey = 'scope-thread-a'
  const activeScopeKey = 'scope-thread-b'
  const chips = [
    { clientID: 'owner-receipt', projectName: 'project-a', draftScopeKey: ownerScopeKey, status: 'ready', receipt: { id: 'receipt-shared' } },
    { clientID: 'other-thread-receipt', projectName: 'project-a', draftScopeKey: activeScopeKey, status: 'ready', receipt: { id: 'receipt-shared' } },
  ]
  const accepted = commitScopedAssistantAttachmentChips(chips, ownerScopeKey, 'project-a', ['receipt-shared'])
  assert.deepEqual(accepted.map((chip) => chip.committed), [true, undefined])

  let deleteCount = 0
  const returnedOwner = reconcileAssistantAttachmentChips(accepted, ownerScopeKey, new Set())
  for (const chip of accepted) {
    if (chip.draftScopeKey === ownerScopeKey && chip.status === 'ready' && !chip.committed && !returnedOwner.some((candidate) => candidate.clientID === chip.clientID)) {
      deleteCount += 1
    }
  }
  assert.deepEqual(returnedOwner.map((chip) => chip.clientID), ['other-thread-receipt'])
  assert.equal(deleteCount, 0, 'an accepted owner receipt leaves the chip projection without issuing DELETE')
})

test('owner draft removal preserves unrelated fields and refuses unscoped keys', () => {
  const storage = memoryStorage()
  const key = assistantComposerDraftStorageKey(ownerScope)
  const value = draft('keep this text', [{ type: 'text', text: 'keep this text' }, receiptPart], {
    skillIDs: ['review'], responseMode: 'plan',
  })
  assert.equal(writeAssistantComposerDraft(ownerScope, value, storage, 10_000), true)
  assert.equal(removeAssistantComposerDraftAttachment(key, receiptPart.attachment.id, storage, 60_000), true)
  const after = storage.getItem(key)
  assert.deepEqual(readAssistantComposerDraft(ownerScope, storage, 60_100), draft('keep this text', [{ type: 'text', text: 'keep this text' }], {
    skillIDs: ['review'], responseMode: 'plan',
  }))
  assert.equal(removeAssistantComposerDraftAttachment('unrelated:key', receiptPart.attachment.id, storage, 60_200), false)
  assert.equal(storage.getItem(key), after)
})

test('composer teardown preserves ready receipts referenced by a draft for remount', () => {
  const storage = memoryStorage()
  const key = assistantComposerDraftStorageKey(ownerScope)
  const savedDraft = draft('keep this file', [{ type: 'text', text: 'keep this file' }, receiptPart])
  assert.equal(writeAssistantComposerDraft(ownerScope, savedDraft, storage, 10_000), true)

  const chips = [
    { clientID: 'ready-owner', status: 'ready', draftScopeKey: key, receipt: receiptPart.attachment },
    { clientID: 'deleting-owner', status: 'deleting', draftScopeKey: key, receipt: receiptPart.attachment },
    { clientID: 'delete-error-owner', status: 'error', retryAction: 'delete', draftScopeKey: key, receipt: receiptPart.attachment },
    { clientID: 'uploading-owner', status: 'uploading', retryAction: 'upload', draftScopeKey: key },
    { clientID: 'staged-owner', status: 'staged', draftScopeKey: key },
  ]
  const teardownCleanup = chips.filter((chip) => shouldCleanupAssistantAttachmentOnScopeSwitch(chip.status, chip.retryAction, Boolean(chip.draftScopeKey)))

  assert.deepEqual(teardownCleanup.map((chip) => chip.clientID), ['uploading-owner', 'staged-owner'])
  assert.equal(shouldCleanupAssistantAttachmentOnScopeSwitch('ready', undefined, false), true)
  assert.deepEqual(readAssistantComposerDraft(ownerScope, storage, 10_100), savedDraft)
  assert.deepEqual(
    reconcileAssistantAttachmentChips([chips[0]], key, new Set([receiptPart.attachment.id])).map((chip) => chip.clientID),
    ['ready-owner'],
  )
})
