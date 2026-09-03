import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
})
test.after(async () => vite?.close())

test('does not render internal verification metadata as a conversation banner', async () => {
  const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.doesNotMatch(appSource, /message\.verification/)
  assert.doesNotMatch(appSource, /Interactions verified/)
  assert.doesNotMatch(appSource, /The current preview was loaded and exercised/)
})

test('projects terminal worked duration from canonical agent message data', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const { parseAssistantProgress } = await vite.ssrLoadModule('/src/assistantProgress.ts')
  const messages = assistantThreadItemsToMessages([{
    id: 'assistant-1',
    turnID: 'run-1',
    type: 'agentMessage',
    status: 'completed',
    content: 'Done',
    data: {
      assistantProgress: {
        version: 1,
        messages: [],
        messageSequences: [],
        workedDurationMs: 83_400,
      },
    },
    sequence: 4,
    createdAt: '2026-08-02T17:42:09Z',
  }], 'demo')

  assert.equal(messages.length, 1)
  assert.equal(messages[0].metadata.assistantStatus, 'completed')
  assert.equal(parseAssistantProgress(messages[0].metadata.assistantProgress)?.workedDurationMs, 83_400)
})

test('projects durable verification metadata after a hard refresh', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const [message] = assistantThreadItemsToMessages([{
    id: 'assistant-verified', turnID: 'run-verified', type: 'agentMessage', status: 'completed', content: 'Done',
    data: { assistantVerification: { outcome: 'rendered_verified', renderedStateObserved: true } },
    sequence: 1,
    createdAt: '2026-08-02T17:42:09Z',
  }], 'demo')

  assert.deepEqual(message.metadata.assistantVerification, {
    outcome: 'rendered_verified',
    renderedStateObserved: true,
  })
})

test('projects only canonical context-resource references on user messages', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([{
    id: 'user-resource', turnID: 'run-resource', type: 'userMessage', status: 'completed', content: 'Use this table',
    data: { contextResources: [{
      provider: 'databricks',
      resourceRef: { apiVersion: 'databricks.faros.sh/v1alpha1', kind: 'Table', resource: 'tables', name: 'trips' },
      uid: 'must-not-project', resourceVersion: 'must-not-project', catalogDigest: 'must-not-project',
    }] },
    sequence: 1, createdAt: '2026-08-02T17:42:09Z',
  }], 'demo')
  assert.deepEqual(messages[0].metadata.assistantContextResources, [{
    provider: 'databricks',
    resourceRef: { apiVersion: 'databricks.faros.sh/v1alpha1', kind: 'Table', resource: 'tables', name: 'trips' },
  }])
  assert.doesNotMatch(JSON.stringify(messages[0].metadata), /must-not-project/)
})

test('projects bounded preview annotations while dropping incomplete history parts', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const annotation = {
    id: 'annotation-1',
    comment: 'Fix this control',
    documentID: '826e6fa5-c38b-4bdb-8f8f-098198b74f65',
    pagePath: '/settings',
    viewport: { width: 1024, height: 768 },
    target: {
      tag: 'button',
      role: 'button',
      name: 'Save changes',
      text: 'Save changes',
      locator: '#save',
      locatorStrategy: 'css',
      ancestors: ['main'],
      rect: { x: 4, y: 8, width: 120, height: 32 },
    },
  }
  const [message] = assistantThreadItemsToMessages([{
    id: 'user-annotation',
    turnID: 'run-annotation',
    type: 'userMessage',
    status: 'completed',
    content: 'Review this control',
    data: {
      contentParts: [
        { type: 'annotation', annotation },
        { type: 'annotation', annotation: { ...annotation, id: 'incomplete', comment: '', documentID: '' } },
      ],
    },
    sequence: 1,
    createdAt: '2026-08-02T17:42:09Z',
  }], 'demo')
  assert.deepEqual(message.metadata.assistantContentParts, [{ type: 'annotation', annotation }])
})

test('renders persisted annotations as one Codex-style thread attachment outside user prose', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const { default: AssistantMessageAnnotations } = await vite.ssrLoadModule('/src/AssistantMessageAnnotations.vue')
  const renderStart = app.indexOf('function renderMessageContent')
  const renderEnd = app.indexOf('function assistantSkillsForMessage', renderStart)
  const render = app.slice(renderStart, renderEnd)
  assert.match(render, /Annotations render as a single thread attachment outside the prose/)
  assert.match(render, /Once durable content parts exist they are the only display authority/)
  assert.match(render, /return rendered/)
  assert.doesNotMatch(render, /if \(rendered\) return rendered/)
  assert.doesNotMatch(render, /§/)
  assert.match(app, /function assistantAnnotationsForMessage/)
  assert.match(app, /<AssistantMessageAnnotations[\s\S]*:annotations="assistantAnnotationsForMessage\(message\)"/)
  assert.match(app, /v-if="userMessageHasVisibleContent\(message\)"/)
  const visibilityStart = app.indexOf('function userMessageHasVisibleContent')
  const visibilityEnd = app.indexOf('\n}', visibilityStart)
  const visibility = app.slice(visibilityStart, visibilityEnd)
  assert.match(visibility, /if \(parts\.length\) return parts\.some\(\(part\) => part\.type !== 'annotation'\)/)
  assert.match(visibility, /return Boolean\(message\.content\)/)

  const html = await renderToString(createSSRApp(AssistantMessageAnnotations, {
    annotations: [{
      id: 'annotation-message-1',
      comment: 'Prefer humans and agents.',
      documentID: '826e6fa5-c38b-4bdb-8f8f-098198b74f65',
      pagePath: '/',
      viewport: { width: 1024, height: 768 },
      target: { tag: 'section', text: `Federated MCP <script>${'alert'}(1)</script>` },
    }],
    currentDocumentID: '826e6fa5-c38b-4bdb-8f8f-098198b74f65',
    disclosureID: 'history-annotations',
  }))
  assert.match(html, />1 annotation</)
  assert.match(html, /aria-describedby="history-annotations-panel"/)
  assert.match(html, /role="tooltip"/)
  assert.doesNotMatch(html, /aria-label="Clear annotations"/)
  assert.match(html, /Federated MCP &lt;script&gt;alert\(1\)&lt;\/script&gt;/)
  assert.match(html, /Prefer humans and agents\./)
})

