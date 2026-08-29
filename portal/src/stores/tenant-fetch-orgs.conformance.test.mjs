import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import test from 'node:test'

const root = path.resolve(new URL('../../../', import.meta.url).pathname)
const tenant = fs.readFileSync(path.join(root, 'portal', 'src', 'stores', 'tenant.ts'), 'utf8')
const fetchStart = tenant.indexOf('function fetchOrgs(): Promise<void>')
const fetchEnd = tenant.indexOf('\n  async function fetchWorkspaces', fetchStart)
assert.ok(fetchStart >= 0 && fetchEnd > fetchStart, 'fetchOrgs source boundary is present')
const fetchOrgs = tenant.slice(fetchStart, fetchEnd)
const workspacesStart = fetchEnd + 1
const workspacesEnd = tenant.indexOf('\n  function selectOrg', workspacesStart)
assert.ok(workspacesEnd > workspacesStart, 'fetchWorkspaces source boundary is present')
const fetchWorkspaces = tenant.slice(workspacesStart, workspacesEnd)

test('fetchOrgs coalesces requests within one selection authority', () => {
  assert.match(tenant, /const orgRequestsBySelectionRevision = new Map<number, Promise<void>>\(\)/)
  assert.match(fetchOrgs, /const selectionRevisionAtStart = selectionRevision/)
  assert.match(fetchOrgs, /const inFlight = orgRequestsBySelectionRevision\.get\(selectionRevisionAtStart\)/)
  assert.match(fetchOrgs, /if \(inFlight\) return inFlight/)
  assert.match(fetchOrgs, /const requestEpoch = \+\+orgRequestEpoch/)
  assert.match(fetchOrgs, /orgRequestsBySelectionRevision\.set\(selectionRevisionAtStart, request\)/)
})

test('fetchOrgs exposes scoped authority state and preserves a cached list on failure', () => {
  assert.match(tenant, /type OrgLoadState = 'idle' \| 'loading' \| 'ready' \| 'error'/)
  assert.match(tenant, /const orgLoadState = ref<OrgLoadState>\('idle'\)/)
  assert.match(tenant, /const orgError = ref<string \| null>\(null\)/)
  assert.match(tenant, /orgs,\s*orgLoadState,\s*orgError,\s*orgListLoaded,\s*workspacesByOrg/)

  // A coalesced caller observes the existing authority rather than resetting
  // the state for a request it did not start.
  assert.match(fetchOrgs, /if \(inFlight\) return inFlight[\s\S]*orgLoadState\.value = 'loading'[\s\S]*orgError\.value = null/)

  // Success is the only path that replaces the list and clears the scoped
  // error. HTTP failure must leave the cached rows available for rendering.
  assert.match(fetchOrgs, /orgs\.value = data\.items \?\? \[\][\s\S]*orgLoadState\.value = 'ready'[\s\S]*orgError\.value = null/)
  assert.match(fetchOrgs, /Unable to load organizations \(HTTP \$\{resp\.status\}\)\. Try again\./)
  assert.doesNotMatch(fetchOrgs, /orgs\.value = \[\]/)
  assert.match(fetchOrgs, /orgLoadState\.value = 'error'[\s\S]*orgError\.value = message/)
  assert.match(fetchOrgs, /readException\('Unable to load organizations\. Try again', e\)/)
})

test('fetchOrgs tracks successful list history independently and fences it', () => {
  assert.match(tenant, /const orgListLoaded = ref\(false\)/)
  assert.match(tenant, /orgs,\s*orgLoadState,\s*orgError,\s*orgListLoaded,\s*workspacesByOrg/)
  assert.doesNotMatch(fetchOrgs, /orgListLoaded\.value = false/)

  const listLoaded = fetchOrgs.indexOf('orgListLoaded.value = true')
  const listAssignment = fetchOrgs.indexOf('orgs.value = data.items ?? []')
  const jsonFence = fetchOrgs.indexOf('if (requestEpoch !== orgRequestEpoch || selectionRevision !== selectionRevisionAtStart) {', fetchOrgs.indexOf('const data'))
  assert.ok(listLoaded >= 0 && listAssignment > listLoaded, 'empty or non-empty success records list history')
  assert.ok(jsonFence >= 0 && listLoaded > jsonFence, 'list history follows the current-response fence')

  const httpFailureStart = fetchOrgs.indexOf('if (!resp.ok) {')
  const httpFailureEnd = fetchOrgs.indexOf('\n        }\n        const data', httpFailureStart)
  assert.ok(httpFailureStart >= 0 && httpFailureEnd > httpFailureStart)
  assert.doesNotMatch(fetchOrgs.slice(httpFailureStart, httpFailureEnd), /orgListLoaded\.value/)
  const catchStart = fetchOrgs.indexOf('} catch (e: unknown) {')
  const catchEnd = fetchOrgs.indexOf('\n      } finally', catchStart)
  assert.ok(catchStart >= 0 && catchEnd > catchStart)
  assert.doesNotMatch(fetchOrgs.slice(catchStart, catchEnd), /orgListLoaded\.value/)
})

