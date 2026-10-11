import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import vue from '@vitejs/plugin-vue'
import { createServer } from 'vite'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', cacheDir: '/tmp/railgrid-vite-assistant-exec', configFile: false, plugins: [vue()], server: { hmr: false, middlewareMode: true } })
})
test.after(async () => vite?.close())

test('accepts the complete public server exec contract and validates source evidence', async () => {
  const { parseAssistantExecDisclosure } = await vite.ssrLoadModule('/src/assistantExecDisclosure.ts')
  const exec = {
    component: 'api', argv: ['node', '-e', 'console.log("OK")'], workdir: '.', timeoutSeconds: 30,
    sourceRevision: 173, sourceDigest: 'a'.repeat(64), syncStatus: 'succeeded', sourceStale: false,
    networkProfile: 'application-runtime', authorityProfile: 'application-container',
    writebackPolicy: 'runtime-workspace-only', status: 'succeeded', summary: 'Command succeeded.',
    exitCode: 0, durationMs: 473, stdout: ['OK'], stderr: [], outputTruncated: false,
  }
  // Exercise every field emitted by the Go public envelope so an added field
  // cannot silently make the strict portal parser hide all command activity.
  const server = await readFile(new URL('../../api/assistant_exec_command.go', import.meta.url), 'utf8')
  const contract = server.match(/type projectAssistantExecMetadata struct \{([\s\S]*?)\n\}/)?.[1]
  assert.ok(contract)
  const keys = [...contract.matchAll(/json:"([^",]+)(?:,[^"]*)?"/g)].map((match) => match[1])
  assert.deepEqual(Object.keys(exec).sort(), keys.sort())
  assert.deepEqual(parseAssistantExecDisclosure(exec), exec)
  for (const invalid of [
    { sourceRevision: -1 }, { sourceRevision: 1.5 }, { sourceRevision: Number.MAX_SAFE_INTEGER + 1 },
    { sourceRevision: '173' }, { sourceDigest: 'x'.repeat(257) }, { sourceDigest: 'bad\u0000digest' },
    { syncStatus: 'x'.repeat(65) }, { syncStatus: 1 }, { rawArguments: 'secret' },
  ]) assert.equal(parseAssistantExecDisclosure({ ...exec, ...invalid }), undefined)
})

test('keeps the bounded public argv and Unicode output emitted by the server', async () => {
  const { parseAssistantExecDisclosure } = await vite.ssrLoadModule('/src/assistantExecDisclosure.ts')
  const exec = { component: 'app', argv: ['node', '-e', 'console.log("OK")'], status: 'canceled' }

  // A legal 4092-byte input token can expand to 8178 bytes after the server
  // replaces repeated short passwd values. Public argv is not truncated.
  const redactionExpandedArg = `--set ${'passwd=[redacted] '.repeat(454)}`
  assert.equal(new TextEncoder().encode(redactionExpandedArg).byteLength, 8_178)
  const publicExec = parseAssistantExecDisclosure({ ...exec, argv: [redactionExpandedArg] })
  assert.equal(publicExec?.argv?.[0], redactionExpandedArg)
  assert.doesNotMatch(JSON.stringify(publicExec), /passwd=x/)
  assert.equal(parseAssistantExecDisclosure({ ...exec, argv: ['x'.repeat(12 * 1024 + 1)] }), undefined)

  // Go's public result limits these fields by Unicode code points, then caps
  // each stdout/stderr stream at 1 MiB. Non-ASCII output remains valid.
  const unicodeSummary = '🧭'.repeat(240)
  const unicodeLine = '🧭'.repeat(4_096)
  assert.equal(parseAssistantExecDisclosure({ ...exec, summary: unicodeSummary, stdout: [unicodeLine] })?.summary, unicodeSummary)
  assert.equal(parseAssistantExecDisclosure({ ...exec, summary: '🧭'.repeat(241) }), undefined)
  assert.equal(parseAssistantExecDisclosure({ ...exec, stdout: ['🧭'.repeat(4_097)] }), undefined)
  assert.equal(parseAssistantExecDisclosure({ ...exec, stdout: Array(65).fill(unicodeLine) }), undefined)
})