test('reload projection trims, deduplicates, and bounds context-resource chips', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const resources = [
    { provider: 'demo', resourceRef: { apiVersion: 'demo.example/v1', kind: 'Widget', resource: 'widgets', name: '  first ' } },
    { provider: 'demo', resourceRef: { apiVersion: 'demo.example/v1', kind: 'Widget', resource: 'widgets', name: 'first' }, uid: 'private' },
    ...Array.from({ length: 8 }, (_, index) => ({
      provider: 'demo',
      resourceRef: { apiVersion: 'demo.example/v1', kind: 'Widget', resource: 'widgets', name: `item-${index}` },
    })),
    { provider: 'demo', resourceRef: { apiVersion: 'demo.example/v1', kind: 'Widget', resource: 'widgets', name: 'overflow' } },
    { provider: '', resourceRef: { apiVersion: 'demo.example/v1', kind: 'Widget', resource: 'widgets', name: 'invalid' } },
  ]
  const [message] = assistantThreadItemsToMessages([{
    id: 'user-resource-bounded', turnID: 'run-resource-bounded', type: 'userMessage', status: 'completed',
    content: 'Use these resources', data: { contextResources: resources }, sequence: 1,
    createdAt: '2026-08-02T17:42:09Z',
  }], 'demo')

  assert.equal(message.metadata.assistantContextResources.length, 8)
  assert.deepEqual(message.metadata.assistantContextResources[0], {
    provider: 'demo',
    resourceRef: { apiVersion: 'demo.example/v1', kind: 'Widget', resource: 'widgets', name: 'first' },
  })
  assert.equal(message.metadata.assistantContextResources.some(({ resourceRef }) => resourceRef.name === 'overflow'), false)
  assert.doesNotMatch(JSON.stringify(message.metadata), /private/)
})

test('keeps action items alongside agent progress in the thread projection', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([{
    id: 'assistant-1', turnID: 'run-1', type: 'agentMessage', status: 'completed', content: 'Done',
    data: { assistantProgress: { version: 1, messages: [], messageSequences: [], workedDurationMs: 2_400 } },
    sequence: 1, createdAt: '2026-08-02T17:42:09Z',
  }, {
    id: 'read-1', turnID: 'run-1', type: 'dynamicToolCall', status: 'completed', content: 'Read file',
    data: { id: 'read-1', kind: 'inspect', status: 'succeeded', title: 'Read file', severity: 'normal', sequence: 1 },
    sequence: 2, createdAt: '2026-08-02T17:42:10Z',
  }], 'demo')

  assert.equal(messages[0].metadata.assistantProgress.workedDurationMs, 2_400)
  assert.equal(messages[0].metadata.assistantActionFeed.length, 1)
})

test('projects model-input image lifecycle without treating it as a tool call', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const { parseAssistantActionFeed } = await vite.ssrLoadModule('/src/assistantActionFeed.ts')
  const imageAction = {
    id: 'feed-image-1', kind: 'inspect', mediaKind: 'image', status: 'running',
    title: 'Viewing image', target: 'screen.png', severity: 'normal', sequence: 1,
  }
  const viewedAction = { ...imageAction, status: 'succeeded', title: 'Viewed image' }
  const messages = assistantThreadItemsToMessages([{
    id: 'assistant-image', turnID: 'run-image', type: 'agentMessage', status: 'completed',
    assistantMessageID: 'assistant-image', content: 'I can see it.', sequence: 1,
    createdAt: '2026-08-02T17:42:09Z',
  }, {
    id: 'model-input-assistant-image-feed-image-1', turnID: 'run-image', type: 'modelInput', status: 'in_progress',
    assistantMessageID: 'assistant-image', data: imageAction, sequence: 2,
    createdAt: '2026-08-02T17:42:10Z',
  }, {
    id: 'model-input-assistant-image-feed-image-1', turnID: 'run-image', type: 'modelInput', status: 'completed',
    assistantMessageID: 'assistant-image', data: viewedAction, sequence: 3,
    createdAt: '2026-08-02T17:42:11Z',
  }], 'demo')

  assert.equal(messages.length, 1)
  const parsed = parseAssistantActionFeed(messages[0].metadata.assistantActionFeed)
  assert.deepEqual(parsed, [viewedAction])
  assert.equal(parsed[0].mediaKind, 'image')
})

test('upserts live model-input lifecycle updates without reclassifying or duplicating the action', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(app, /rawItem\.type === 'dynamicToolCall' \|\| rawItem\.type === 'modelInput'/)
  assert.match(app, /metadata\.assistantActionFeed = upsertAssistantActionFeed\(metadata\.assistantActionFeed, rawItem\)/)

  const { upsertAssistantActionFeed } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const started = {
    id: 'model-input-assistant-live-feed-image-1', turnID: 'run-live-feed', type: 'modelInput', status: 'in_progress',
    assistantMessageID: 'assistant-live-feed', data: {
      id: 'feed-image-live', kind: 'inspect', mediaKind: 'image', status: 'running',
      title: 'Viewing image', target: 'screen.png', severity: 'normal', sequence: 1,
    }, sequence: 2, createdAt: '2026-08-02T17:42:10Z',
  }
  const completed = {
    ...started, status: 'completed', data: {
      ...started.data, status: 'succeeded', title: 'Viewed image',
    }, sequence: 3,
  }
  const afterStarted = upsertAssistantActionFeed(undefined, started)
  const afterCompleted = upsertAssistantActionFeed(afterStarted, completed)
  assert.equal(afterCompleted.length, 1)
  assert.deepEqual(afterCompleted[0], completed.data)
})

