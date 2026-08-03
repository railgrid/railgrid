import assert from 'node:assert/strict'
import test from 'node:test'
import vue from '@vitejs/plugin-vue'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', cacheDir: '/tmp/kedge-vite-assistant-exec', configFile: false, plugins: [vue()], server: { hmr: false, middlewareMode: true } })
})
test.after(async () => vite?.close())

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
  assert.match(html, /building/)
  assert.match(html, /failed/)
  assert.match(html, /Output truncated/)
})

test('does not render unknown disclosure fields', async () => {
  const { default: AssistantExecDetails } = await vite.ssrLoadModule('/src/AssistantExecDetails.vue')
  const html = await renderToString(createSSRApp(AssistantExecDetails, {
    exec: { component: 'backend', argv: ['go', 'test'], rawArguments: 'token=secret' },
  }))
  assert.doesNotMatch(html, /Command execution|rawArguments/)
  assert.doesNotMatch(html, /secret/)
})