test('projects a canceled command receipt into the expandable interrupted-turn details', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const { parseAssistantActionFeed } = await vite.ssrLoadModule('/src/assistantActionFeed.ts')
  const { parseAssistantProgress } = await vite.ssrLoadModule('/src/assistantProgress.ts')
  const { buildAssistantTrace } = await vite.ssrLoadModule('/src/assistantTrace.ts')
  const { default: AITurnProgress } = await vite.ssrLoadModule('/src/agentkit/AITurnProgress.vue')
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const command = 'console.log(JSON.stringify({marker:"HARNESS_RECEIPT_CANCEL_FINAL",pid:process.pid,startedAt:new Date().toISOString()}));setTimeout(()=>console.log(JSON.stringify({marker:"HARNESS_RECEIPT_CANCEL_FINAL",pid:process.pid,finishedAt:new Date().toISOString()})),45000)'
  assert.equal(new TextEncoder().encode(command).byteLength, 262)
  const messageID = 'msg-canceled'
  const [message] = assistantThreadItemsToMessages([
    {
      id: messageID, turnID: 'run-canceled', type: 'agentMessage', phase: 'final_answer', status: 'interrupted',
      assistantMessageID: messageID, content: '', mode: 'default', revision: 22, sequence: 7,
      data: { assistantProgress: { version: 1, messages: [], messageSequences: [], workedDurationMs: 21_455 } },
      createdAt: '2026-10-11T15:15:58.839342Z',
    },
    {
      id: `tool-${messageID}-feed-canceled`, turnID: 'run-canceled', type: 'dynamicToolCall', status: 'completed',
      assistantMessageID: messageID, content: 'Canceled', sequence: 6,
      data: {
        id: 'feed-canceled', kind: 'run', title: 'Canceled', status: 'canceled', severity: 'normal', sequence: 2,
        exec: {
          argv: ['node', '-e', command], component: 'app', status: 'canceled',
          summary: 'Command canceled in component "app".', durationMs: 9_383, syncStatus: 'succeeded',
          sourceDigest: '8511ba17a68a4c16bf8515d7c52d5329bee0b079ea2fb5e76eb45848b59a15ee',
          networkProfile: 'application-runtime', sourceRevision: 12, timeoutSeconds: 120,
          writebackPolicy: 'runtime-workspace-only', authorityProfile: 'application-container',
        },
      },
      createdAt: '2026-10-11T15:16:10.006419Z',
    },
  ], 'demo')

  assert.equal(message.metadata.assistantStatus, 'interrupted')
  const actions = parseAssistantActionFeed(message.metadata.assistantActionFeed)
  assert.equal(actions.length, 1)
  assert.equal(actions[0].status, 'canceled')
  assert.equal(actions[0].exec.status, 'canceled')
  const blocks = buildAssistantTrace(parseAssistantProgress(message.metadata.assistantProgress), actions)
  assert.equal(blocks[0].kind, 'actions')
  const html = await renderToString(createSSRApp({ render: () => h(AITurnProgress, {
    turnId: message.id, status: 'interrupted', duration: '21s', interrupted: true, expanded: false,
  }, { details: () => blocks.map((block) => h(AssistantActionLog, { messageId: message.id, items: block.items })) }) }))
  assert.match(html, /class="k-ai-turn-progress__trigger"/)
  assert.match(html, /aria-expanded="false"/)
  assert.match(html, /Canceled/)
  assert.match(html, /node -e/)
})

test('reloaded command-only turns render an expandable worked section with all saved receipts', async () => {
  const { assistantThreadItemsToMessages } = await vite.ssrLoadModule('/src/assistantThreadProjection.ts')
  const { parseAssistantActionFeed } = await vite.ssrLoadModule('/src/assistantActionFeed.ts')
  const { parseAssistantProgress } = await vite.ssrLoadModule('/src/assistantProgress.ts')
  const { buildAssistantTrace } = await vite.ssrLoadModule('/src/assistantTrace.ts')
  const { default: AITurnProgress } = await vite.ssrLoadModule('/src/agentkit/AITurnProgress.vue')
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const items = ['blocked', 'succeeded', 'failed', 'outcome_unknown'].map((status, index) => ({
    id: `tool-assistant-${index}`, turnID: 'turn-1', assistantMessageID: 'assistant-1',
    type: 'dynamicToolCall', status: status === 'succeeded' || status === 'outcome_unknown' ? 'completed' : 'failed', sequence: index + 1,
    data: {
      id: `feed-${index}`, kind: 'run', title: 'Command', status: status === 'succeeded' || status === 'outcome_unknown' ? 'succeeded' : 'failed',
      severity: status === 'succeeded' ? 'normal' : 'error', sequence: index + 1,
      exec: {
        component: 'api', argv: ['node', '-e', `console.log("RECEIPT_${index}")`],
        sourceRevision: 173, sourceDigest: 'a'.repeat(64), syncStatus: 'succeeded', status,
        ...(status === 'succeeded' ? { exitCode: 0 } : status === 'failed' ? { exitCode: 7 } : {}),
      },
    },
  }))
  items.push({
    id: 'assistant-1', turnID: 'turn-1', type: 'agentMessage', status: 'completed', phase: 'final_answer',
    sequence: 4, content: 'Done',
    data: { assistantProgress: { version: 1, messages: [], messageSequences: [], workedDurationMs: 12670 } },
  })
  const [message] = assistantThreadItemsToMessages(items, 'demo')
  const actions = parseAssistantActionFeed(message.metadata.assistantActionFeed)
  assert.equal(actions.length, 4)
  assert.equal(actions[0].exec.exitCode, undefined)
  assert.deepEqual(actions.slice(1).map((action) => action.exec.exitCode), [0, 7, undefined])
  const blocks = buildAssistantTrace(parseAssistantProgress(message.metadata.assistantProgress), actions)
  for (const expanded of [false, true]) {
    const html = await renderToString(createSSRApp({ render: () => h(AITurnProgress, {
      turnId: message.id, status: 'completed', duration: '13s', expanded,
    }, { details: () => blocks.map((block) => h(AssistantActionLog, { messageId: message.id, items: block.items })) }) }))
    assert.match(html, /<button[^>]*class="k-ai-turn-progress__trigger"/)
    assert.match(html, new RegExp(`aria-expanded="${expanded}"`))
    for (let index = 0; index < 4; index++) assert.match(html, new RegExp(`RECEIPT_${index}`))
    assert.match(html, /Unverified/)
  }
})