test('projects native browser actions across live updates and durable reload', async () => {
  const { upsertAssistantActionFeed, assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const { parseAssistantActionFeed } = await vite.ssrLoadModule('/src/assistantActionFeed.ts')
  const snapshotStarted = {
    id: 'tool-assistant-browser-snapshot', turnID: 'run-browser', type: 'dynamicToolCall', status: 'in_progress',
    assistantMessageID: 'assistant-browser', data: {
      id: 'browser-snapshot-1', kind: 'inspect', status: 'running', title: 'Inspecting preview',
      severity: 'attention', sequence: 1,
    }, sequence: 2, createdAt: '2026-08-02T17:42:10Z',
  }
  const consoleCompleted = {
    id: 'tool-assistant-browser-console', turnID: 'run-browser', type: 'dynamicToolCall', status: 'completed',
    assistantMessageID: 'assistant-browser', data: {
      id: 'browser-console-1', kind: 'inspect', status: 'succeeded', title: 'Reviewed browser console',
      severity: 'normal', sequence: 2,
    }, sequence: 3, createdAt: '2026-08-02T17:42:11Z',
  }
  const clickFailed = {
    id: 'tool-assistant-browser-click', turnID: 'run-browser', type: 'dynamicToolCall', status: 'completed',
    assistantMessageID: 'assistant-browser', data: {
      id: 'browser-click-1', kind: 'run', status: 'failed', title: 'Preview interaction failed',
      severity: 'error', sequence: 3,
      diagnostic: { category: 'runtime', message: 'Preview interaction failed.', referenceID: 'action-browser-click' },
    }, sequence: 4, createdAt: '2026-08-02T17:42:12Z',
  }
  const unknownCompleted = {
    id: 'tool-assistant-browser-unknown', turnID: 'run-browser', type: 'dynamicToolCall', status: 'completed',
    assistantMessageID: 'assistant-browser', data: {
      id: 'browser-unknown-1', kind: 'other', status: 'succeeded', title: 'Completed action',
      severity: 'normal', sequence: 4,
    }, sequence: 5, createdAt: '2026-08-02T17:42:13Z',
  }

  const live = upsertAssistantActionFeed(undefined, snapshotStarted)
  assert.deepEqual(live, [snapshotStarted.data])
  const liveAfterConsole = upsertAssistantActionFeed(live, consoleCompleted)
  const liveAfterClick = upsertAssistantActionFeed(liveAfterConsole, clickFailed)
  const liveAfterUnknown = upsertAssistantActionFeed(liveAfterClick, unknownCompleted)
  assert.deepEqual(parseAssistantActionFeed(liveAfterUnknown), [snapshotStarted.data, consoleCompleted.data, clickFailed.data])

  const reloaded = assistantThreadItemsToMessages([
    {
      id: 'assistant-browser', turnID: 'run-browser', type: 'agentMessage', status: 'in_progress',
      assistantMessageID: 'assistant-browser', content: '', sequence: 1,
      createdAt: '2026-08-02T17:42:09Z',
    },
    snapshotStarted, consoleCompleted, clickFailed, unknownCompleted,
  ], 'demo')
  const owner = reloaded.find(({ id }) => id === 'assistant-browser')
  assert.ok(owner)
  assert.deepEqual(parseAssistantActionFeed(owner.metadata.assistantActionFeed), [snapshotStarted.data, consoleCompleted.data, clickFailed.data])
  assert.doesNotMatch(JSON.stringify(owner.metadata.assistantActionFeed), /browser_(snapshot|console_messages|click)|arguments|result|receipt/)
})

test('projects skill load and resource updates into the parsed action feed used by the action log', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const { parseAssistantActionFeed, groupAssistantActions } = await vite.ssrLoadModule('/src/assistantActionFeed.ts')
  const items = [
    {
      id: 'assistant-skill', turnID: 'run-skill', type: 'agentMessage', status: 'in_progress',
      assistantMessageID: 'assistant-skill', content: '', sequence: 1,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'tool-assistant-skill-load', turnID: 'run-skill', type: 'dynamicToolCall', status: 'in_progress',
      assistantMessageID: 'assistant-skill',
      data: {
        id: 'skill-load-1', kind: 'inspect', status: 'running', title: 'Loading skill',
        target: 'project:example', severity: 'normal', sequence: 1,
      }, sequence: 2, createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'tool-assistant-skill-load', turnID: 'run-skill', type: 'dynamicToolCall', status: 'completed',
      assistantMessageID: 'assistant-skill',
      data: {
        id: 'skill-load-1', kind: 'inspect', status: 'succeeded', title: 'Loaded skill',
        target: 'project:example', severity: 'normal', sequence: 1,
      }, sequence: 3, createdAt: '2026-08-02T17:42:11Z',
    },
    {
      id: 'tool-assistant-skill-resource', turnID: 'run-skill', type: 'dynamicToolCall', status: 'in_progress',
      assistantMessageID: 'assistant-skill',
      data: {
        id: 'skill-resource-1', kind: 'inspect', status: 'running', title: 'Reading skill resource',
        target: 'project:example', severity: 'normal', sequence: 2,
      }, sequence: 4, createdAt: '2026-08-02T17:42:12Z',
    },
    {
      id: 'tool-assistant-skill-resource', turnID: 'run-skill', type: 'dynamicToolCall', status: 'completed',
      assistantMessageID: 'assistant-skill',
      data: {
        id: 'skill-resource-1', kind: 'inspect', status: 'succeeded', title: 'Read skill resource',
        target: 'project:example', severity: 'normal', sequence: 2,
      }, sequence: 5, createdAt: '2026-08-02T17:42:13Z',
    },
  ]

  const owner = assistantThreadItemsToMessages(items, 'demo').find(({ id }) => id === 'assistant-skill')
  assert.ok(owner)
  const parsed = parseAssistantActionFeed(owner.metadata.assistantActionFeed)
  assert.deepEqual(parsed, [{
    id: 'skill-load-1', kind: 'inspect', status: 'succeeded', title: 'Loaded skill',
    target: 'project:example', severity: 'normal', sequence: 1,
  }, {
    id: 'skill-resource-1', kind: 'inspect', status: 'succeeded', title: 'Read skill resource',
    target: 'project:example', severity: 'normal', sequence: 2,
  }])

  for (const item of parsed) {
    assert.equal(item.target, 'project:example')
    assert.equal('path' in item, false)
    assert.equal('resourcePath' in item, false)
    assert.equal('content' in item, false)
    assert.equal('digest' in item, false)
  }

  const rows = groupAssistantActions(parsed)
  assert.equal(rows.length, 2)
  assert.deepEqual(rows.map(({ title, target }) => ({ title, target })), [
    { title: 'Loaded skill', target: 'project:example' },
    { title: 'Read skill resource', target: 'project:example' },
  ])
})

