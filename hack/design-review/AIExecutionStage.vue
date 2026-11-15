<script setup>
import { computed, ref } from 'vue'
import AIExecutionDetails from '../../provider-sdk/agentkit-vue/AIExecutionDetails.vue'
import AssistantExecDetails from '../../providers/app-studio/portal/src/AssistantExecDetails.vue'
const state = ref('completed')
const output = Array.from({ length: 16 }, (_, i) => `service-${String(i + 1).padStart(2, '0')}  ready  3/3`)
const execution = computed(() => ({
  command: 'kubectl get deployments',
  status: state.value === 'unknown' ? 'unrecognized-state' : state.value === 'running' ? 'running' : state.value === 'failed' ? 'failed' : 'succeeded',
  ...(state.value === 'running' ? {} : { exitCode: state.value === 'failed' ? 1 : 0, duration: '2.4 s' }),
  output: state.value === 'running' ? [] : state.value === 'failed' ? ['Unable to reach the staging cluster.'] : output,
  ...(state.value === 'truncated' ? { outputTruncated: true } : {}),
}))
const studioExecution = { component: 'backend', argv: ['kubectl', 'get', 'deployments'], workdir: '.', timeoutSeconds: 30, exitCode: 0, stdout: ['relay    3/3 ready'], status: 'succeeded' }
</script>

<template>
  <div class="spec-stack">
    <p class="spec-note">Anatomy: a supplied command or input, output, controls, and reported outcome. Sample receipts only; no command runs and no cluster is contacted.</p>
    <section class="spec-section spec-stack" aria-labelledby="execution-receipt-title">
      <div><h3 id="execution-receipt-title">Execution receipt</h3><p class="spec-note">Change the reported state. Expand long output and try line wrapping; provider truncation remains visible after expansion.</p></div>
      <label class="spec-field spec-fit">Reported state<select v-model="state" class="k-input"><option value="completed">Completed</option><option value="running">Running</option><option value="failed">Failed</option><option value="truncated">Provider-truncated</option><option value="unknown">Unknown</option></select></label>
      <AIExecutionDetails :execution="execution" />
    </section>
    <section class="spec-section spec-stack" aria-labelledby="execution-approval-title">
      <div><h3 id="execution-approval-title">Approval disclosure</h3><p class="spec-note">Before an action runs, display the proposed command and scope without presenting an execution result.</p></div>
      <AIExecutionDetails variant="approval" :execution="{ command: 'kubectl get deployments', argv: ['kubectl', 'get', 'deployments'], fields: [{ label: 'Working directory', value: '.' }] }" />
    </section>
    <section class="spec-section spec-stack" aria-labelledby="execution-product-title">
      <div><h3 id="execution-product-title">App Studio product component</h3><p class="spec-note">The provider component projects a sample receipt into the shared presentation.</p></div>
      <AssistantExecDetails :exec="studioExecution" />
    </section>
    <p class="spec-note"><a href="http://127.0.0.1:5200/" target="_blank" rel="noopener">Open the Agents run fixture</a> for the surrounding investigation. It uses sample data and may not contain a command receipt.</p>
  </div>
</template>
