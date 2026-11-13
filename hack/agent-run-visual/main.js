import { createApp, markRaw, ref } from 'vue'
import RunDetail from '../../providers/agents/portal/src/views/RunDetail.vue'
import './style.css'

const store = new EventTarget()
store.agent = () => ({ spec: { tools: {} } })

const run = {
  id: 'run-deploy-4821',
  agent: 'deployment-scout',
  sessionID: 'session-operations',
  trigger: 'chat',
  class: 'interactive',
  phase: 'Failed',
  message: 'The service health check timed out before the rollout could be verified.',
  input: 'Check the production deployment and explain whether it is safe to continue.',
  output: 'The deployment is running, but I could not verify service health. Review the failed health check before continuing.',
  inputTokens: 4120,
  outputTokens: 680,
  usdMicros: 12000,
  createdAt: '2026-11-12T15:30:00Z',
  startedAt: '2026-11-12T15:30:00Z',
  finishedAt: '2026-11-12T15:30:18Z',
  durationMS: 18000,
  steps: [
    { id: 'deployment', tool: 'get_deployment', outcome: 'ok', args: '{"name":"api","environment":"production"}', result: '{"revision":"v42","status":"running"}', durationMS: 350, at: '2026-11-12T15:30:02Z' },
    { id: 'health', tool: 'check_service_health', outcome: 'error', args: '{"service":"api","edge":"production-east"}', error: 'Health check timed out after 10 seconds.', durationMS: 10000, at: '2026-11-12T15:30:05Z' },
    { id: 'logs', tool: 'read_recent_logs', outcome: 'ok', args: '{"service":"api","limit":20}', result: 'No recent application errors were recorded.', durationMS: 1200, at: '2026-11-12T15:30:16Z' },
  ],
}
const api = { getRun: async () => structuredClone(run) }

createApp({
  components: { RunDetail },
  setup() {
    const theme = ref('light')
    function toggleTheme() {
      theme.value = theme.value === 'light' ? 'dark' : 'light'
      document.documentElement.classList.toggle('light', theme.value === 'light')
    }
    return { store: markRaw(store), api: markRaw(api), theme, toggleTheme }
  },
  template: `
    <main class="fixture">
      <div class="fixture-controls">
        <button class="k-btn k-btn--ghost" type="button" @click="toggleTheme">Switch to {{ theme === 'light' ? 'dark' : 'light' }} theme</button>
      </div>
      <railgrid-provider-agents>
        <RunDetail :store="store" :api="api" run-id="run-deploy-4821" />
      </railgrid-provider-agents>
    </main>`,
}).mount('#app')
