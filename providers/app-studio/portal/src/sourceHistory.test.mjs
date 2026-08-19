import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ server: { middlewareMode: true }, appType: 'custom', cacheDir: '/tmp/faros-vite-source-history' })
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
  assert.equal(reconcileHistorySelection(sha('c'), [failed, newest, older]), sha('b'))
  assert.equal(selectedHistoryCommit([newest, older], sha('a')).message, 'Commit a')
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
