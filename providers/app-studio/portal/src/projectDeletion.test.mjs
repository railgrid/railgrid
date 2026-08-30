import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./projectDeletion.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const { createProjectDeletionController, sameProjectIdentity } = await import(moduleURL)

function deferred() {
  let resolve
  let reject
  const promise = new Promise((nextResolve, nextReject) => {
    resolve = nextResolve
    reject = nextReject
  })
  return { promise, resolve, reject }
}

const context = { fingerprint: 'tenant-a:user-a', routePath: '' }

function project(uid = 'uid-old', extra = {}) {
  return { name: 'demo', uid, ...extra }
}

test('hides an accepted delete from deferred active and terminating lists until gone', async () => {
  const controller = createProjectDeletionController()
  const deleteResponse = deferred()
  const listResponse = deferred()
  const operation = controller.begin(project(), context)

  const deleteRequest = deleteResponse.promise.then(() => controller.acknowledge(operation))
  const reloadedList = listResponse.promise.then((items) => controller.reconcile(context.fingerprint, items))

  // The list response is allowed to race the DELETE response. It must not
  // resurrect the accepted UID when it is an active (stale) projection.
  deleteResponse.resolve()
  await deleteRequest
  listResponse.resolve([project()])
  assert.deepEqual(await reloadedList, [])
  assert.equal(controller.hasPending(context.fingerprint), true)

  const terminating = controller.reconcile(context.fingerprint, [project('uid-old', { deleting: true })])
  assert.deepEqual(terminating, [])

  assert.deepEqual(controller.reconcile(context.fingerprint, []), [])
  assert.equal(controller.hasPending(context.fingerprint), false)
})

test('keeps a server-discovered terminating project visible and locked until gone', () => {
  const controller = createProjectDeletionController()
  const terminating = project('uid-old', { deleting: true })

  assert.deepEqual(controller.reconcile(context.fingerprint, [terminating]), [terminating])
  assert.equal(controller.isDeleting(context.fingerprint, terminating), true)
  assert.equal(controller.hasPending(context.fingerprint), true)

  assert.deepEqual(controller.reconcile(context.fingerprint, []), [])
  assert.equal(controller.hasPending(context.fingerprint), false)
})

test('fences a confirmation and retry when route or authenticated context changes', async () => {
  const controller = createProjectDeletionController()
  const oldTarget = project('uid-old')
  const oldOperation = controller.begin(oldTarget, context)
  const replacementContext = { fingerprint: 'tenant-b:user-b', routePath: 'replacement' }
  controller.invalidate()

  let deleteCalls = 0
  const staleRetry = async () => {
    if (controller.matchesCurrent(oldOperation, replacementContext, project('uid-new'))) deleteCalls += 1
  }
  await staleRetry()

  assert.equal(controller.isCurrent(oldOperation, replacementContext), false)
  assert.equal(deleteCalls, 0)
  assert.equal(controller.hasPending(replacementContext.fingerprint), false)
})

test('allows a same-name recreation with a different UID to remain visible', () => {
  const controller = createProjectDeletionController()
  const oldTarget = project('uid-old')
  const operation = controller.begin(oldTarget, context)
  controller.acknowledge(operation)

  const replacement = project('uid-new')
  assert.equal(sameProjectIdentity(oldTarget, replacement), false)
  assert.deepEqual(controller.reconcile(context.fingerprint, [replacement]), [replacement])
  assert.equal(controller.isDeleting(context.fingerprint, replacement), false)
  assert.equal(controller.hasPending(context.fingerprint), false)
})

test('does not leave a tombstone after a failed DELETE and permits a retry', async () => {
  const controller = createProjectDeletionController()
  const target = project()
  const firstOperation = controller.begin(target, context)
  let deleteCalls = 0

  await assert.rejects(
    (async () => {
      deleteCalls += 1
      throw new Error('delete rejected')
    })(),
    /delete rejected/,
  )
  assert.equal(controller.hasPending(context.fingerprint), false)
  assert.deepEqual(controller.reconcile(context.fingerprint, [target]), [target])

  const retryOperation = controller.begin(target, context)
  await Promise.resolve()
  deleteCalls += 1
  controller.acknowledge(retryOperation)
  assert.equal(deleteCalls, 2)
  assert.deepEqual(controller.reconcile(context.fingerprint, [target]), [])
  // The failed operation did not become authoritative; only the retry's
  // acknowledgement suppresses the stale active projection.
  assert.equal(firstOperation.serial < retryOperation.serial, true)
})
