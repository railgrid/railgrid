<script setup lang="ts">
import { computed } from 'vue'
import AIExecutionDetails from '../agentkit/AIExecutionDetails.vue'
import type { AIExecutionView } from '../agentkit/activity'
import { redactTechnicalSecrets } from '../failure-presentation'
import { toolCommand, toolOutcome } from '../toolExecution'
import { prettyJSON, type ToolCall } from '../types'

const props = defineProps<{
  tool: ToolCall
  failed?: boolean
}>()

const execution = computed<AIExecutionView>(() => {
  const value = props.tool.durationMS
  const command = toolCommand(props.tool)
  const outcome = toolOutcome(props.tool)
  const text = outcome ? props.tool.result
    : command ? props.tool.result || props.tool.error
    : props.tool.error || props.tool.result
  const output = text
    ? [command || outcome ? redactTechnicalSecrets(text) : props.tool.error || prettyJSON(text)]
    : undefined
  return {
    ...(command ? {
      command,
    } : {}),
    heading: command ? 'Shell' : `Tool · ${props.tool.name}`,
    ...(!command && props.tool.args ? { input: prettyJSON(props.tool.args), inputLabel: 'Arguments' } : {}),
    output,
    outputLabel: command || !output ? undefined : !outcome && props.tool.error ? 'Error' : 'Output',
    status: outcome?.kind === 'exit' ? 'succeeded'
      : outcome?.kind === 'unknown' ? 'outcome_unknown'
      : props.tool.pending ? 'running'
      : props.failed || props.tool.error ? 'failed' : 'succeeded',
    statusLabel: outcome?.label,
    exitCode: outcome?.exitCode,
    detail: outcome?.detail,
    ...(typeof value === 'number' && Number.isFinite(value) && value >= 0 ? { durationMs: value } : {}),
  }
})
</script>

<template>
  <AIExecutionDetails :execution="execution" variant="activity" />
</template>