test('projects bounded public skill provenance onto durable user messages', async () => {
  const { assistantThreadItemsToMessages, projectAssistantSkills } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const skills = Array.from({ length: 10 }, (_, index) => ({
    id: `skill-${index + 1}`,
    name: `Skill ${index + 1}`,
    description: `Private body ${index + 1}`,
    scope: index % 2 ? 'project' : 'system',
  }))
  const messages = assistantThreadItemsToMessages([{
    id: 'user-skills',
    turnID: 'run-skills',
    type: 'userMessage',
    status: 'completed',
    content: 'Use the selected skills.',
    data: {
      skills: [skills[0], null, { id: '', name: 'invalid', description: '', scope: 'project' }, skills[0], ...skills.slice(1)],
    },
    sequence: 1,
    createdAt: '2026-08-02T17:42:09Z',
  }], 'demo')

  assert.deepEqual(messages[0].metadata.assistantSkills.map(({ id, name, scope }) => ({ id, name, scope })), skills.slice(0, 8).map(({ id, name, scope }) => ({ id, name, scope })))
  assert.equal(messages[0].metadata.assistantSkills[0].description, 'Private body 1')
  assert.equal(projectAssistantSkills(skills).length, 8)
  assert.deepEqual(assistantThreadItemsToMessages([{
    id: 'legacy-user', type: 'userMessage', status: 'completed', content: 'No selection', sequence: 1,
    createdAt: '2026-08-02T17:42:09Z',
  }], 'demo')[0].metadata, {})
})

test('projects typed commentary items without erasing the owner trace', async () => {
  const { assistantThreadItemsToMessages, assistantThreadItemsToRuns } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'commentary-assistant-1-2', turnID: 'run-1', type: 'agentMessage', phase: 'commentary', status: 'completed',
      assistantMessageID: 'assistant-1', content: 'I found the relevant files.', sequence: 2,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'tool-1', turnID: 'run-1', type: 'dynamicToolCall', status: 'completed', assistantMessageID: 'assistant-1',
      data: { id: 'call-1', kind: 'inspect', status: 'succeeded', title: 'Read files', severity: 'normal', sequence: 1 }, sequence: 3,
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'assistant-1', turnID: 'run-1', type: 'agentMessage', phase: 'final_answer', status: 'completed',
      assistantMessageID: 'assistant-1', content: 'Here is the answer.', mode: 'default', revision: 4,
      data: { assistantProgress: { version: 1, messages: ['I found the relevant files.'], messageSequences: [2], workedDurationMs: 1_200 } },
      sequence: 4, createdAt: '2026-08-02T17:42:09Z',
    },
  ], 'demo')

  assert.deepEqual(messages.map(({ id }) => id), ['commentary-assistant-1-2', 'assistant-1'])
  assert.equal(messages[0].metadata.assistantPhase, 'commentary')
  assert.equal(messages[0].content, 'I found the relevant files.')
  assert.equal(messages[1].metadata.assistantPhase, 'final_answer')
  assert.equal(messages[1].metadata.assistantActionFeed[0].id, 'call-1')
  assert.deepEqual(messages[1].metadata.assistantProgress.messages, ['I found the relevant files.'])
  assert.deepEqual(messages[1].metadata.assistantProgress.messageSequences, [2])
  assert.equal(messages[1].metadata.assistantProgress.workedDurationMs, 1_200)
  assert.equal(assistantThreadItemsToRuns([
    { id: 'commentary-assistant-1-2', turnID: 'run-1', type: 'agentMessage', phase: 'commentary', status: 'completed', assistantMessageID: 'assistant-1', sequence: 2, createdAt: '2026-08-02T17:42:09Z' },
    { id: 'assistant-1', turnID: 'run-1', type: 'agentMessage', phase: 'final_answer', status: 'completed', assistantMessageID: 'assistant-1', sequence: 4, createdAt: '2026-08-02T17:42:09Z' },
  ])['run-1'].activeMessageID, 'assistant-1')
})

test('keeps terminal trace ordering and response surfaces after commentary collapse', async () => {
  const { assistantThreadItemsToMessages, hideCommentaryRepresentedInTrace } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const { buildAssistantTrace } = await vite.ssrLoadModule('/src/assistantTrace.ts')
  const items = [
    {
      id: 'commentary-assistant-2-2', turnID: 'run-2', type: 'agentMessage', phase: 'commentary', status: 'completed',
      assistantMessageID: 'assistant-2', content: 'I am mapping the project.', sequence: 2,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'tool-2-1', turnID: 'run-2', type: 'dynamicToolCall', status: 'completed', assistantMessageID: 'assistant-2',
      data: { id: 'call-2-1', kind: 'inspect', status: 'succeeded', title: 'Read project', severity: 'normal', sequence: 3 }, sequence: 3,
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'commentary-assistant-2-4', turnID: 'run-2', type: 'agentMessage', phase: 'commentary', status: 'completed',
      assistantMessageID: 'assistant-2', content: 'I found the edit seam.', sequence: 4,
      createdAt: '2026-08-02T17:42:11Z',
    },
    {
      id: 'tool-2-2', turnID: 'run-2', type: 'dynamicToolCall', status: 'completed', assistantMessageID: 'assistant-2',
      data: { id: 'call-2-2', kind: 'inspect', status: 'succeeded', title: 'Check tests', severity: 'normal', sequence: 5 }, sequence: 5,
      createdAt: '2026-08-02T17:42:12Z',
    },
    {
      id: 'plan-run-2', turnID: 'run-2', type: 'plan', status: 'completed', assistantMessageID: 'assistant-2',
      data: { steps: [{ content: 'Inspect', status: 'completed' }] }, sequence: 5,
      createdAt: '2026-08-02T17:42:12Z',
    },
    {
      id: 'assistant-2', turnID: 'run-2', type: 'agentMessage', phase: 'final_answer', status: 'completed',
      assistantMessageID: 'assistant-2', content: 'The answer is ready.',
      data: {
        assistantProgress: {
          version: 1,
          messages: ['I am mapping the project.', 'I found the edit seam.'],
          messageSequences: [2, 4],
          workedDurationMs: 2_400,
        },
      },
      sequence: 6, createdAt: '2026-08-02T17:42:13Z',
    },
  ]

  const messages = assistantThreadItemsToMessages(items, 'demo')
  const terminal = messages.find(({ id }) => id === 'assistant-2')
  assert.ok(terminal)
  assert.equal(terminal.content, 'The answer is ready.')
  assert.deepEqual(terminal.metadata.assistantPlan, { steps: [{ content: 'Inspect', status: 'completed' }] })
  assert.deepEqual(buildAssistantTrace(terminal.metadata.assistantProgress, terminal.metadata.assistantActionFeed), [
    { kind: 'progress', key: 'progress-0', message: 'I am mapping the project.' },
    { kind: 'actions', key: 'actions-0', items: [terminal.metadata.assistantActionFeed[0]] },
    { kind: 'progress', key: 'progress-1', message: 'I found the edit seam.' },
    { kind: 'actions', key: 'actions-1', items: [terminal.metadata.assistantActionFeed[1]] },
  ])
  assert.deepEqual(hideCommentaryRepresentedInTrace(messages).map(({ id }) => id), ['assistant-2'])
})

