import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true } })
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
  assert.doesNotMatch(html, /rawError|arguments/)
})
