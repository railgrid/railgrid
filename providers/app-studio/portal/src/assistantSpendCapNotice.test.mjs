import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
})
test.after(async () => vite?.close())

test('shows the spend-cap notice only when a completed file mutation is recorded', async () => {
  const { shouldShowAssistantSpendCapChangesNotice } = await vite.ssrLoadModule('/src/assistantSpendCapNotice.ts')
  const edit = { kind: 'edit', status: 'succeeded', groupKey: 'edit:files' }

  assert.equal(shouldShowAssistantSpendCapChangesNotice('org_spend_cap_exceeded', undefined), false, 'no activity')
  assert.equal(shouldShowAssistantSpendCapChangesNotice('org_spend_cap_exceeded', []), false, 'empty activity')
  assert.equal(shouldShowAssistantSpendCapChangesNotice('org_spend_cap_exceeded', [
    { kind: 'inspect', status: 'succeeded', groupKey: 'inspect:files' },
  ]), false, 'read-only activity')
  assert.equal(shouldShowAssistantSpendCapChangesNotice('org_spend_cap_exceeded', [
    { kind: 'edit', status: 'failed', groupKey: 'edit:files' },
  ]), false, 'failed edit')
  assert.equal(shouldShowAssistantSpendCapChangesNotice('other', [edit]), false, 'successful edit under a different error')
  assert.equal(shouldShowAssistantSpendCapChangesNotice('org_spend_cap_exceeded', [
    { kind: 'edit', status: 'succeeded', groupKey: 'edit:plan' },
  ]), false, 'edit without the server file-mutation group')
  assert.equal(shouldShowAssistantSpendCapChangesNotice('org_spend_cap_exceeded', [edit]), true, 'completed file mutation under cap')
})

test('renders the note from the canonical run error and successful file action feed', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(app, /shouldShowAssistantSpendCapChangesNotice\(run\?\.error\?\.errorInfo, message\.actionFeed\)/)
  assert.match(app, /Completed file changes have already been applied\. Review the activity before trying again\./)
})
