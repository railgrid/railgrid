<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { FileText, Image, Loader2 } from 'lucide-vue-next'
import type { FarosContext, ProjectAssistantAttachmentReceipt } from './types'
import { api } from './api'

const props = defineProps<{
  attachments: ProjectAssistantAttachmentReceipt[]
  ctx: FarosContext | null
  projectName: string
}>()

type ImagePreview = {
  status: 'loading' | 'ready' | 'error'
  digest: string
  url?: string
  error?: string
}

const imagePreviews = ref<Record<string, ImagePreview>>({})
const previewControllers = new Map<string, AbortController>()
let previewScope = ''

const imageAttachments = computed(() => props.attachments.filter((attachment) => attachment.contentType.startsWith('image/')))
const attachmentSignature = computed(() => props.attachments.map((attachment) => `${attachment.id}:${attachment.sha256}`).join('\u0000'))

function releasePreview(attachmentID: string) {
  previewControllers.get(attachmentID)?.abort()
  previewControllers.delete(attachmentID)
  const preview = imagePreviews.value[attachmentID]
  if (preview?.url) URL.revokeObjectURL(preview.url)
  if (preview) {
    const next = { ...imagePreviews.value }
    delete next[attachmentID]
    imagePreviews.value = next
  }
}

function setPreview(attachmentID: string, preview: ImagePreview) {
  imagePreviews.value = { ...imagePreviews.value, [attachmentID]: preview }
}

async function loadImagePreview(attachment: ProjectAssistantAttachmentReceipt) {
  if (!props.projectName.trim()) {
    setPreview(attachment.id, { status: 'error', digest: attachment.sha256, error: 'Attachment preview is unavailable.' })
    return
  }
  const controller = new AbortController()
  previewControllers.set(attachment.id, controller)
  setPreview(attachment.id, { status: 'loading', digest: attachment.sha256 })
  try {
    const blob = await api.getAssistantAttachment(props.ctx, props.projectName, attachment.id, controller.signal)
    const url = URL.createObjectURL(blob)
    if (previewControllers.get(attachment.id) !== controller) {
      URL.revokeObjectURL(url)
      return
    }
    previewControllers.delete(attachment.id)
    setPreview(attachment.id, { status: 'ready', digest: attachment.sha256, url })
  } catch (error) {
    if (controller.signal.aborted || previewControllers.get(attachment.id) !== controller) return
    previewControllers.delete(attachment.id)
    setPreview(attachment.id, {
      status: 'error',
      digest: attachment.sha256,
      error: error instanceof Error ? error.message : 'Attachment preview is unavailable.',
    })
  }
}

function syncImagePreviews() {
  const nextScope = `${props.projectName}\u0000${props.ctx?.token ?? ''}\u0000${props.ctx?.tenant ?? ''}\u0000${props.ctx?.orgUUID ?? ''}\u0000${props.ctx?.workspaceUUID ?? ''}`
  if (nextScope !== previewScope) {
    for (const attachmentID of new Set([...previewControllers.keys(), ...Object.keys(imagePreviews.value)])) releasePreview(attachmentID)
    previewScope = nextScope
  }
  const imageIDs = new Set(imageAttachments.value.map((attachment) => attachment.id))
  for (const attachmentID of Object.keys(imagePreviews.value)) {
    if (!imageIDs.has(attachmentID)) releasePreview(attachmentID)
  }
  for (const attachment of imageAttachments.value) {
    const preview = imagePreviews.value[attachment.id]
    if (preview && preview.digest !== attachment.sha256) releasePreview(attachment.id)
    if (!imagePreviews.value[attachment.id] && !previewControllers.has(attachment.id)) {
      void loadImagePreview(attachment)
    }
  }
}

watch([
  attachmentSignature,
  () => props.projectName,
  () => props.ctx?.token,
  () => props.ctx?.tenant,
  () => props.ctx?.orgUUID,
  () => props.ctx?.workspaceUUID,
], syncImagePreviews, { immediate: true })

onBeforeUnmount(() => {
  for (const attachmentID of new Set([...previewControllers.keys(), ...Object.keys(imagePreviews.value)])) {
    releasePreview(attachmentID)
  }
})
</script>

<template>
  <div v-if="attachments.length" class="flex max-w-full flex-wrap justify-end gap-1.5" aria-label="Attachments">
    <template v-for="attachment in attachments" :key="attachment.id">
      <div
        v-if="attachment.contentType.startsWith('image/')"
        class="relative h-24 w-32 overflow-hidden rounded-md border border-border-subtle bg-surface-raised"
        :title="`${attachment.filename} · ${attachment.contentType} · ${attachment.sizeBytes} bytes`"
        :aria-label="`${attachment.filename} image attachment`"
        :aria-busy="imagePreviews[attachment.id]?.status === 'loading'"
      >
        <img
          v-if="imagePreviews[attachment.id]?.status === 'ready' && imagePreviews[attachment.id]?.url"
          :src="imagePreviews[attachment.id]?.url"
          :alt="attachment.filename"
          class="h-full w-full object-cover"
        />
        <div v-else-if="imagePreviews[attachment.id]?.status === 'loading'" class="grid h-full place-items-center text-text-muted" role="status">
          <Loader2 class="h-5 w-5 animate-spin motion-reduce:animate-none" :stroke-width="1.75" aria-hidden="true" />
          <span class="sr-only">Loading {{ attachment.filename }}</span>
        </div>
        <div v-else class="grid h-full place-items-center gap-1 px-2 text-center text-[10px] text-text-muted" role="status">
          <Image class="h-4 w-4 text-accent" :stroke-width="1.75" aria-hidden="true" />
          <span>{{ imagePreviews[attachment.id]?.error || 'Preview unavailable' }}</span>
        </div>
      </div>
      <div
        v-else
        class="inline-flex min-w-0 max-w-full items-center gap-1.5 rounded-sm border border-border-subtle bg-surface-raised px-2 py-1 text-[11px] font-mono text-text-secondary"
        :title="`${attachment.contentType} · ${attachment.sizeBytes} bytes`"
      >
        <FileText class="h-3.5 w-3.5 shrink-0 text-accent" :stroke-width="1.75" />
        <span class="max-w-56 truncate">{{ attachment.filename }}</span>
      </div>
    </template>
  </div>
</template>
