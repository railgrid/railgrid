import assert from 'node:assert/strict'
import test from 'node:test'
import vue from '@vitejs/plugin-vue'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', cacheDir: '/tmp/faros-vite-assistant-exec', configFile: false, plugins: [vue()], server: { hmr: false, middlewareMode: true } })
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

test('infers failed exec state from a non-zero exit when nested status is absent or unknown', async () => {
  const { assistantExecStatusPresentation } = await vite.ssrLoadModule('/src/assistantExecDisclosure.ts')
  assert.equal(assistantExecStatusPresentation({ argv: ['make', 'test'], exitCode: 2 }, 'succeeded').label, 'Failed')
  assert.equal(assistantExecStatusPresentation({ argv: ['make', 'test'], status: 'future_status', exitCode: 2 }).label, 'Failed')
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