test('uses the same interleaved trace for an active typed-commentary owner', async () => {
  const { assistantThreadItemsToMessages, hideCommentaryRepresentedInTrace } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const { buildAssistantTrace } = await vite.ssrLoadModule('/src/assistantTrace.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-active', turnID: 'run-active', type: 'agentMessage', status: 'in_progress',
      assistantMessageID: 'assistant-active', content: '', sequence: 1,
      data: {
        assistantProgress: {
          version: 1,
          messages: ['I am mapping the project.', 'I found the edit seam.'],
          messageSequences: [2, 4],
          workedDurationMs: 1_200,
        },
      },
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'commentary-assistant-active-2', turnID: 'run-active', type: 'agentMessage', phase: 'commentary', status: 'completed', assistantMessageID: 'assistant-active',
      content: 'I am mapping the project.', sequence: 2,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'tool-active-1', turnID: 'run-active', type: 'dynamicToolCall', status: 'completed', assistantMessageID: 'assistant-active',
      data: { id: 'call-active-1', kind: 'inspect', status: 'succeeded', title: 'Read project', severity: 'normal', sequence: 3 },
      sequence: 3, createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'commentary-assistant-active-4', turnID: 'run-active', type: 'agentMessage', phase: 'commentary', status: 'completed', assistantMessageID: 'assistant-active',
      content: 'I found the edit seam.', sequence: 4, createdAt: '2026-08-02T17:42:11Z',
    },
    {
      id: 'tool-active-2', turnID: 'run-active', type: 'dynamicToolCall', status: 'in_progress', assistantMessageID: 'assistant-active',
      data: { id: 'call-active-2', kind: 'run', status: 'running', title: 'Run checks', severity: 'normal', sequence: 5 },
      sequence: 5, createdAt: '2026-08-02T17:42:12Z',
    },
  ], 'demo')

  const visible = hideCommentaryRepresentedInTrace(messages)
  assert.deepEqual(visible.map(({ id }) => id), ['assistant-active'])
  const owner = visible[0]
  assert.deepEqual(buildAssistantTrace(owner.metadata.assistantProgress, owner.metadata.assistantActionFeed), [
    { kind: 'progress', key: 'progress-0', message: 'I am mapping the project.' },
    { kind: 'actions', key: 'actions-0', items: [owner.metadata.assistantActionFeed[0]] },
    { kind: 'progress', key: 'progress-1', message: 'I found the edit seam.' },
    { kind: 'actions', key: 'actions-1', items: [owner.metadata.assistantActionFeed[1]] },
  ])
})

test('materializes owner-start commentary and tool events into one canonical live trace', async () => {
  const { assistantThreadItemsToMessages, hideCommentaryRepresentedInTrace } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const { buildAssistantTrace } = await vite.ssrLoadModule('/src/assistantTrace.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-live', turnID: 'run-live', type: 'agentMessage', status: 'in_progress',
      assistantMessageID: 'assistant-live', content: '', sequence: 1,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'commentary-live-2', turnID: 'run-live', type: 'agentMessage', phase: 'commentary', status: 'completed',
      assistantMessageID: 'assistant-live', content: 'I am checking the project.', sequence: 2,
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'tool-live-3', turnID: 'run-live', type: 'dynamicToolCall', status: 'completed',
      assistantMessageID: 'assistant-live',
      data: { id: 'call-live-3', kind: 'inspect', status: 'succeeded', title: 'Read project', severity: 'normal', sequence: 3 },
      sequence: 3, createdAt: '2026-08-02T17:42:11Z',
    },
  ], 'demo')

  const visible = hideCommentaryRepresentedInTrace(messages)
  assert.deepEqual(visible.map(({ id }) => id), ['assistant-live'])
  const owner = visible[0]
  assert.deepEqual(owner.metadata.assistantProgress.messages, ['I am checking the project.'])
  assert.deepEqual(buildAssistantTrace(owner.metadata.assistantProgress, owner.metadata.assistantActionFeed), [
    { kind: 'progress', key: 'progress-0', message: 'I am checking the project.' },
    { kind: 'actions', key: 'actions-0', items: [owner.metadata.assistantActionFeed[0]] },
  ])
})

test('preserves repeated commentary prose at distinct sequence positions', async () => {
  const { assistantThreadItemsToMessages, hideCommentaryRepresentedInTrace } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-repeat', turnID: 'run-repeat', type: 'agentMessage', status: 'in_progress',
      assistantMessageID: 'assistant-repeat', content: '', sequence: 1,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'commentary-repeat-2', turnID: 'run-repeat', type: 'agentMessage', phase: 'commentary', status: 'completed',
      assistantMessageID: 'assistant-repeat', content: 'Checking again.', sequence: 2,
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'commentary-repeat-4', turnID: 'run-repeat', type: 'agentMessage', phase: 'commentary', status: 'completed',
      assistantMessageID: 'assistant-repeat', content: 'Checking again.', sequence: 4,
      createdAt: '2026-08-02T17:42:12Z',
    },
  ], 'demo')

  const owner = messages.find(({ id }) => id === 'assistant-repeat')
  assert.deepEqual(owner.metadata.assistantProgress.messages, ['Checking again.', 'Checking again.'])
  assert.deepEqual(owner.metadata.assistantProgress.messageSequences, [2, 4])
  assert.deepEqual(hideCommentaryRepresentedInTrace(messages).map(({ id }) => id), ['assistant-repeat'])
})

