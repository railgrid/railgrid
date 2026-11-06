<script setup lang="ts">
import AgentKitAttachmentTextPreview from './agentkit/AgentKitAttachmentTextPreview.vue'

const props = defineProps<{ file: File; label: string }>()
</script>

<template>
  <AgentKitAttachmentTextPreview v-bind="props" />
</template>
