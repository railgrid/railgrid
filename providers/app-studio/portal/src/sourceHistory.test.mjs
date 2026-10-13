import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ server: { middlewareMode: true }, appType: 'custom', cacheDir: '/tmp/railgrid-vite-source-history' })
test.after(async () => { await vite.close() })

const {
  adjacentHistoryCommit,
  formatHistoryAge,
  orderRepositoryCommits,
  reconcileHistorySelection,
  repositoryCommitSelectable,
  selectedHistoryCommit,
} = await vite.ssrLoadModule('/src/sourceHistory.ts')

const sha = (char) => char.repeat(40)
const commit = (char, overrides = {}) => ({
  name: `commit-${char}`,
  phase: 'Succeeded',
  commitSHA: sha(char),
  message: `Commit ${char}`,
  createdAt: '2026-08-18T10:00:00Z',
  ...overrides,
})

test('selects successful full repository commits independently of release images', () => {
  assert.equal(repositoryCommitSelectable(commit('a')), true)
  assert.equal(repositoryCommitSelectable(commit('b', { phase: 'Failed' })), false)
  assert.equal(repositoryCommitSelectable(commit('c', { commitSHA: 'cafe123' })), false)
  assert.equal(repositoryCommitSelectable(commit('d', { deployable: false, missing: ['app'] })), true)
})

test('orders source history newest first and preserves a valid selection', () => {
  const older = commit('a', { createdAt: '2026-08-17T10:00:00Z' })
  const newest = commit('b', { createdAt: '2026-08-19T10:00:00Z' })
  const failed = commit('c', { phase: 'Failed', createdAt: '2026-08-20T10:00:00Z' })
  assert.deepEqual(orderRepositoryCommits([older, newest, failed]).map(({ commitSHA }) => commitSHA), [sha('c'), sha('b'), sha('a')])
  assert.equal(reconcileHistorySelection(sha('a'), [newest, older]), sha('a'))
  assert.equal(reconcileHistorySelection(sha('c'), [failed, newest, older]), '')
  assert.equal(selectedHistoryCommit([newest, older], sha('a')).message, 'Commit a')
})

test('deduplicates successful revisions by canonical SHA and keeps the newest record', () => {
  const older = commit('a', {
    name: 'older-a',
    createdAt: '2026-08-18T10:00:00Z',
    completedAt: '2026-08-18T10:01:00Z',
  })
  const newest = commit('A', {
    name: 'newest-a',
    commitSHA: ` ${sha('A')} `,
    createdAt: '2026-08-19T10:00:00Z',
    completedAt: '2026-08-19T10:01:00Z',
    message: 'Latest metadata for revision a',
  })
  const failed = commit('a', { name: 'failed-a', phase: 'Failed', createdAt: '2026-08-20T10:00:00Z' })
  const pending = commit('a', { name: 'pending-a', phase: 'Pending', createdAt: '2026-08-21T10:00:00Z' })
  const nextRevision = commit('b', { createdAt: '2026-08-17T10:00:00Z' })
  const commits = [older, failed, newest, pending, nextRevision]
  const inputOrder = [...commits]

  const ordered = orderRepositoryCommits(commits)
  assert.deepEqual(commits, inputOrder, 'ordering must not mutate the source list')
  assert.equal(ordered.filter(repositoryCommitSelectable).filter(({ commitSHA }) => commitSHA.trim().toLowerCase() === sha('a')).length, 1)
  assert.ok(ordered.includes(newest), 'the newest successful record should represent the revision')
  assert.ok(!ordered.includes(older), 'an older successful duplicate should be removed')
  assert.ok(ordered.includes(failed), 'a failed record should remain visible')
  assert.ok(ordered.includes(pending), 'a pending record should remain visible')
  assert.equal(reconcileHistorySelection(sha('a'), commits), newest.commitSHA)
  assert.equal(selectedHistoryCommit(commits, sha('a')), newest)
  assert.equal(adjacentHistoryCommit(commits, sha('a'), 'next'), nextRevision)
  assert.equal(adjacentHistoryCommit(commits, nextRevision.commitSHA, 'next'), newest)
})

test('resolves selection and keyboard movement across trimmed, case-insensitive SHA values', () => {
  const selected = commit('a', { commitSHA: ` ${sha('A')} `, createdAt: '2026-08-19T10:00:00Z' })
  const older = commit('b', { createdAt: '2026-08-18T10:00:00Z' })
  const commits = [selected, older]
  const requestedSHA = `\t${sha('a').toUpperCase()}  `

  assert.equal(reconcileHistorySelection(requestedSHA, commits), selected.commitSHA)
  assert.equal(selectedHistoryCommit(commits, requestedSHA), selected)
  assert.equal(adjacentHistoryCommit(commits, requestedSHA, 'next'), older)
  assert.equal(adjacentHistoryCommit(commits, ` ${sha('A')} `, 'previous'), older)
  assert.equal(adjacentHistoryCommit(commits, requestedSHA, 'next').commitSHA, older.commitSHA)
  assert.equal(adjacentHistoryCommit(commits, older.commitSHA, 'next'), selected)
})

test('moves keyboard selection across successful commits with wrapping', () => {
  const commits = [commit('a'), commit('b', { createdAt: '2026-08-17T10:00:00Z' }), commit('c', { phase: 'Pending' })]
  assert.equal(adjacentHistoryCommit(commits, sha('a'), 'next').commitSHA, sha('b'))
  assert.equal(adjacentHistoryCommit(commits, sha('b'), 'next').commitSHA, sha('a'))
  assert.equal(adjacentHistoryCommit(commits, sha('a'), 'previous').commitSHA, sha('b'))
  assert.equal(adjacentHistoryCommit(commits, sha('b'), 'first').commitSHA, sha('a'))
})

test('formats stable relative history ages', () => {
  assert.equal(formatHistoryAge('2026-08-18T10:00:00Z', Date.parse('2026-08-18T10:45:00Z')), '45m ago')
  assert.equal(formatHistoryAge('not-a-date'), '')
})

test('never automatically chooses a revision on load or when the selected revision disappears', () => {
  const commits = [commit('a'), commit('b')]
  assert.equal(reconcileHistorySelection('', commits), '')
  assert.equal(reconcileHistorySelection(null, commits), '')
  assert.equal(reconcileHistorySelection(sha('c'), commits), '')
  assert.equal(reconcileHistorySelection(sha('a'), []), '')
  assert.equal(adjacentHistoryCommit(commits, '', 'next').commitSHA, sha('b'))
})
