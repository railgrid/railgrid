<script setup lang="ts">
import AgentKitAttachmentPreview from './agentkit/AgentKitAttachmentPreview.vue'
import type { AssistantAttachmentStatus } from './assistantAttachments'

const props = withDefaults(defineProps<{
  file: File
  label: string
  status: AssistantAttachmentStatus
  error?: string
  retryable?: boolean
  retryAction?: 'upload' | 'delete'
}>(), {
  error: '',
  retryable: false,
  retryAction: undefined,
})

const emit = defineEmits<{ remove: []; retry: [] }>()
</script>

<template>
  <AgentKitAttachmentPreview
    v-bind="props"
    @remove="emit('remove')"
    @retry="emit('retry')"
  />
</template>