test('parses approval disclosures while rejecting unknown statuses and fields', async () => {
  const { parseAssistantExecDisclosure } = await vite.ssrLoadModule('/src/assistantExecDisclosure.ts')
  const exec = {
    component: 'backend',
    argv: ['go', 'test'],
    status: 'permission_required',
  }
  assert.deepEqual(parseAssistantExecDisclosure(exec), exec)
  assert.equal(parseAssistantExecDisclosure({ ...exec, status: 'unknown_status' }), undefined)
  assert.equal(parseAssistantExecDisclosure({ ...exec, rawArguments: 'token=secret' }), undefined)
})

test('parses stale source evidence and presents it as unverified without hiding failures', async () => {
  const { parseAssistantExecDisclosure, assistantExecStatusPresentation } = await vite.ssrLoadModule('/src/assistantExecDisclosure.ts')
  const staleSuccess = { argv: ['npm', 'run', 'build'], status: 'stale', sourceStale: true, exitCode: 0 }
  assert.deepEqual(parseAssistantExecDisclosure(staleSuccess), staleSuccess)
  assert.equal(parseAssistantExecDisclosure({ ...staleSuccess, sourceStale: 'yes' }), undefined)
  const unverified = assistantExecStatusPresentation(staleSuccess)
  assert.equal(unverified.label, 'Unverified')
  assert.equal(unverified.attention, true)
  assert.equal(unverified.error, false)

  const unknownOutcome = { argv: ['npm', 'run', 'build'], status: 'outcome_unknown', exitCode: null }
  assert.deepEqual(parseAssistantExecDisclosure(unknownOutcome), unknownOutcome)
  assert.equal(assistantExecStatusPresentation(unknownOutcome).label, 'Unverified')

  const staleFailure = assistantExecStatusPresentation({ ...staleSuccess, status: 'failed', exitCode: 2 })
  assert.equal(staleFailure.label, 'Failed')
  assert.equal(staleFailure.error, true)
})

test('renders an unknown command outcome as unverified in the shared execution details', async () => {
  const { default: AssistantExecDetails } = await vite.ssrLoadModule('/src/AssistantExecDetails.vue')
  const html = await renderToString(createSSRApp(AssistantExecDetails, {
    exec: {
      component: 'backend',
      argv: ['npm', 'run', 'build'],
      status: 'outcome_unknown',
      exitCode: null,
      summary: 'The command result could not be confirmed.',
    },
  }))
  assert.match(html, /Unverified/)
  assert.doesNotMatch(html, /Success|k-ai-execution-details__status--success/)
})

test('uses terminal exit proof for successful execs when nested status is absent or unknown', async () => {
  const { assistantExecStatusPresentation } = await vite.ssrLoadModule('/src/assistantExecDisclosure.ts')
  assert.equal(assistantExecStatusPresentation({ argv: ['make', 'test'], exitCode: 2 }, 'succeeded').label, 'Failed')
  assert.equal(assistantExecStatusPresentation({ argv: ['make', 'test'], status: 'future_status', exitCode: 2 }).label, 'Failed')
  assert.equal(assistantExecStatusPresentation({ argv: ['make', 'test'] }, 'succeeded').label, 'Unverified')
  assert.equal(assistantExecStatusPresentation({ argv: ['make', 'test'], status: 'succeeded' }).label, 'Unverified')
  assert.equal(assistantExecStatusPresentation({ argv: ['make', 'test'], exitCode: 0 }, 'succeeded').label, 'Ran')
})

