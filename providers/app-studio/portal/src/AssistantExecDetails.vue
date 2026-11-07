<script setup lang="ts">
import { computed } from 'vue'
import type { AIExecutionField, AIExecutionView } from './agentkit/activity'
import AIExecutionDetails from './agentkit/AIExecutionDetails.vue'
import {
  assistantExecStatusPresentation,
  formatAssistantExecCommand,
  formatAssistantExecDuration,
  parseAssistantExecDisclosure,
} from './assistantExecDisclosure'
import type { AssistantExecDisplayState } from './assistantExecDisclosure'
import type { ProjectAssistantExecDisclosure } from './types'

const props = withDefaults(defineProps<{
  exec?: ProjectAssistantExecDisclosure
  variant?: 'approval' | 'activity'
}>(), {
  variant: 'activity',
})

// Studio remains the authority for the server-owned disclosure contract. The
// shared component receives only this parsed, allowlisted projection.
const disclosure = computed(() => parseAssistantExecDisclosure(props.exec))
const command = computed(() => formatAssistantExecCommand(disclosure.value))
const output = computed(() => {
  const exec = disclosure.value
  if (!exec) return undefined
  const lines = [...(exec.stdout || [])]
  if (exec.stderr?.length) {
    if (lines.length) lines.push('')
    lines.push(...exec.stderr)
  }
  return lines.length ? lines : undefined
})
const sourceFields = computed<AIExecutionField[]>(() => {
  const exec = disclosure.value
  if (!exec) return []
  return [
    exec.sourceRevision !== undefined ? { label: 'Source revision', value: String(exec.sourceRevision) } : undefined,
    exec.sourceDigest ? { label: 'Source digest', value: exec.sourceDigest } : undefined,
    exec.syncStatus ? { label: 'Sync status', value: exec.syncStatus } : undefined,
  ].filter((field): field is AIExecutionField => field !== undefined)
})
const fields = computed<AIExecutionField[] | undefined>(() => {
  const exec = disclosure.value
  if (!exec) return undefined
  const rows = [
    exec.component ? { label: 'Component', value: exec.component } : undefined,
    { label: 'Relative cwd', value: exec.workdir || '.' },
    exec.timeoutSeconds !== undefined ? { label: 'Timeout', value: `${exec.timeoutSeconds}s` } : undefined,
    exec.authorityProfile ? { label: 'Authority', value: exec.authorityProfile } : undefined,
    exec.networkProfile ? { label: 'Network', value: exec.networkProfile } : undefined,
    exec.writebackPolicy ? { label: 'Writeback', value: exec.writebackPolicy } : undefined,
  ].filter((row): row is AIExecutionField => row !== undefined)
  return [...rows, ...sourceFields.value]
})

function executionStatusFor(state: AssistantExecDisplayState): string {
  switch (state) {
    case 'ran': return 'succeeded'
    case 'unverified':
    case 'skipped':
    case 'blocked': return 'blocked'
    case 'running':
    case 'failed':
    case 'canceled':
    case 'timed_out': return state
  }
}

const detail = computed(() => {
  const exec = disclosure.value
  if (!exec) return undefined
  const originalDetail = exec.detail?.trim()
  if (!exec.sourceStale) return originalDetail || undefined

  // A stale-source summary often carries the server's precise explanation.
  // Keep both it and any preexisting failure/block detail, then add a concise
  // fallback note only when neither already explains the source uncertainty.
  const parts = [...new Set([originalDetail, exec.summary?.trim()].filter((part): part is string => Boolean(part)))]
  const explainsSourceUncertainty = /(?:source|revision).{0,100}(?:changed|stale|unverified|could not|cannot|unknown|current)|(?:changed|stale|unverified).{0,100}(?:source|revision)/i
  if (!parts.some((part) => explainsSourceUncertainty.test(part))) {
    parts.push('The project source may have changed during execution, so this output may not reflect the current source.')
  }
  return parts.join(' ')
})

const execution = computed<AIExecutionView | undefined>(() => {
  const exec = disclosure.value
  if (!exec) return undefined
  const status = assistantExecStatusPresentation(exec)
  return {
    ...(command.value ? { command: command.value } : {}),
    ...(exec.argv ? { argv: exec.argv } : {}),
    ...(props.variant === 'approval' && fields.value ? { fields: fields.value } : {}),
    ...(output.value ? { output: output.value } : {}),
    status: executionStatusFor(status.state),
    statusLabel: status.label,
    ...(exec.durationMs !== undefined ? {
      duration: formatAssistantExecDuration(exec.durationMs),
      durationMs: exec.durationMs,
    } : {}),
    ...(exec.exitCode !== undefined ? { exitCode: exec.exitCode } : {}),
    ...(exec.outputTruncated !== undefined ? { outputTruncated: exec.outputTruncated } : {}),
    ...(detail.value ? { detail: detail.value } : {}),
    ...(exec.detailURL ? { detailURL: exec.detailURL } : {}),
  }
})
</script>

<template>
  <AIExecutionDetails :execution="execution" :variant="variant" />
</template>
