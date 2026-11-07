<script setup lang="ts">
import { computed } from 'vue'
import AIExecutionDetails from '../agentkit/AIExecutionDetails.vue'
import type { AIExecutionView } from '../agentkit/activity'
import { toolCommand } from '../toolExecution'
import { prettyJSON, type ToolCall } from '../types'

const props = defineProps<{
  tool: ToolCall
}>()

const execution = computed<AIExecutionView>(() => {
  const value = props.tool.durationMS
  const output = props.tool.error
    ? { output: [props.tool.error], outputLabel: 'Error' }
    : props.tool.result
      ? { output: [prettyJSON(props.tool.result)], outputLabel: 'Output' }
      : {}
  const command = toolCommand(props.tool)
  return {
    ...(command ? {
      command,
    } : {}),
    heading: command ? 'Shell' : `Tool · ${props.tool.name}`,
    ...(!command && props.tool.args ? { input: prettyJSON(props.tool.args), inputLabel: 'Arguments' } : {}),
    ...output,
    ...(command ? { outputLabel: undefined } : {}),
    status: props.tool.pending ? 'running' : props.tool.error ? 'failed' : 'succeeded',
    ...(typeof value === 'number' && Number.isFinite(value) && value >= 0 ? { durationMs: value } : {}),
  }
})
</script>

<template>
  <AIExecutionDetails :execution="execution" variant="activity" />
</template>
