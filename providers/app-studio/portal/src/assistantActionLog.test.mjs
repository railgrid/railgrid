import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vue from '@vitejs/plugin-vue'
import { createServer } from 'vite'
import { createSSRApp, createRenderer, nextTick, reactive, ssrContextKey, unref } from 'vue'
import { renderToString } from 'vue/server-renderer'
import ts from 'typescript'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', cacheDir: '/tmp/railgrid-vite-assistant-action-log', configFile: false, plugins: [vue()], server: { hmr: false, middlewareMode: true } })
})
test.after(async () => vite?.close())

test('renders a compact bounded log without execution mechanics', async () => {
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const html = await renderToString(createSSRApp(AssistantActionLog, {
    messageId: 'assistant-1',
    items: [
      { id: 'read-1', kind: 'inspect', status: 'succeeded', title: 'Read project file', target: 'src/App.vue', severity: 'normal' },
      { id: 'check-1', kind: 'run', status: 'succeeded', title: 'Checked development preview', outcome: 'Ready', severity: 'normal' },
    ],
  }))
  assert.match(html, /2 actions/)
  assert.match(html, /aria-expanded="false"/)
  assert.match(html, /aria-controls="app-studio-assistant-actions-assistant-1"/)
  assert.match(html, /Inspected the project/)
  assert.match(html, /Ran checks/)
  assert.doesNotMatch(html, /action-chain-fade/)
  assert.doesNotMatch(html, /tool call|tool result|args:|offset|limit|read_file/)
  assert.doesNotMatch(html, /rounded-xl border border-border-subtle bg-surface/)
})

test('renders exec_command as the same compact action row with details collapsed', async () => {
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const html = await renderToString(createSSRApp(AssistantActionLog, {
    messageId: 'assistant-exec',
    items: [{
      id: 'exec-1',
      kind: 'run',
      status: 'succeeded',
      title: 'Ran command',
      outcome: 'exit 0',
      severity: 'normal',
      exec: {
        component: 'workspace',
        argv: ['go', 'test', './...'],
        workdir: 'backend',
        status: 'succeeded',
        exitCode: 0,
        durationMs: 1250,
        stdout: ['COMMAND_OUTPUT'],
        stderr: ['COMMAND_WARNING'],
      },
    }],
  }))
  assert.match(html, /Ran commands/)
  assert.match(html, /Ran go test \.\/\.\.\./)
  assert.match(html, /aria-expanded="false"/)
  assert.match(html, /aria-controls="app-studio-assistant-actions-assistant-exec-exec-1-exec"/)
  assert.doesNotMatch(html, /Sanitized argv|Relative cwd|Duration|Exit status|stdout|stderr|COMMAND_OUTPUT|COMMAND_WARNING|Shell/)
  assert.doesNotMatch(html, /bg-surface-raised\/70 p-2\.5/)
})

test('keeps the studio-sandbox shell disclosure inside the click-only expansion', async () => {
  const { default: AssistantExecDetails } = await vite.ssrLoadModule('/src/AssistantExecDetails.vue')
  const html = await renderToString(createSSRApp(AssistantExecDetails, {
    variant: 'activity',
    exec: {
      component: 'workspace',
      argv: ['go', 'test', './...'],
      workdir: 'backend',
      status: 'failed',
      exitCode: 1,
      durationMs: 1250,
      stdout: ['COMMAND_OUTPUT'],
      stderr: ['COMMAND_WARNING'],
    },
  }))
  assert.match(html, /Shell/)
  assert.match(html, /\$ <\/span>go test \.\/\.\.\./)
  assert.match(html, /1\.3 s/)
  assert.match(html, /COMMAND_OUTPUT/)
  assert.match(html, /COMMAND_WARNING/)
  assert.match(html, /Failed · exit 1/)
  assert.doesNotMatch(html, /Direct command|Sanitized argv|Relative cwd|Exit status|stdout|stderr|backend/)
})

