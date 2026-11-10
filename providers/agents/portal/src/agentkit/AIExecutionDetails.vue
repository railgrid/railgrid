<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIExecutionDetails renders a provider-sanitized execution view. Providers
  retain admission, parsing, authorization, and lifecycle policy; this
  component only escapes text and applies the same-origin link guard.
-->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Check, Copy, CircleHelp, ExternalLink, Loader2, Terminal, TriangleAlert, X } from 'lucide-vue-next'
import { ensureAgentUIStyles } from '../agentkit/styles'
import {
  executionStatusPresentation,
  safeExecutionURL,
} from '../agentkit/activity'
import type { AIExecutionVariant, AIExecutionView } from '../agentkit/activity'

const props = withDefaults(defineProps<{
  execution?: AIExecutionView
  variant?: AIExecutionVariant
}>(), {
  variant: 'activity',
})

ensureAgentUIStyles()

const view = computed(() => props.execution)
const detailURL = computed(() => safeExecutionURL(view.value?.detailURL))
const status = computed(() => executionStatusPresentation(view.value?.status, view.value?.exitCode))
const statusLabel = computed(() => {
  const current = view.value
  const label = status.value.known && current?.statusLabel ? current.statusLabel : status.value.label
  const suffix = current?.exitCode !== undefined && current?.exitCode !== null && !status.value.busy
    ? ` · exit ${current.exitCode}`
    : ''
  // The exit code is a receipt even when the provider's status is unverified
  // (for example, stale source). Keep that status label and show the code only
  // when one was reported for a non-running execution.
  return suffix && !label.endsWith(suffix) ? `${label}${suffix}` : label
})
const statusIcon = computed(() => {
  switch (status.value.state) {
    case 'running': return Loader2
    case 'succeeded': return Check
    case 'exited': return TriangleAlert
    case 'failed':
    case 'timed_out':
    case 'canceled':
    case 'blocked': return X
    default: return CircleHelp
  }
})

const expandedOutput = ref(false)
const wrapOutput = ref(false)
const copyState = ref('')
watch(
  [() => view.value?.command, () => view.value?.input],
  () => {
    expandedOutput.value = false
    copyState.value = ''
  },
)
const fullOutput = computed(() => outputLines(view.value).join('\n'))
const previewOutput = computed(() => fullOutput.value.split('\n').slice(0, 12).map(line => line.slice(0, 2000)).join('\n'))
const hasMoreOutput = computed(() => previewOutput.value !== fullOutput.value)
async function copyExecution(): Promise<void> {
  try {
    await navigator.clipboard.writeText([view.value?.command || view.value?.input, fullOutput.value].filter(Boolean).join('\n\n'))
    copyState.value = 'Copied'
  } catch {
    copyState.value = 'Could not copy'
  }
}

function durationLabel(execution?: AIExecutionView): string {
  if (!execution) return ''
  if (execution.duration) return execution.duration
  if (execution.durationMs === undefined) return ''
  if (execution.durationMs < 1_000) return `${execution.durationMs} ms`
  const seconds = execution.durationMs / 1_000
  if (seconds < 60) return `${seconds.toFixed(seconds >= 10 ? 0 : 1)} s`
  const minutes = Math.floor(seconds / 60)
  return `${minutes}m ${Math.round(seconds % 60)}s`
}

function outputLines(execution?: AIExecutionView): readonly string[] {
  return execution?.output || []
}
</script>

<template>
  <div
    v-if="view"
    class="k-ai-execution-details"
    :class="`k-ai-execution-details--${variant}`"
  >
    <template v-if="variant === 'approval'">
      <div class="k-ai-execution-details__heading">
        <Terminal class="k-ai-execution-details__heading-icon" :stroke-width="1.75" aria-hidden="true" />
        Command execution
      </div>

      <div v-if="view.command" class="k-ai-execution-details__section">
        <div class="k-ai-execution-details__label">Command</div>
        <code class="k-ai-execution-details__command">{{ view.command }}</code>
      </div>

      <div v-if="view.argv?.length" class="k-ai-execution-details__section">
        <div class="k-ai-execution-details__label">Sanitized argv</div>
        <div class="k-ai-execution-details__argv" aria-label="Sanitized argv">
          <code v-for="(token, index) in view.argv" :key="`${index}-${token}`" class="k-ai-execution-details__argv-token">{{ token }}</code>
        </div>
      </div>

      <dl v-if="view.fields?.length" class="k-ai-execution-details__fields k-ai-execution-details__section">
        <template v-for="(field, index) in view.fields" :key="`${field.label}-${index}`">
          <dt>{{ field.label }}</dt>
          <dd>{{ field.value }}</dd>
        </template>
      </dl>
    </template>

    <template v-else>
      <div class="k-ai-execution-details__toolbar">
        <span>{{ view.heading || 'Shell' }}</span>
        <div class="k-ai-execution-details__controls">
          <button type="button" :aria-pressed="wrapOutput" @click="wrapOutput = !wrapOutput">Wrap lines</button>
          <button type="button" aria-label="Copy command and output" @click="copyExecution"><Copy :size="14" aria-hidden="true" /> Copy</button>
          <span role="status">{{ copyState }}</span>
        </div>
      </div>
      <div v-if="view.input" class="k-ai-execution-details__section">
        <div v-if="view.inputLabel" class="k-ai-execution-details__label">{{ view.inputLabel }}</div>
        <pre class="k-ai-execution-details__command">{{ view.input }}</pre>
      </div>
      <div v-if="view.command" class="k-ai-execution-details__command k-ai-execution-details__section">
        <span aria-hidden="true" class="k-ai-execution-details__prompt">$ </span>{{ view.command }}
      </div>
      <div v-if="outputLines(view).length" class="k-ai-execution-details__section">
        <div v-if="view.outputLabel" class="k-ai-execution-details__label">{{ view.outputLabel }}</div>
        <pre class="k-ai-execution-details__output" :class="{ 'is-wrapped': wrapOutput }" tabindex="0" aria-label="Command output">{{ expandedOutput ? fullOutput : previewOutput }}</pre>
      </div>
      <div v-else class="k-ai-execution-details__no-output">{{ status.busy ? 'Waiting for output…' : 'No output' }}</div>
      <div v-if="view.outputTruncated" class="k-ai-execution-details__truncated">Output truncated to the latest bounded lines.</div>
      <div v-if="view.detail || detailURL" class="k-ai-execution-details__detail">
        <span v-if="view.detail">{{ view.detail }}</span>
        <a
          v-if="detailURL"
          class="k-ai-execution-details__detail-link"
          :href="detailURL"
          target="_blank"
          rel="noreferrer"
        >
          Details
          <ExternalLink :stroke-width="2" aria-hidden="true" />
        </a>
      </div>
      <div class="k-ai-execution-details__footer">
        <button v-if="hasMoreOutput" type="button" :aria-expanded="expandedOutput" @click="expandedOutput = !expandedOutput">{{ expandedOutput ? 'Show less output' : 'Show full output' }}</button>
        <span v-else />
        <span class="k-ai-execution-details__status" :class="`k-ai-execution-details__status--${status.tone}`">
          <component
            :is="statusIcon"
            class="k-ai-execution-details__status-icon"
            :class="{ 'k-ai-execution-details__status-icon--running': status.busy }"
            :stroke-width="1.75"
            aria-hidden="true"
          />
          {{ statusLabel }}<template v-if="durationLabel(view)"> · {{ durationLabel(view) }}</template>
        </span>
      </div>
    </template>
  </div>
</template>
