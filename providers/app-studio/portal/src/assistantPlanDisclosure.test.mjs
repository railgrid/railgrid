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

test('renders a collapsed plan history disclosure with accessible details', async () => {
  const { default: AssistantPlanDisclosure } = await vite.ssrLoadModule('/src/AssistantPlanDisclosure.vue')
  const html = await renderToString(createSSRApp(AssistantPlanDisclosure, {
    messageId: 'assistant-terminal',
    plan: {
      steps: [
        { content: 'Inspect the project', status: 'completed' },
        { content: 'Verify the preview', status: 'in_progress' },
        { content: 'Summarize the result', status: 'pending' },
      ],
    },
  }))

  assert.match(html, />Plan<\/span><span[^>]*> · 1 of 3 steps<\/span>/)
  assert.match(html, /aria-expanded="false"/)
  assert.match(html, /aria-controls="app-studio-assistant-plan-history-assistant-terminal"/)
  assert.match(html, /Inspect the project/)
  assert.match(html, /Verify the preview/)
})

test('keeps plan history keyboard-operable through native button semantics', async () => {
  const source = await readFile(new URL('./AssistantPlanDisclosure.vue', import.meta.url), 'utf8')
  assert.match(source, /<button[\s\S]*type="button"[\s\S]*:aria-expanded="expanded"/)
  assert.match(source, /:aria-controls="panelID"/)
  assert.match(source, /@click="togglePlan"/)
  assert.match(source, /function togglePlan\(\)[\s\S]*expanded\.value = !expanded\.value/)
  assert.match(source, /v-show="expanded"[\s\S]*role="region"/)
  assert.match(source, /focus-visible:ring-2 focus-visible:ring-accent\/30/)
})
