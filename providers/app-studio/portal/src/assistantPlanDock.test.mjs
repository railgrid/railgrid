import assert from 'node:assert/strict'
import test from 'node:test'

import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite

test.before(async () => {
  vite = await createServer({
    appType: 'custom',
    server: { middlewareMode: true },
  })
})

test.after(async () => {
  await vite?.close()
})

test('renders the active plan summary and collapsed step disclosure', async () => {
  const { default: AssistantPlanDock } = await vite.ssrLoadModule('/src/AssistantPlanDock.vue')
  const html = await renderToString(createSSRApp(AssistantPlanDock, {
    messageId: 'assistant-active',
    plan: {
      steps: [
        { content: 'Inspect the quote submission form', status: 'completed' },
        {
          content: 'Add the quote submission form',
          activeForm: 'Adding the quote submission form',
          status: 'in_progress',
        },
        { content: 'Verifying the preview', status: 'pending' },
      ],
    },
  }))

  assert.match(html, /1 of 3 steps · Adding the quote submission form/)
  assert.match(html, /aria-expanded="false"/)
  assert.match(html, /Adding the quote submission form/)
  assert.match(html, /Verifying the preview/)
})