test('deduplicates commentary lifecycle cursors by the real item ID suffix', async () => {
  const { assistantThreadItemsToMessages, hideCommentaryRepresentedInTrace } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-lifecycle', turnID: 'run-lifecycle', type: 'agentMessage', status: 'in_progress',
      assistantMessageID: 'assistant-lifecycle', content: '', sequence: 1,
      createdAt: '2026-08-02T17:42:09Z',
    },
    // The payload sequence is zero in the live lifecycle; these values model
    // distinct SSE cursors after materialization. The item ID suffix is the
    // durable progress identity and must remain the only trace key.
    {
      id: 'commentary-assistant-lifecycle-7', turnID: 'run-lifecycle', type: 'agentMessage', phase: 'commentary', status: 'in_progress',
      assistantMessageID: 'assistant-lifecycle', content: 'Checking the project.', sequence: 12,
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'commentary-assistant-lifecycle-7', turnID: 'run-lifecycle', type: 'agentMessage', phase: 'commentary', status: 'completed',
      assistantMessageID: 'assistant-lifecycle', content: 'Checking the project.', sequence: 13,
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'commentary-assistant-lifecycle-9', turnID: 'run-lifecycle', type: 'agentMessage', phase: 'commentary', status: 'completed',
      assistantMessageID: 'assistant-lifecycle', content: 'Checking the project.', sequence: 14,
      createdAt: '2026-08-02T17:42:11Z',
    },
    {
      id: 'assistant-lifecycle', turnID: 'run-lifecycle', type: 'agentMessage', phase: 'final_answer', status: 'completed',
      assistantMessageID: 'assistant-lifecycle', content: 'Done.', sequence: 15,
      data: { assistantProgress: { version: 1, messages: ['Checking the project.'], messageSequences: [7], workedDurationMs: 900 } },
      createdAt: '2026-08-02T17:42:12Z',
    },
  ], 'demo')

  const owner = messages.find(({ id }) => id === 'assistant-lifecycle')
  assert.deepEqual(owner.metadata.assistantProgress.messages, ['Checking the project.', 'Checking the project.'])
  assert.deepEqual(owner.metadata.assistantProgress.messageSequences, [7, 9])
  assert.deepEqual(hideCommentaryRepresentedInTrace(messages).map(({ id }) => id), ['assistant-lifecycle'])
})

test('keeps commentary visible until its owner trace contains the same prose', async () => {
  const { hideCommentaryRepresentedInTrace } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = [
    {
      id: 'commentary-active-1', projectID: 'demo', role: 'assistant', content: 'Still working.',
      metadata: { assistantPhase: 'commentary', assistantMessageID: 'assistant-active', assistantCommentarySequence: 2 },
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'assistant-active', projectID: 'demo', role: 'assistant', content: '',
      metadata: {
        assistantStatus: 'running',
        assistantMessageID: 'assistant-active',
        assistantProgress: { version: 1, messages: ['An earlier update.'], messageSequences: [1], workedDurationMs: 200 },
      },
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'commentary-done-1', projectID: 'demo', role: 'assistant', content: 'Finished the checks.',
      metadata: { assistantPhase: 'commentary', assistantMessageID: 'assistant-done', assistantCommentarySequence: 2 },
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'assistant-done', projectID: 'demo', role: 'assistant', content: 'Done.',
      metadata: {
        assistantStatus: 'completed',
        assistantMessageID: 'assistant-done',
        assistantProgress: { version: 1, messages: ['Finished the checks.'], messageSequences: [2], workedDurationMs: 400 },
      },
      createdAt: '2026-08-02T17:42:10Z',
    },
  ]

  assert.deepEqual(hideCommentaryRepresentedInTrace(messages).map(({ id }) => id), [
    'commentary-active-1',
    'assistant-active',
    'assistant-done',
  ])
})

test('deduplicates scoped live tool items by their raw action ID across status updates', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-1', turnID: 'run-1', type: 'agentMessage', status: 'in_progress',
      assistantMessageID: 'assistant-1', mode: 'default', revision: 1, content: '', sequence: 1,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'tool-assistant-1-call-1', turnID: 'run-1', type: 'dynamicToolCall', status: 'in_progress',
      assistantMessageID: 'assistant-1', data: { id: 'call-1', kind: 'run', status: 'running', title: 'Run command', severity: 'normal', sequence: 1 }, sequence: 2,
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'tool-assistant-1-call-1', turnID: 'run-1', type: 'dynamicToolCall', status: 'completed',
      assistantMessageID: 'assistant-1', data: { id: 'call-1', kind: 'run', status: 'succeeded', title: 'Run command', severity: 'normal', sequence: 1 }, sequence: 3,
      createdAt: '2026-08-02T17:42:11Z',
    },
  ], 'demo')

  const actionFeed = messages[0].metadata.assistantActionFeed
  assert.equal(actionFeed.length, 1)
  assert.equal(actionFeed[0].id, 'call-1')
  assert.equal(actionFeed[0].status, 'succeeded')
})

test('projects linked retrying and recovered actions without dropping recovery metadata', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-recovery', turnID: 'run-recovery', type: 'agentMessage', status: 'in_progress',
      assistantMessageID: 'assistant-recovery', content: '', sequence: 1,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'tool-prior', turnID: 'run-recovery', type: 'dynamicToolCall', status: 'completed', assistantMessageID: 'assistant-recovery',
      data: {
        id: 'prior-1', kind: 'edit', status: 'failed', title: 'Edit failed', severity: 'error', sequence: 1,
        diagnostic: { category: 'validation', message: 'The source is stale.', referenceID: 'action-prior', code: 'stale_source', operation: 'edit_file', path: 'src/App.vue', guidance: 'Read and retry.' },
      }, sequence: 2, createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'tool-retry', turnID: 'run-recovery', type: 'dynamicToolCall', status: 'in_progress', assistantMessageID: 'assistant-recovery',
      data: { id: 'retry-1', kind: 'edit', status: 'retrying', title: 'Retrying file update', severity: 'attention', sequence: 2, recoveryOf: 'prior-1' },
      sequence: 3, createdAt: '2026-08-02T17:42:11Z',
    },
    {
      id: 'tool-recovered', turnID: 'run-recovery', type: 'dynamicToolCall', status: 'completed', assistantMessageID: 'assistant-recovery',
      data: { id: 'retry-1', kind: 'edit', status: 'recovered', title: 'Recovered file update', severity: 'normal', sequence: 2, recoveryOf: 'prior-1' },
      sequence: 4, createdAt: '2026-08-02T17:42:12Z',
    },
  ], 'demo')

  const actions = messages[0].metadata.assistantActionFeed
  assert.equal(actions.length, 2)
  assert.equal(actions[0].status, 'failed')
  assert.equal(actions[1].status, 'recovered')
  assert.equal(actions[1].recoveryOf, 'prior-1')
  assert.equal(actions[0].diagnostic.code, 'stale_source')
})

