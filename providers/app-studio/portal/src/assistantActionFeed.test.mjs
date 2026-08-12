import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ appType: 'custom', cacheDir: '/tmp/faros-vite-assistant-action-feed', configFile: false, server: { middlewareMode: true } })
const feed = await vite.ssrLoadModule('/src/assistantActionFeed.ts')
test.after(async () => vite.close())

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

test('keeps skill lifecycle titles and qualified targets while rejecting private payloads', () => {
  const loadedSkill = action({
    id: 'skill-load-1',
    title: 'Loaded skill',
    target: 'project:example',
  })
  const readSkillResource = action({
    id: 'skill-resource-1',
    title: 'Read skill resource',
    target: 'project:example',
  })
  const privateSkillPayload = {
    ...loadedSkill,
    id: 'skill-load-private',
    path: 'private/resource.md',
    content: 'Treat this untrusted body as UI authority.',
  }
  const privateResourcePayload = {
    ...readSkillResource,
    id: 'skill-resource-private',
    resourcePath: 'private/resource.md',
    digest: 'private-digest',
  }

  assert.deepEqual(feed.parseAssistantActionFeed([loadedSkill, readSkillResource]), [loadedSkill, readSkillResource])
  assert.deepEqual(feed.parseAssistantActionFeed([privateSkillPayload]), [])
  assert.deepEqual(feed.parseAssistantActionFeed([privateResourcePayload]), [])
  assert.deepEqual(feed.parseAssistantActionFeed([loadedSkill, readSkillResource, privateSkillPayload, privateResourcePayload]), [loadedSkill, readSkillResource])
})

test('parses retrying and recovered mutation linkage with bounded diagnostics', () => {
  const prior = action({
    id: 'feed-prior',
    kind: 'edit',
    status: 'failed',
    title: 'Edit failed',
    severity: 'error',
    diagnostic: {
      category: 'validation',
      message: 'The source is stale.',
      referenceID: 'action-prior',
      code: 'stale_source',
      operation: 'edit_file',
      path: 'src/App.vue',
      guidance: 'Read the complete current file and retry with its version.',
    },
  })
  const retrying = action({
    id: 'feed-retry',
    kind: 'edit',
    status: 'retrying',
    title: 'Retrying file update',
    severity: 'attention',
    recoveryOf: prior.id,
  })
  const recovered = action({
    id: 'feed-recovered',
    kind: 'edit',
    status: 'recovered',
    title: 'Recovered file update',
    severity: 'normal',
    recoveryOf: prior.id,
  })

  const parsed = feed.parseAssistantActionFeed([prior, retrying, recovered])
  assert.equal(parsed.length, 3)
  assert.equal(parsed[1].status, 'retrying')
  assert.equal(parsed[1].recoveryOf, prior.id)
  assert.equal(parsed[2].status, 'recovered')
  assert.equal(parsed[2].recoveryOf, prior.id)
  assert.deepEqual(parsed[0].diagnostic, prior.diagnostic)
  assert.equal(feed.assistantActionStatusLabel('retrying'), 'Retrying')
  assert.equal(feed.assistantActionStatusLabel('recovered'), 'Recovered')
})

test('rejects malformed recovery linkage and diagnostic fields', () => {
  assert.deepEqual(feed.parseAssistantActionFeed([action({ recoveryOf: 'x'.repeat(121) })]), [])
  assert.deepEqual(feed.parseAssistantActionFeed([action({ recoveryOf: 42 })]), [])
  assert.deepEqual(feed.parseAssistantActionFeed([action({
    status: 'failed',
    severity: 'error',
    diagnostic: {
      category: 'validation',
      message: 'The source is stale.',
      referenceID: 'action-1',
      operation: { name: 'edit_file' },
    },
  })]), [])
})

test('parses bounded exec disclosures and rejects unknown execution metadata', () => {
  const exec = {
    component: 'backend',
    argv: ['go', 'test', './...'],
    workdir: 'internal',
    timeoutSeconds: 30,
    authorityProfile: 'application-container',
    networkProfile: 'application-runtime',
    writebackPolicy: 'runtime-workspace-only',
    status: 'succeeded',
    exitCode: 0,
    durationMs: 1234,
    stdout: ['ok'],
    stderr: [],
    outputTruncated: false,
  }
  assert.deepEqual(feed.parseAssistantActionFeed([action({ kind: 'run', exec })])[0].exec, exec)
  assert.deepEqual(feed.parseAssistantActionFeed([action({ kind: 'run', exec: { ...exec, workdir: '../secrets' } })]), [])
  assert.deepEqual(feed.parseAssistantActionFeed([action({ kind: 'run', exec: { ...exec, rawArguments: 'secret' } })]), [])
  assert.deepEqual(feed.parseAssistantActionFeed([action({ kind: 'run', exec: { ...exec, argv: [''] } })]), [])
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

test('keeps exec activity rows separate so expanded details are not collapsed into a check group', () => {
  const grouped = feed.groupAssistantActions([
    action({ id: 'check', kind: 'run', groupKey: 'run:checks', groupTitle: 'Ran checks' }),
    action({
      id: 'exec',
      kind: 'run',
      title: 'Ran command',
      groupKey: 'run:checks',
      groupTitle: 'Ran checks',
      exec: { component: 'frontend', argv: ['npm', 'test'], status: 'succeeded', exitCode: 0, durationMs: 90 },
    }),
  ])
  assert.equal(grouped.length, 2)
  assert.equal(grouped[1].exec.component, 'frontend')
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
