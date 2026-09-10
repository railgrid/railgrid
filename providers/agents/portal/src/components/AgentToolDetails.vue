<script setup lang="ts">
import { computed } from 'vue'
import AIExecutionDetails from '../agentkit/AIExecutionDetails.vue'
import type { AIExecutionView } from '../agentkit/activity'
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
  return {
    heading: `Tool · ${props.tool.name}`,
    ...(props.tool.args ? { input: prettyJSON(props.tool.args), inputLabel: 'Arguments' } : {}),
    ...output,
    status: props.tool.pending ? 'running' : props.tool.error ? 'failed' : 'succeeded',
    ...(typeof value === 'number' && Number.isFinite(value) && value >= 0 ? { durationMs: value } : {}),
  }
})
</script>

<template>
  <AIExecutionDetails :execution="execution" variant="activity" />
</template>