test('binds steered activity to its explicit assistant segment and preserves historical fallback order', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const item = (overrides) => ({
    turnID: 'run-steered', status: 'completed', sequence: 1, createdAt: '2026-08-02T17:42:09Z', ...overrides,
  })
  const messages = assistantThreadItemsToMessages([
    item({ id: 'user-1', type: 'userMessage', content: 'build it', sequence: 1 }),
    item({ id: 'assistant-1', assistantMessageID: 'assistant-1', type: 'agentMessage', mode: 'default', revision: 1, content: 'first', sequence: 2 }),
    item({
      id: 'old-tool', assistantMessageID: 'assistant-1', type: 'dynamicToolCall', sequence: 3,
      data: { id: 'old-tool', kind: 'inspect', status: 'succeeded', title: 'Read old segment', severity: 'normal', sequence: 1 },
    }),
    item({
      id: 'old-plan', assistantMessageID: 'assistant-1', type: 'plan', sequence: 4,
      data: { steps: [{ content: 'Old plan', status: 'completed' }] },
    }),
    item({ id: 'assistant-2', assistantMessageID: 'assistant-2', type: 'agentMessage', mode: 'plan', revision: 2, status: 'in_progress', content: 'replacement', sequence: 5 }),
    item({
      id: 'new-tool', assistantMessageID: 'assistant-2', type: 'dynamicToolCall', sequence: 6,
      data: { id: 'new-tool', kind: 'run', status: 'running', title: 'Run new segment', severity: 'normal', sequence: 2 },
    }),
    item({
      id: 'new-plan', assistantMessageID: 'assistant-2', type: 'plan', sequence: 7,
      data: { steps: [{ content: 'New plan', status: 'in_progress' }] },
    }),
    // Legacy activity has no segment field; it belongs to the preceding agent.
    item({
      id: 'legacy-new-tool', type: 'dynamicToolCall', sequence: 8,
      data: { id: 'legacy-new-tool', kind: 'inspect', status: 'succeeded', title: 'Legacy new segment', severity: 'normal', sequence: 3 },
    }),
  ], 'demo')

  const first = messages.find((message) => message.id === 'assistant-1')
  const replacement = messages.find((message) => message.id === 'assistant-2')
  assert.deepEqual(first.metadata.assistantActionFeed.map(({ id }) => id), ['old-tool'])
  assert.deepEqual(first.metadata.assistantPlan.steps.map(({ content }) => content), ['Old plan'])
  assert.deepEqual(replacement.metadata.assistantActionFeed.map(({ id }) => id), ['new-tool', 'legacy-new-tool'])
  assert.deepEqual(replacement.metadata.assistantPlan.steps.map(({ content }) => content), ['New plan'])
})

test('hydrates terminal run state, errors, and completed plan mode from durable items', async () => {
  const { assistantThreadItemsToMessages, assistantThreadItemsToRuns } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-failed', turnID: 'run-failed', type: 'agentMessage', status: 'failed',
      assistantMessageID: 'assistant-failed', mode: 'default', revision: 6,
      error: { message: 'provider failed', errorInfo: 'provider_error' }, content: 'Nope', sequence: 2,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'assistant-interrupted', turnID: 'run-interrupted', type: 'agentMessage', status: 'interrupted',
      assistantMessageID: 'assistant-interrupted', mode: 'default', revision: 4, content: 'Stopped', sequence: 4,
      createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'assistant-plan', turnID: 'run-plan', type: 'agentMessage', status: 'completed',
      assistantMessageID: 'assistant-plan', mode: 'plan', revision: 8, content: 'Plan ready', sequence: 6,
      createdAt: '2026-08-02T17:42:11Z',
    },
  ], 'demo')
  const runs = assistantThreadItemsToRuns([
    {
      id: 'assistant-failed', turnID: 'run-failed', type: 'agentMessage', status: 'failed', assistantMessageID: 'assistant-failed', mode: 'default', revision: 6,
      error: { message: 'provider failed', errorInfo: 'provider_error' }, sequence: 2, createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'assistant-interrupted', turnID: 'run-interrupted', type: 'agentMessage', status: 'interrupted', assistantMessageID: 'assistant-interrupted', mode: 'default', revision: 4,
      sequence: 4, createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'assistant-plan', turnID: 'run-plan', type: 'agentMessage', status: 'completed', assistantMessageID: 'assistant-plan', mode: 'plan', revision: 8,
      sequence: 6, createdAt: '2026-08-02T17:42:11Z',
    },
  ])

  assert.equal(messages.find((message) => message.id === 'assistant-failed').metadata.assistantStatus, 'failed')
  assert.equal(messages.find((message) => message.id === 'assistant-interrupted').metadata.assistantStatus, 'interrupted')
  assert.equal(runs['run-failed'].status, 'failed')
  assert.deepEqual(runs['run-failed'].error, { message: 'provider failed', errorInfo: 'provider_error' })
  assert.equal(runs['run-interrupted'].status, 'interrupted')
  assert.equal(runs['run-plan'].mode, 'plan')
  assert.equal(runs['run-plan'].status, 'completed')
  assert.equal(runs['run-plan'].activeMessageID, 'assistant-plan')
})

