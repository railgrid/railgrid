import assert from 'node:assert/strict'
import test from 'node:test'
import vue from '@vitejs/plugin-vue'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', cacheDir: '/tmp/kedge-vite-assistant-action-log', configFile: false, plugins: [vue()], server: { hmr: false, middlewareMode: true } })
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
  assert.doesNotMatch(html, /tool call|tool result|args:|offset|limit|read_file/)
})

test('renders only allowlisted structured failure diagnostics', async () => {
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
  assert.match(html, /Technical details/)
  assert.match(html, /Failed:/)
  assert.doesNotMatch(html, /aria-controls="app-studio-assistant-actions-assistant-2-failed-1-diagnostic"/)
  assert.doesNotMatch(html, /rawError|arguments/)
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