test('renders approval metadata and bounded command output', async () => {
  const { default: AssistantExecDetails } = await vite.ssrLoadModule('/src/AssistantExecDetails.vue')
  const html = await renderToString(createSSRApp(AssistantExecDetails, {
    variant: 'approval',
    exec: {
      component: 'backend',
      argv: ['go', 'test', './internal package'],
      workdir: 'internal',
      timeoutSeconds: 30,
      authorityProfile: 'application-container',
      networkProfile: 'application-runtime',
      writebackPolicy: 'runtime-workspace-only',
      status: 'permission_required',
    },
  }))
  assert.match(html, /Command execution/)
  assert.match(html, /backend/)
  assert.match(html, /internal/)
  assert.match(html, /30s/)
  assert.match(html, /application-container/)
  assert.match(html, /application-runtime/)
  assert.match(html, /runtime-workspace-only/)
  assert.match(html, /\.\/internal package/)
})

test('renders completed activity status, duration, and bounded stdout/stderr', async () => {
  const { default: AssistantExecDetails } = await vite.ssrLoadModule('/src/AssistantExecDetails.vue')
  const html = await renderToString(createSSRApp(AssistantExecDetails, {
    exec: {
      component: 'frontend',
      argv: ['npm', 'run', 'build'],
      workdir: 'web',
      status: 'failed',
      exitCode: 2,
      durationMs: 2040,
      stdout: ['building'],
      stderr: ['failed'],
      outputTruncated: true,
      summary: 'Command failed in frontend.',
    },
  }))
  assert.match(html, /exit 2/)
  assert.match(html, /2\.0 s/)
  assert.match(html, /Shell/)
  assert.match(html, /\$ <\/span>npm run build/)
  assert.match(html, /building/)
  assert.match(html, /failed/)
  assert.match(html, /Failed · exit 2/)
  assert.match(html, /Output truncated/)
  assert.doesNotMatch(html, /Direct command|Sanitized argv|Relative cwd|Exit status|stdout|stderr|web/)
})

test('preserves failure and blocked details while adding stale source context', async () => {
  const { default: AssistantExecDetails } = await vite.ssrLoadModule('/src/AssistantExecDetails.vue')
  const staleSuccess = await renderToString(createSSRApp(AssistantExecDetails, {
    exec: {
      component: 'backend',
      argv: ['npm', 'run', 'build'],
      status: 'stale',
      sourceStale: true,
      exitCode: 0,
      stdout: ['BUILD_OK'],
      summary: 'Project source changed from revision 1 to 2 while this command ran; the result is unverified for current source.',
    },
  }))
  assert.match(staleSuccess, /Unverified/)
  assert.match(staleSuccess, /BUILD_OK/)
  assert.match(staleSuccess, /changed from revision 1 to 2/)
  assert.doesNotMatch(staleSuccess, /Success|k-ai-execution-details__status--success/)

  const staleFailure = await renderToString(createSSRApp(AssistantExecDetails, {
    exec: {
      argv: ['npm', 'run', 'build'],
      status: 'failed',
      sourceStale: true,
      exitCode: 2,
      stderr: ['BUILD_FAILED'],
      detail: 'The compiler reported a missing module.',
      summary: 'Command failed. Project source changed from revision 1 to 2 while this command ran; this failure is not evidence about current source.',
    },
  }))
  assert.match(staleFailure, /Failed · exit 2/)
  assert.match(staleFailure, /BUILD_FAILED/)
  assert.match(staleFailure, /The compiler reported a missing module/)
  assert.match(staleFailure, /changed from revision 1 to 2/)
  assert.doesNotMatch(staleFailure, /Unverified|Success|k-ai-execution-details__status--success/)

  const staleBlocked = await renderToString(createSSRApp(AssistantExecDetails, {
    exec: {
      argv: ['npm', 'run', 'build'],
      status: 'permission_required',
      sourceStale: true,
      detail: 'Approval is required before this command can run.',
    },
  }))
  assert.match(staleBlocked, /Blocked/)
  assert.match(staleBlocked, /Approval is required before this command can run/)
  assert.match(staleBlocked, /project source may have changed during execution/)
  assert.doesNotMatch(staleBlocked, /Unverified/)
})

