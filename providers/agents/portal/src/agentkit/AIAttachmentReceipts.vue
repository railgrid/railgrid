<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies;
  edit here and run `make sync-portalkit`.
-->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { File as FileIcon, FileText, Image, Loader2 } from 'lucide-vue-next'
import type { AgentKitAttachmentReceipt } from './composer'
import { assistantAttachmentContentTypeKind, assistantAttachmentSizeLabel } from './composerAttachments'

const props = defineProps<{
  attachments: AgentKitAttachmentReceipt[]
  loadAttachment?: (receipt: AgentKitAttachmentReceipt, signal: AbortSignal) => Promise<Blob>
}>()

type ImagePreview = { status: 'loading' | 'ready' | 'error'; digest: string; url?: string; error?: string }
const previews = ref<Record<string, ImagePreview>>({})
const controllers = new Map<string, AbortController>()
const isImageReceipt = (attachment: AgentKitAttachmentReceipt) => assistantAttachmentContentTypeKind(attachment.contentType, attachment.sizeBytes) === 'image'
const images = computed(() => props.attachments.filter(isImageReceipt))
const signature = computed(() => props.attachments.map((attachment) => `${attachment.id}:${attachment.sha256}`).join('\u0000'))

function release(id: string) {
  controllers.get(id)?.abort()
  controllers.delete(id)
  const preview = previews.value[id]
  if (preview?.url) URL.revokeObjectURL(preview.url)
  if (preview) {
    const next = { ...previews.value }
    delete next[id]
    previews.value = next
  }
}

function set(id: string, preview: ImagePreview) {
  previews.value = { ...previews.value, [id]: preview }
}

async function load(receipt: AgentKitAttachmentReceipt) {
  if (!props.loadAttachment) {
    set(receipt.id, { status: 'error', digest: receipt.sha256, error: 'Preview unavailable' })
    return
  }
  const controller = new AbortController()
  controllers.set(receipt.id, controller)
  set(receipt.id, { status: 'loading', digest: receipt.sha256 })
  try {
    const blob = await props.loadAttachment(receipt, controller.signal)
    const url = URL.createObjectURL(blob)
    if (controllers.get(receipt.id) !== controller) {
      URL.revokeObjectURL(url)
      return
    }
    controllers.delete(receipt.id)
    set(receipt.id, { status: 'ready', digest: receipt.sha256, url })
  } catch (error) {
    if (controller.signal.aborted || controllers.get(receipt.id) !== controller) return
    controllers.delete(receipt.id)
    set(receipt.id, { status: 'error', digest: receipt.sha256, error: error instanceof Error ? error.message : 'Preview unavailable' })
  }
}

function sync() {
  const ids = new Set(images.value.map((attachment) => attachment.id))
  for (const id of Object.keys(previews.value)) if (!ids.has(id)) release(id)
  for (const receipt of images.value) {
    const existing = previews.value[receipt.id]
    if (existing && existing.digest !== receipt.sha256) release(receipt.id)
    if (!previews.value[receipt.id] && !controllers.has(receipt.id)) void load(receipt)
  }
}

function retryPreview(receipt: AgentKitAttachmentReceipt) {
  release(receipt.id)
  void load(receipt)
}

watch(signature, sync, { immediate: true })
onBeforeUnmount(() => {
  for (const id of new Set([...controllers.keys(), ...Object.keys(previews.value)])) release(id)
})
</script>

<template>
  <div v-if="attachments.length" class="flex max-w-full flex-wrap justify-start gap-1.5" role="group" aria-label="Attachments">
    <template v-for="attachment in attachments" :key="attachment.id">
      <div
        v-if="isImageReceipt(attachment)"
        class="k-ai-attachment-receipt"
        :title="`${attachment.filename} · ${attachment.contentType} · ${assistantAttachmentSizeLabel(attachment.sizeBytes)}`"
        :aria-label="`${attachment.filename} image attachment`"
        role="group"
        :aria-busy="previews[attachment.id]?.status === 'loading'"
      >
        <img v-if="previews[attachment.id]?.status === 'ready' && previews[attachment.id]?.url" :src="previews[attachment.id]?.url" :alt="attachment.filename" class="k-ai-attachment-receipt__image" />
        <div v-else-if="previews[attachment.id]?.status === 'loading'" class="k-ai-attachment-receipt__loading" role="status">
          <Loader2 class="h-5 w-5 k-spin" :stroke-width="1.75" aria-hidden="true" />
          <span class="sr-only">Loading {{ attachment.filename }}</span>
        </div>
        <div v-else class="k-ai-attachment-receipt__failed">
          <div class="k-ai-attachment-receipt__failure-status" role="status">
            <Image :stroke-width="1.75" aria-hidden="true" />
            <span>{{ previews[attachment.id]?.error || 'Preview unavailable' }}</span>
          </div>
          <button
            v-if="loadAttachment"
            type="button"
            class="k-agentkit-touch-target k-ai-attachment-receipt__retry"
            :aria-label="`Retry image preview for ${attachment.filename}`"
            @click="retryPreview(attachment)"
          >
            Retry preview
          </button>
        </div>
      </div>
      <div v-else class="flex min-w-0 max-w-full flex-col rounded-sm border border-border-subtle bg-surface-raised px-2 py-1 text-[11px] font-mono text-text-secondary" :title="`${attachment.contentType} · ${assistantAttachmentSizeLabel(attachment.sizeBytes)}`" role="group" :aria-label="`${attachment.filename} file attachment`">
        <div class="inline-flex min-w-0 items-center gap-1.5">
          <FileIcon v-if="assistantAttachmentContentTypeKind(attachment.contentType, attachment.sizeBytes) === 'file'" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
          <FileText v-else class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
          <span class="max-w-56 truncate">{{ attachment.filename }}</span>
          <span v-if="assistantAttachmentContentTypeKind(attachment.contentType, attachment.sizeBytes) === 'file'" class="shrink-0 text-[10px] text-text-muted">{{ assistantAttachmentSizeLabel(attachment.sizeBytes) }}</span>
        </div>
        <div v-if="assistantAttachmentContentTypeKind(attachment.contentType, attachment.sizeBytes) === 'file'" class="mt-1 text-[10px] font-sans text-text-muted" role="note">Stored file; contents unavailable for analysis</div>
      </div>
    </template>
  </div>
</template>
