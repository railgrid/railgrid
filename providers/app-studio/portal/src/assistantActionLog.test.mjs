import assert from 'node:assert/strict'
import test from 'node:test'
import vue from '@vitejs/plugin-vue'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', cacheDir: '/tmp/faros-vite-assistant-action-log', configFile: false, plugins: [vue()], server: { hmr: false, middlewareMode: true } })
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
  assert.match(html, /Ran commands and checks/)
  assert.doesNotMatch(html, /action-chain-fade/)
  assert.doesNotMatch(html, /tool call|tool result|args:|offset|limit|read_file/)
  assert.doesNotMatch(html, /rounded-xl border border-border-subtle bg-surface/)
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
  assert.match(html, /Details/)
  assert.match(html, /aria-expanded="true"/)
  assert.match(html, /text-danger/)
  assert.match(html, /max-h-\[min\(40vh,320px\)\]/)
  assert.match(html, /overflow-auto/)
  assert.match(html, /Failed:/)
  assert.match(html, /aria-controls="app-studio-assistant-actions-assistant-2-failed-1-diagnostic"/)
  assert.match(html, /inline-flex h-7[^>]*aria-expanded="false"[^>]*failed-1-diagnostic/)
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

test('keeps active work visible with semantic group labels', async () => {
  const { default: AssistantActionLog } = await vite.ssrLoadModule('/src/AssistantActionLog.vue')
  const html = await renderToString(createSSRApp(AssistantActionLog, {
    messageId: 'assistant-active',
    items: [
      { id: 'search-1', kind: 'inspect', status: 'running', title: 'Searching project files', target: 'src', severity: 'normal' },
      { id: 'search-2', kind: 'inspect', status: 'succeeded', title: 'Searched for App.vue', target: 'src/App.vue', severity: 'normal' },
    ],
  }))
  assert.match(html, /aria-expanded="true"/)
  assert.match(html, /Inspecting the project/)
  assert.match(html, /Searching project files/)
  assert.match(html, /Searched for App.vue/)
  assert.match(html, /animate-spin/)
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
  assert.match(html, /action-chain-fade max-h-\[240px\] overflow-y-auto pb-7 pr-1/)
  assert.doesNotMatch(html, /assistant-long-chain-inspect-\d+" class="ml-5/)
  assert.doesNotMatch(html, /class="mt-1 grid max-h-\[min\(40vh,320px\)\]/)
  assert.doesNotMatch(html, /max-h-\[min\(40vh,320px\)\] gap-1.5/)
  assert.match(html, /Read file 8/)
  assert.match(html, /Read files/)
})