test('retains persisted plan snapshots for interrupted and failed reload owners', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-interrupted-plan', turnID: 'run-interrupted-plan', type: 'agentMessage', status: 'interrupted',
      assistantMessageID: 'assistant-interrupted-plan', content: 'Stopped while implementing.', sequence: 1,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'plan-interrupted', turnID: 'run-interrupted-plan', type: 'plan', status: 'completed',
      assistantMessageID: 'assistant-interrupted-plan', data: {
        steps: [
          { content: 'Inspect the project', status: 'completed' },
          { content: 'Apply the change', status: 'pending' },
        ],
      }, sequence: 2, createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'assistant-failed-plan', turnID: 'run-failed-plan', type: 'agentMessage', status: 'failed',
      assistantMessageID: 'assistant-failed-plan', content: 'The change failed.', sequence: 3,
      createdAt: '2026-08-02T17:42:11Z',
    },
    {
      id: 'plan-failed', turnID: 'run-failed-plan', type: 'plan', status: 'completed',
      assistantMessageID: 'assistant-failed-plan', data: {
        steps: [
          { content: 'Inspect the project', status: 'completed' },
          { content: 'Run the checks', status: 'in_progress' },
        ],
      }, sequence: 4, createdAt: '2026-08-02T17:42:12Z',
    },
  ], 'demo')

  assert.equal(messages.find(({ id }) => id === 'assistant-interrupted-plan').metadata.assistantStatus, 'interrupted')
  assert.deepEqual(messages.find(({ id }) => id === 'assistant-interrupted-plan').metadata.assistantPlan.steps.map(({ status }) => status), ['completed', 'pending'])
  assert.equal(messages.find(({ id }) => id === 'assistant-failed-plan').metadata.assistantStatus, 'failed')
  assert.deepEqual(messages.find(({ id }) => id === 'assistant-failed-plan').metadata.assistantPlan.steps.map(({ status }) => status), ['completed', 'in_progress'])
})

test('projects the latest persisted plan snapshot for a terminal owner', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const messages = assistantThreadItemsToMessages([
    {
      id: 'assistant-terminal-plan', turnID: 'run-terminal-plan', type: 'agentMessage', status: 'completed',
      assistantMessageID: 'assistant-terminal-plan', content: 'Finished the work.', sequence: 1,
      createdAt: '2026-08-02T17:42:09Z',
    },
    {
      id: 'plan-terminal-1', turnID: 'run-terminal-plan', type: 'plan', status: 'completed',
      assistantMessageID: 'assistant-terminal-plan', data: {
        steps: [{ content: 'Inspect the project', status: 'in_progress' }],
      }, sequence: 2, createdAt: '2026-08-02T17:42:10Z',
    },
    {
      id: 'plan-terminal-2', turnID: 'run-terminal-plan', type: 'plan', status: 'completed',
      assistantMessageID: 'assistant-terminal-plan', data: {
        steps: [
          { content: 'Inspect the project', status: 'completed' },
          { content: 'Verify the preview', status: 'completed' },
        ],
      }, sequence: 3, createdAt: '2026-08-02T17:42:11Z',
    },
  ], 'demo')

  assert.deepEqual(messages[0].metadata.assistantPlan, {
    steps: [
      { content: 'Inspect the project', status: 'completed' },
      { content: 'Verify the preview', status: 'completed' },
    ],
  })
})

test('retains the first replacement delta when a stale list projection arrives after the stream', async () => {
  const { mergeAssistantThreadMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const current = [{ id: 'assistant-2', projectID: 'demo', role: 'assistant', content: 'first replacement delta', metadata: { assistantRevision: 2 }, createdAt: '2026-08-02T17:42:09Z' }]
  const projected = [{ id: 'assistant-2', projectID: 'demo', role: 'assistant', content: '', metadata: { assistantRevision: 2 }, createdAt: '2026-08-02T17:42:09Z' }]
  assert.equal(mergeAssistantThreadMessages(current, projected)[0].content, 'first replacement delta')
})

test('stale thread refresh keeps newer live commentary progress and actions', async () => {
  const { mergeAssistantThreadMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const current = [{
    id: 'assistant-live', projectID: 'demo', role: 'assistant', content: '',
    metadata: {
      assistantRevision: 2,
      assistantProgress: { version: 1, messages: ['Live update'], messageSequences: [4], workedDurationMs: 900 },
      assistantActionFeed: [{ id: 'call-live', kind: 'run', status: 'running', title: 'Run checks', severity: 'normal', sequence: 5 }],
    },
    createdAt: '2026-08-02T17:42:09Z',
  }]
  const projected = [{
    id: 'assistant-live', projectID: 'demo', role: 'assistant', content: '',
    metadata: {
      assistantRevision: 2,
      assistantProgress: { version: 1, messages: [], messageSequences: [], workedDurationMs: 0 },
      assistantActionFeed: [],
    },
    createdAt: '2026-08-02T17:42:09Z',
  }]
  const merged = mergeAssistantThreadMessages(current, projected)[0]
  assert.deepEqual(merged.metadata.assistantProgress.messages, ['Live update'])
  assert.deepEqual(merged.metadata.assistantProgress.messageSequences, [4])
  assert.deepEqual(merged.metadata.assistantActionFeed.map(({ id }) => id), ['call-live'])
})

test('a stale projection cannot roll back the status of a same-ID action from a newer message revision', async () => {
  const { mergeAssistantThreadMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const current = [{
    id: 'assistant-action-status', projectID: 'demo', role: 'assistant', content: '',
    metadata: {
      assistantRevision: 9,
      assistantActionFeed: [{ id: 'call-status', kind: 'run', status: 'succeeded', title: 'Checks passed', severity: 'normal', sequence: 7 }],
    },
    createdAt: '2026-08-02T17:42:09Z',
  }]
  const projected = [{
    id: 'assistant-action-status', projectID: 'demo', role: 'assistant', content: '',
    metadata: {
      assistantRevision: 8,
      assistantActionFeed: [{ id: 'call-status', kind: 'run', status: 'running', title: 'Running checks', severity: 'normal', sequence: 6 }],
    },
    createdAt: '2026-08-02T17:42:09Z',
  }]

  const merged = mergeAssistantThreadMessages(current, projected)[0]
  assert.deepEqual(merged.metadata.assistantActionFeed, current[0].metadata.assistantActionFeed)
})
