import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import ts from 'typescript'

const source = await readFile(new URL('./assistantActionFeed.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
})
const feed = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)

const action = (overrides = {}) => ({
  id: 'action-1',
  kind: 'inspect',
  status: 'succeeded',
  title: 'Read project file',
  target: 'src/App.vue',
  severity: 'normal',
  sequence: 1,
  ...overrides,
})

test('parses only the fresh allowlisted action feed contract', () => {
  assert.deepEqual(feed.parseAssistantActionFeed([action()]), [action()])
  assert.deepEqual(feed.parseAssistantActionFeed([action({ sequence: 2 })]), [action({ sequence: 2 })])
  assert.deepEqual(feed.parseAssistantActionFeed([action({ sequence: undefined })]), [])
  assert.deepEqual(feed.parseAssistantActionFeed([action({ sequence: 0 })]), [])
  assert.deepEqual(feed.parseAssistantActionFeed([action({ sequence: 10_001 })]), [])
  assert.deepEqual(
    feed.parseAssistantActionFeed([action({ status: 'skipped', title: 'Skipped duplicate read' })]),
    [action({ status: 'skipped', title: 'Skipped duplicate read' })],
  )
  assert.deepEqual(feed.parseAssistantActionFeed([action({ tool: 'read_file' })]), [])
  assert.deepEqual(feed.parseAssistantActionFeed([action({ arguments: 'offset=200 limit=50' })]), [])
})

test('suppresses plan events and rejects malformed diagnostics', () => {
  assert.deepEqual(feed.parseAssistantActionFeed([action({ kind: 'plan' })]), [])
  assert.deepEqual(feed.parseAssistantActionFeed([action({
    kind: 'other',
    title: 'Completed action',
  })]), [])
  assert.deepEqual(feed.parseAssistantActionFeed([action({
    kind: 'other',
    status: 'running',
    title: 'Working',
  })]), [])
  assert.equal(feed.parseAssistantActionFeed([action({
    kind: 'other',
    status: 'waiting',
    severity: 'attention',
    title: 'Waiting for action',
  })]).length, 1)
  assert.equal(feed.parseAssistantActionFeed([action({
    kind: 'other',
    status: 'failed',
    severity: 'error',
    title: 'Action failed',
  })]).length, 1)
  assert.deepEqual(feed.parseAssistantActionFeed([action({
    status: 'failed',
    severity: 'error',
    diagnostic: { category: 'runtime', message: 'Preview did not start.', referenceID: 'run-1', rawError: 'secret' },
  })]), [])
})

test('preserves file targets and only aggregates adjacent reads of the same file', () => {
  const grouped = feed.groupAssistantActions([
    action({ id: 'read-1', groupKey: 'inspect:files', groupTitle: 'Read project files' }),
    action({ id: 'read-2', target: 'src/style.css', groupKey: 'inspect:files', groupTitle: 'Read project files' }),
    action({ id: 'read-3', target: 'src/style.css', groupKey: 'inspect:files', groupTitle: 'Read project files' }),
    action({ id: 'search', title: 'Searched project', target: undefined, groupKey: 'inspect:search', groupTitle: 'Searched project' }),
    action({ id: 'read-4', groupKey: 'inspect:files', groupTitle: 'Read project files' }),
    action({ id: 'commit', kind: 'commit', title: 'Committed changes', groupKey: undefined, groupTitle: undefined }),
  ])
  assert.equal(grouped.length, 5)
  assert.deepEqual(grouped[0].sourceIDs, ['read-1'])
  assert.equal(grouped[0].title, 'Read project file')
  assert.equal(grouped[0].target, 'src/App.vue')
  assert.deepEqual(grouped[1].sourceIDs, ['read-2', 'read-3'])
  assert.equal(grouped[1].target, 'src/style.css')
  assert.equal(grouped[1].outcome, '2 reads')
  assert.deepEqual(grouped[3].sourceIDs, ['read-4'])
  assert.equal(feed.assistantActionCount(grouped), 5)
})

test('never groups failures, rejected actions, diagnostics, or milestones', () => {
  const grouped = feed.groupAssistantActions([
    action({ id: 'failed', status: 'failed', severity: 'error', groupKey: 'read', groupTitle: 'Read files' }),
    action({ id: 'rejected', status: 'rejected', severity: 'attention', groupKey: 'read', groupTitle: 'Read files' }),
    action({ id: 'commit', kind: 'commit', title: 'Committed changes', groupKey: 'commit', groupTitle: 'Committed changes' }),
  ])
  assert.equal(grouped.length, 3)
  assert.equal(feed.assistantActionStatusLabel('failed'), 'Failed')
  assert.equal(feed.assistantActionStatusLabel('rejected'), 'Rejected')
  assert.equal(feed.assistantActionStatusLabel('skipped'), 'Skipped')
})

test('bounds collapsed summaries while preserving grouped count', () => {
  const grouped = feed.groupAssistantActions([
    action({ id: 'one', title: `Read ${'nested/'.repeat(20)}App.vue` }),
    action({ id: 'two', title: 'Updated src/App.vue' }),
    action({ id: 'three', title: 'Checked preview', outcome: 'Ready' }),
    action({ id: 'four', title: 'Committed 2 files' }),
  ])
  assert.match(feed.summarizeAssistantActions(grouped), /\.\.\. · Updated src\/App.vue · Checked preview · Ready · 1 more$/)
})

test('counts visible grouped rows rather than tool calls or affected resources', () => {
  const grouped = feed.groupAssistantActions([
    action({ id: 'list', title: 'Read project files', outcome: '6 project files', count: 6 }),
    action({ id: 'read-1', groupKey: 'inspect:files', groupTitle: 'Read project files' }),
    action({ id: 'read-2', groupKey: 'inspect:files', groupTitle: 'Read project files' }),
  ])
  assert.equal(feed.assistantActionCount(grouped), 2)
})