test('renders shared command status labels for blocked, skipped, unknown, and terminal outcomes', async () => {
  const { default: AssistantExecDetails } = await vite.ssrLoadModule('/src/AssistantExecDetails.vue')
  const cases = [
    { status: 'permission_required', sourceStale: true, want: 'Blocked' },
    { status: 'not_executed', want: 'Blocked' },
    { status: 'rejected', want: 'Blocked' },
    { status: 'skipped', want: 'Skipped' },
    { status: 'outcome_unknown', exitCode: null, sourceStale: true, want: 'Unverified' },
    { status: 'failed', exitCode: 2, sourceStale: true, want: 'Failed · exit 2' },
    { status: 'timed_out', exitCode: 124, sourceStale: true, want: 'Timed out · exit 124' },
    { status: 'succeeded', want: 'Unverified' },
  ]
  for (const [index, { want, ...exec }] of cases.entries()) {
    const html = await renderToString(createSSRApp(AssistantExecDetails, {
      exec: { argv: ['npm', 'run', `case-${index}`], ...exec },
    }))
    assert.ok(html.includes(want), `status ${exec.status} should render ${want}`)
    if (exec.status === 'not_executed') assert.doesNotMatch(html, /Not run/)
  }
})

test('renders source provenance in activity and approval disclosures', async () => {
  const { default: AssistantExecDetails } = await vite.ssrLoadModule('/src/AssistantExecDetails.vue')
  const sourceDigest = `sha256:${'a'.repeat(64)}`
  const exec = {
    component: 'backend',
    argv: ['go', 'test', './...'],
    sourceRevision: 42,
    sourceDigest,
    syncStatus: 'stale',
    status: 'failed',
    exitCode: 2,
  }
  for (const variant of ['activity', 'approval']) {
    const html = await renderToString(createSSRApp(AssistantExecDetails, { exec, variant }))
    assert.match(html, /Source revision/)
    assert.match(html, />42</)
    assert.match(html, /Source digest/)
    assert.match(html, new RegExp(sourceDigest))
    assert.match(html, /Sync status/)
    assert.match(html, />stale</)
  }
})

test('does not render unknown disclosure fields', async () => {
  const { default: AssistantExecDetails } = await vite.ssrLoadModule('/src/AssistantExecDetails.vue')
  const html = await renderToString(createSSRApp(AssistantExecDetails, {
    exec: { component: 'backend', argv: ['go', 'test'], rawArguments: 'token=secret' },
  }))
  assert.doesNotMatch(html, /Command execution|rawArguments/)
  assert.doesNotMatch(html, /secret/)
})

test('shared execution details keeps unknown status neutral, escapes text, and rejects external links', async () => {
  const { default: AIExecutionDetails } = await vite.ssrLoadModule('/src/agentkit/AIExecutionDetails.vue')
  const html = await renderToString(createSSRApp(AIExecutionDetails, {
    execution: {
      command: '<danger> && echo ok',
      output: ['<script>alert(1)</script>'],
      status: 'future_status',
      exitCode: 0,
      detail: 'Review <the bounded output>',
      detailURL: 'https://evil.example/steal',
    },
  }))
  assert.match(html, /Status unavailable/)
  assert.match(html, /&lt;danger&gt;/)
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/)
  assert.match(html, /Review &lt;the bounded output&gt;/)
  assert.doesNotMatch(html, /k-ai-execution-details__status--success|href=/)
})

test('shared execution details keeps same-origin detail links and known success status', async () => {
  const { default: AIExecutionDetails } = await vite.ssrLoadModule('/src/agentkit/AIExecutionDetails.vue')
  const html = await renderToString(createSSRApp(AIExecutionDetails, {
    execution: {
      command: 'go test ./...',
      status: 'succeeded',
      duration: '1.2 s',
      detailURL: '/runs/123?tab=output',
    },
  }))
  assert.match(html, /Status unavailable|Success/)
  assert.match(html, /k-ai-execution-details__status--success/)
  assert.match(html, /href="&#x2F;runs&#x2F;123\?tab=output"|href="\/runs\/123\?tab=output"/)
})


test('does not present success when the execution has a failed exit code', async () => {
  const { default: AssistantExecDetails } = await vite.ssrLoadModule('/src/AssistantExecDetails.vue')
  const html = await renderToString(createSSRApp(AssistantExecDetails, {
    exec: { argv: ['npm', 'test'], status: 'succeeded', exitCode: 2 },
  }))
  assert.match(html, /Failed · exit 2/)
  assert.doesNotMatch(html, /Success|k-ai-execution-details__status--success/)
})
