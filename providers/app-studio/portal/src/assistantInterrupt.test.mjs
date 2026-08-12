import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ appType: 'custom', cacheDir: '/tmp/faros-vite-assistant-interrupt', configFile: false, server: { middlewareMode: true } })
const interrupt = await vite.ssrLoadModule('/src/assistantInterrupt.ts')
test.after(async () => vite.close())

const disclosure = {
  component: 'backend',
  argv: ['node', '-e', 'console.log("safe")'],
  workdir: '.',
  timeoutSeconds: 30,
  authorityProfile: 'application-container',
  networkProfile: 'application-runtime',
  writebackPolicy: 'runtime-workspace-only',
}

const approval = (exec = disclosure) => ({
  interruptId: 'interrupt-1',
  kind: 'permission',
  status: 'pending',
  description: 'Review this action before it runs.',
  action: {
    runId: 'run-1',
    requestId: 'request-1',
    assistantMessageId: 'assistant-1',
    exec,
  },
})

test('preserves valid approval envelopes and bounded exec disclosure', () => {
  const parsed = interrupt.parseAssistantInterrupt(approval())
  assert.deepEqual(parsed, approval())
  assert.equal(interrupt.assistantInterruptAllowsApproval(parsed), true)
})

test('keeps action identifiers while stripping an oversized exec disclosure', () => {
  const raw = approval({ ...disclosure, argv: ['node', '-e', 'x'.repeat(313)] })
  const parsed = interrupt.parseAssistantInterrupt(raw)

  assert.deepEqual(parsed?.action, {
    runId: 'run-1',
    requestId: 'request-1',
    assistantMessageId: 'assistant-1',
  })
  assert.equal(parsed?.execDisclosureInvalid, true)
  assert.equal(interrupt.assistantInterruptAllowsApproval(parsed), false)
  assert.equal(JSON.stringify(parsed).includes('x'.repeat(313)), false)
})

test('marks invalid top-level disclosure without leaking its raw fields', () => {
  const raw = { ...approval(undefined), exec: { argv: [''] } }
  const parsed = interrupt.parseAssistantInterrupt(raw)

  assert.equal(parsed?.execDisclosureInvalid, true)
  assert.equal(parsed?.exec, undefined)
  assert.deepEqual(parsed?.action?.exec, disclosure)
})

test('rejects malformed approval envelopes while preserving strict outer validation', () => {
  assert.equal(interrupt.parseAssistantInterrupt({ ...approval(), action: { ...approval().action, extra: 'raw' } }), undefined)
  assert.equal(interrupt.parseAssistantInterrupt({ ...approval(), rawArguments: 'secret' }), undefined)
  assert.equal(interrupt.assistantInterruptAllowsApproval(undefined), false)
})
