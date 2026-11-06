<script setup lang="ts">
import AIAttachmentReceipts from './agentkit/AIAttachmentReceipts.vue'
import { api } from './api'
import type { RailgridContext, ProjectAssistantAttachmentReceipt } from './types'
import type { AgentKitAttachmentReceipt } from './agentkit/composer'

const props = defineProps<{
  attachments: ProjectAssistantAttachmentReceipt[]
  ctx: RailgridContext | null
  projectName: string
}>()

function loadAttachment(receipt: AgentKitAttachmentReceipt, signal: AbortSignal): Promise<Blob> {
  return api.getAssistantAttachment(props.ctx, props.projectName, receipt.id, signal)
}
</script>

<template>
  <AIAttachmentReceipts :attachments="attachments" :load-attachment="loadAttachment" />
</template>