test('fetchOrgs fences stale revisions and cleans up only its own request', () => {
  assert.match(fetchOrgs, /requestEpoch !== orgRequestEpoch \|\| selectionRevision !== selectionRevisionAtStart/)
  const firstFence = fetchOrgs.indexOf('if (requestEpoch !== orgRequestEpoch || selectionRevision !== selectionRevisionAtStart) {')
  const responseState = fetchOrgs.indexOf("orgLoadState.value = 'ready'")
  assert.ok(firstFence >= 0 && responseState > firstFence, 'success state follows the response fence')
  const catchStart = fetchOrgs.indexOf('} catch (e: unknown) {')
  const catchFence = fetchOrgs.indexOf('if (requestEpoch === orgRequestEpoch && selectionRevision === selectionRevisionAtStart)', catchStart)
  const catchState = fetchOrgs.indexOf("orgLoadState.value = 'error'", catchStart)
  assert.ok(catchFence > catchStart && catchState > catchFence, 'network error state is revision-fenced')
  assert.match(fetchOrgs, /if \(orgRequestsBySelectionRevision\.get\(selectionRevisionAtStart\) === request\) \{\s*orgRequestsBySelectionRevision\.delete\(selectionRevisionAtStart\)\s*\}/)
  assert.match(fetchOrgs, /finally \{[\s\S]*orgPending = Math\.max\(0, orgPending - 1\)[\s\S]*updateLoading\(\)/)
})

test('fetchOrgs idles a stale completion only while it owns the loading state', () => {
  const staleStateStart = fetchOrgs.indexOf('const resetStaleLoadState = (): void => {')
  const staleStateEnd = fetchOrgs.indexOf('\n    }\n\n    let request!', staleStateStart)
  assert.ok(staleStateStart >= 0 && staleStateEnd > staleStateStart, 'stale state guard is present')
  const staleStateGuard = fetchOrgs.slice(staleStateStart, staleStateEnd)
  assert.match(staleStateGuard, /requestEpoch === orgRequestEpoch/)
  assert.match(staleStateGuard, /selectionRevision !== selectionRevisionAtStart/)
  assert.match(staleStateGuard, /orgLoadState\.value === 'loading'/)
  assert.match(staleStateGuard, /orgLoadState\.value = 'idle'/)
  assert.match(staleStateGuard, /orgError\.value = null/)

  // Both response fences and the network catch must use the guard; a newer
  // request or a newer terminal state therefore cannot be reset by an older
  // completion.
  assert.equal((fetchOrgs.match(/resetStaleLoadState\(\)/g) ?? []).length, 3)
})

test('fetchOrgs keeps one pending count and permits a later retry', () => {
  assert.equal((fetchOrgs.match(/orgPending\+\+/g) ?? []).length, 1)
  assert.equal((fetchOrgs.match(/orgPending = Math\.max\(0, orgPending - 1\)/g) ?? []).length, 1)
  assert.match(fetchOrgs, /if \(inFlight\) return inFlight[\s\S]*let request!:[\s\S]*request =/)
  assert.match(fetchOrgs, /orgRequestsBySelectionRevision\.delete\(selectionRevisionAtStart\)/)
})

test('fetchWorkspaces preserves cached rows on current HTTP and network failures', () => {
  const httpFailureStart = fetchWorkspaces.indexOf('if (!resp.ok) {')
  const httpFailureEnd = fetchWorkspaces.indexOf('\n      const data', httpFailureStart)
  assert.ok(httpFailureStart >= 0 && httpFailureEnd > httpFailureStart, 'HTTP failure branch is present')
  const httpFailure = fetchWorkspaces.slice(httpFailureStart, httpFailureEnd)
  const httpFence = fetchWorkspaces.indexOf('if (epoch !== workspaceRequestEpochByOrg.get(targetOrgUUID)) return')
  assert.ok(httpFence >= 0 && httpFence < httpFailureStart, 'HTTP failure follows the epoch fence')
  assert.match(httpFailure, /workspaceLoadStateByOrg\.value = \{[\s\S]*\[targetOrgUUID\]: 'error'/)
  assert.match(httpFailure, /workspaceErrorByOrg\.value = \{[\s\S]*\[targetOrgUUID\]: message/)
  assert.doesNotMatch(httpFailure, /workspacesByOrg\.value\s*=/)

  const networkFailureStart = fetchWorkspaces.indexOf('} catch (e: unknown) {')
  const networkFailureEnd = fetchWorkspaces.indexOf('\n    } finally', networkFailureStart)
  assert.ok(networkFailureStart >= 0 && networkFailureEnd > networkFailureStart, 'network failure branch is present')
  const networkFailure = fetchWorkspaces.slice(networkFailureStart, networkFailureEnd)
  assert.match(networkFailure, /if \(epoch !== workspaceRequestEpochByOrg\.get\(targetOrgUUID\)\) return/)
  assert.match(networkFailure, /workspaceLoadStateByOrg\.value = \{[\s\S]*\[targetOrgUUID\]: 'error'/)
  assert.match(networkFailure, /workspaceErrorByOrg\.value = \{[\s\S]*\[targetOrgUUID\]: message/)
  assert.doesNotMatch(networkFailure, /workspacesByOrg\.value\s*=/)
})