test('uses nested exec state labels and keeps each disclosure collapsed by default', async () => {
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const states = [
    ['running', 'running', 'Running'],
    ['success', 'succeeded', 'Ran'],
    ['failure', 'failed', 'Failed'],
    ['cancel', 'canceled', 'Canceled'],
    ['timeout', 'timed_out', 'Timed out'],
    ['blocked', 'blocked', 'Blocked'],
  ]
  const html = await renderToString(createSSRApp(AssistantActionLog, {
    messageId: 'assistant-exec-states',
    items: states.map(([id, status]) => ({
      id: `exec-${id}`,
      kind: 'run',
      // The outer action is settled successfully in this fixture. The nested
      // command result is the authoritative state shown to the user.
      status: 'succeeded',
      title: 'Ran command',
      severity: 'normal',
      exec: { argv: ['npm', 'run', id], status },
    })),
  }))
  for (const [id, , label] of states) {
    assert.match(html, new RegExp(`${label} npm run ${id}`))
    assert.match(html, new RegExp(`aria-expanded="false"[^>]+aria-controls="app-studio-assistant-actions-assistant-exec-states-exec-${id}-exec"`))
  }
  assert.doesNotMatch(html, /Shell|Direct command|stdout|stderr/)
})

test('keeps structured failure diagnostics out of the user-visible action history', async () => {
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const html = await renderToString(createSSRApp(AssistantActionLog, {
    messageId: 'assistant-2',
    items: [{
      id: 'failed-1',
      kind: 'run',
      status: 'failed',
      title: 'Preview check failed',
      outcome: 'Development server did not become ready',
      severity: 'error',
      diagnostic: { category: 'timeout', message: 'Preview readiness timed out.', referenceID: 'run-2' },
    }],
  }))
  assert.match(html, /aria-expanded="false"[^>]+aria-controls="app-studio-assistant-actions-/)
  assert.match(html, /text-danger/)
  assert.match(html, /class="k-ai-activity__panel"/)
  assert.doesNotMatch(html, /max-h-\[min\(40vh,320px\)\]/)
  assert.doesNotMatch(html, /class="grid max-h-\[min\(40vh,320px\)\] overflow-auto"/)
  assert.match(html, /Failed:/)
  assert.match(html, /Preview check failed/)
  assert.match(html, /Development server did not become ready/)
  assert.doesNotMatch(html, /Details|Preview readiness timed out|run-2|failed-1-diagnostic|Copy diagnostic info|rawError|arguments/)
})

test('renders preview assertion mismatches as attention rather than runtime errors', async () => {
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const html = await renderToString(createSSRApp(AssistantActionLog, {
    messageId: 'assistant-preview-assertion',
    items: [{
      id: 'preview-assertion-1',
      kind: 'run',
      status: 'failed',
      title: 'Preview assertions did not match',
      severity: 'attention',
      diagnostic: {
        category: 'validation',
        code: 'preview_assertion_mismatch',
        operation: 'inspect_development_preview',
        message: '3 of 6 preview assertions did not match.',
        guidance: 'Review the rendered accessibility evidence and inspect again.',
        referenceID: 'action-preview',
      },
    }],
  }))
  assert.match(html, /Preview assertions did not match/)
  assert.match(html, /Needs attention:/)
  assert.match(html, /text-warning/)
  assert.doesNotMatch(html, /text-danger/)
})

test('renders retrying and recovered lifecycle labels without exposing correlation IDs', async () => {
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const html = await renderToString(createSSRApp(AssistantActionLog, {
    messageId: 'assistant-recovery',
    items: [
      { id: 'retry-1', kind: 'edit', status: 'retrying', title: 'Retrying file update', severity: 'attention', recoveryOf: 'prior-1' },
      { id: 'recovered-1', kind: 'edit', status: 'recovered', title: 'Recovered file update', severity: 'normal', recoveryOf: 'prior-1' },
    ],
  }))
  assert.match(html, /Retrying file update/)
  assert.match(html, /Recovered file update/)
  assert.match(html, /Retrying:/)
  assert.match(html, /Recovered:/)
  assert.doesNotMatch(html, /prior-1/)
})

test('keeps active work collapsed with a visible progress summary', async () => {
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const html = await renderToString(createSSRApp(AssistantActionLog, {
    messageId: 'assistant-active',
    items: [
      { id: 'search-1', kind: 'inspect', status: 'running', title: 'Searching project files', target: 'src', severity: 'normal' },
      { id: 'search-2', kind: 'inspect', status: 'succeeded', title: 'Searched for App.vue', target: 'src/App.vue', severity: 'normal' },
    ],
  }))
  assert.match(html, /aria-expanded="false"[^>]+aria-controls="app-studio-assistant-actions-/)
  assert.match(html, /Inspecting the project/)
  assert.match(html, /Searching project files/)
  assert.match(html, /Searched for App.vue/)
  assert.match(html, /animate-spin/)
})

test('renders image model-input actions with image-specific group labels and icon', async () => {
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const viewing = await renderToString(createSSRApp(AssistantActionLog, {
    messageId: 'assistant-image-viewing',
    items: [{
      id: 'image-view-1', kind: 'inspect', mediaKind: 'image', status: 'running',
      title: 'Viewing image', target: 'screen.png', severity: 'normal', sequence: 1,
    }],
  }))
  assert.match(viewing, /Viewing image/)
  assert.doesNotMatch(viewing, /Inspecting the project/)
  assert.match(viewing, /lucide-image/)

  const viewed = await renderToString(createSSRApp(AssistantActionLog, {
    messageId: 'assistant-image-viewed',
    items: [{
      id: 'image-view-1', kind: 'inspect', mediaKind: 'image', status: 'succeeded',
      title: 'Viewed image', target: 'screen.png', severity: 'normal', sequence: 1,
    }],
  }))
  assert.match(viewed, /Viewed image/)
  assert.doesNotMatch(viewed, /Inspected the project/)
  assert.match(viewed, /lucide-image/)
})

test('aligns group headers and child actions while bounding long call chains with a fade', async () => {
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const html = await renderToString(createSSRApp(AssistantActionLog, {
    messageId: 'assistant-long-chain',
    items: Array.from({ length: 8 }, (_, index) => ({
      id: `read-${index}`,
      kind: 'inspect',
      status: index === 7 ? 'running' : 'succeeded',
      title: `Read file ${index + 1}`,
      target: `src/file-${index + 1}.ts`,
      groupKey: 'inspect:files',
      groupTitle: 'Read files',
      severity: 'normal',
    })),
  }))
  assert.match(html, /k-ai-activity-feed__group-rows--scrollable/)
  const activityCSS = readFileSync(new URL('./agentkit/activity.css', import.meta.url), 'utf8')
  assert.match(activityCSS, /\.k-ai-activity-feed__group-rows--scrollable\s*\{[^}]*max-height: 240px;[^}]*overflow-y: auto;[^}]*mask-image:/)
  assert.doesNotMatch(html, /assistant-long-chain-inspect-\d+" class="ml-5/)
  assert.doesNotMatch(html, /class="mt-1 grid max-h-\[min\(40vh,320px\)\]/)
  assert.doesNotMatch(html, /max-h-\[min\(40vh,320px\)\] gap-1.5/)
  assert.match(html, /Read file 8/)
  assert.match(html, /Read files/)
})

test('keeps a streamed group panel identity when an earlier group appears', async () => {
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const target = {
    id: 'stable-read-1',
    kind: 'inspect',
    status: 'succeeded',
    title: 'Read project file',
    target: 'src/App.vue',
    groupKey: 'inspect:files',
    groupTitle: 'Read files',
    severity: 'normal',
  }
  const prefix = {
    id: 'streamed-prefix-1',
    kind: 'run',
    status: 'succeeded',
    title: 'Checked preview',
    outcome: 'Ready',
    groupKey: 'run:preview',
    groupTitle: 'Ran checks',
    severity: 'normal',
  }

  const panelFor = (html, label) => {
    const labelMarker = `k-ai-activity-feed__group-label">${label}</span>`
    const labelOffset = html.indexOf(labelMarker)
    assert.notEqual(labelOffset, -1, `missing group label: ${label}`)
    const buttonOffset = html.lastIndexOf('<button', labelOffset)
    const control = html.slice(buttonOffset, labelOffset).match(/aria-controls="([^"]+)"/)
    assert.ok(control, `missing group panel control: ${label}`)
    return control[1]
  }

  const initial = await renderToString(createSSRApp(AssistantActionLog, {
    messageId: 'assistant-streaming-groups',
    items: [target],
  }))
  const afterEarlierGroup = await renderToString(createSSRApp(AssistantActionLog, {
    messageId: 'assistant-streaming-groups',
    items: [prefix, target],
  }))

  assert.equal(panelFor(initial, 'Read files'), panelFor(afterEarlierGroup, 'Read files'))
  assert.match(panelFor(initial, 'Read files'), /stable-read-1/)
})


