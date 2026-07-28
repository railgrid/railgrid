import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true } })
})
test.after(async () => vite?.close())

test('renders the floating desktop capsule and accessible checklist', async () => {
  const { default: AssistantPlanPopover } = await vite.ssrLoadModule('/src/AssistantPlanPopover.vue')
  const html = await renderToString(createSSRApp(AssistantPlanPopover, {
    messageId: 'assistant-active',
    plan: {
      steps: [
        { content: 'Inspect the quote form', status: 'completed' },
        { content: 'Add the quote form', activeForm: 'Adding the quote form', status: 'in_progress' },
        { content: 'Verify the preview', status: 'pending' },
      ],
    },
  }))
  assert.match(html, /absolute bottom-3 right-4/)
  assert.match(html, /1 of 3 steps · Adding the quote form/)
  assert.match(html, /aria-expanded="false"/)
  assert.match(html, /Completed:/)
  assert.match(html, /In progress:/)
  assert.match(html, /Pending:/)
  assert.doesNotMatch(html, /border-t/)
})

test('supports explicit collapse and releases the mobile sheet at the desktop breakpoint', async () => {
  const source = await readFile(new URL('./AssistantPlanPopover.vue', import.meta.url), 'utf8')
  assert.match(source, /if \(pinned\.value\) \{[\s\S]*dismissed\.value = true/)
  assert.match(source, /desktopOpen = computed\(\(\) => !dismissed\.value/)
  assert.match(source, /matchMedia\('\(min-width: 768px\)'\)/)
  assert.match(source, /if \(event\.matches\) closeMobile\(false\)/)
  assert.match(source, /removeEventListener\('change', onDesktopBreakpoint\)/)
})
