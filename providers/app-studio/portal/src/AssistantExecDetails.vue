<script setup lang="ts">
import { computed } from 'vue'
import type { AIExecutionField, AIExecutionView } from './agentkit/activity'
import AIExecutionDetails from './agentkit/AIExecutionDetails.vue'
import {
  formatAssistantExecCommand,
  formatAssistantExecDuration,
  parseAssistantExecDisclosure,
} from './assistantExecDisclosure'
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
  ].filter((row): row is AIExecutionField => Boolean(row))
  return rows.length ? rows : undefined
})

const execution = computed<AIExecutionView | undefined>(() => {
  const exec = disclosure.value
  if (!exec) return undefined
  return {
    ...(command.value ? { command: command.value } : {}),
    ...(exec.argv ? { argv: exec.argv } : {}),
    ...(fields.value ? { fields: fields.value } : {}),
    ...(output.value ? { output: output.value } : {}),
    ...(exec.status ? { status: exec.status } : {}),
    ...(exec.durationMs !== undefined ? {
      duration: formatAssistantExecDuration(exec.durationMs),
      durationMs: exec.durationMs,
    } : {}),
    ...(exec.exitCode !== undefined ? { exitCode: exec.exitCode } : {}),
    ...(exec.outputTruncated !== undefined ? { outputTruncated: exec.outputTruncated } : {}),
    ...(exec.detail ? { detail: exec.detail } : {}),
    ...(exec.detailURL ? { detailURL: exec.detailURL } : {}),
  }
})
</script>

<template>
  <AIExecutionDetails :execution="execution" :variant="variant" />
</template>