test('preserves user disclosure choices across tool starts, completion, and failures', async () => {
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const props = reactive({ messageId: 'lifecycle', items: [], stopping: false })
  // Mount the real setup in a Vue instance so lifecycle watchers run on updates.
  const renderer = createRenderer({
    createComment: () => ({}), insert() {}, remove() {},
    parentNode: () => null, nextSibling: () => null,
  })
  let state
  const app = renderer.createApp({
    setup() {
      state = AssistantActionLog.setup(props, { expose() {} })
      return () => null
    },
  })
  app.provide(ssrContextKey, {})
  app.mount({})
  try {
    const update = async (status, severity = 'normal') => {
      props.items = [{ id: 'tool', kind: 'run', status, severity, title: 'Build' }]
      await nextTick()
    }
    await update('running')
    assert.equal(unref(state.expanded), false)
    assert.equal(unref(state.hasBusyAction), true)
    state.toggleLog()
    await update('succeeded')
    assert.equal(unref(state.expanded), true)
    await update('running')
    assert.equal(unref(state.expanded), true)
    state.toggleLog()
    await update('succeeded')
    await update('running')
    assert.equal(unref(state.expanded), false)
    await update('failed', 'error')
    assert.equal(unref(state.expanded), false)
    assert.equal(unref(state.hasErrorAction), true)

    // Reproduce the missing path: append a NEW active call to a collapsed
    // completed group, rather than only changing one existing call's status.
    for (const exec of [false, true]) {
      const item = (id, status) => ({
        id, kind: 'run', status, severity: 'normal', title: 'Build',
        ...(exec ? { exec: { argv: ['npm', 'run', 'build'], status } } : {}),
      })
      props.items = [item('completed', 'succeeded')]
      await nextTick()
      const key = unref(state.activityGroups)[0].key
      if (unref(state.collapsedGroups).has(key)) state.toggleGroupByKey(key)
      state.toggleGroupByKey(key)
      for (const status of ['running', 'retrying', 'failed', 'succeeded']) {
        props.items = [item('completed', 'succeeded'), item('new-call', status)]
        await nextTick()
        const groups = unref(state.activityGroups)
        assert.equal(groups.length, 1, 'new calls must not split off an expanded group')
        assert.equal(groups[0].key, key)
        assert.equal(unref(state.collapsedGroups).has(key), true)
        assert.equal(groups[0].rows.length, 2)
      }
      state.toggleGroupByKey(key)
      props.items = [...props.items, item('another-call', 'running')]
      await nextTick()
      assert.equal(unref(state.activityGroups).length, 1)
      assert.equal(unref(state.activityGroups)[0].busy, true)
      assert.equal(unref(state.activityGroups)[0].rows.length, 3)
      assert.equal(unref(state.collapsedGroups).has(key), false)
    }
  } finally {
    app.unmount()
  }
})

test('the work section opens for active turns and collapses on completion, with manual control in each phase', () => {
  const source = readFileSync(new URL('./App.vue', import.meta.url), 'utf8')
  const implementation = source.match(/function assistantProgressExpanded\(message: ProjectMessageView\): boolean \{[\s\S]*?\n\}/)?.[0]
  assert.ok(implementation)
  const { outputText } = ts.transpileModule(implementation, { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const choices = { value: new Map() }
  const isExpanded = new Function('assistantProgressDisclosureChoices', 'assistantProgressClosed', `${outputText}; return assistantProgressExpanded`)(choices, message => message.closed)
  assert.equal(isExpanded({ id: 'turn', closed: false }), true)
  for (const expanded of [false, true]) {
    choices.value.set('turn', { closed: false, expanded })
    assert.equal(isExpanded({ id: 'turn', closed: false }), expanded)
    assert.equal(isExpanded({ id: 'turn', closed: true }), false)
  }
  choices.value.set('turn', { closed: true, expanded: true })
  assert.equal(isExpanded({ id: 'turn', closed: true }), true)
  assert.equal(isExpanded({ id: 'next-turn', closed: false }), true)
  assert.equal(isExpanded({ id: 'old-turn', closed: true }), false)
})
