<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { Image, Loader2, RotateCcw, X } from 'lucide-vue-next'
import { assistantAttachmentIsImage, type AssistantAttachmentStatus } from './composerAttachments'

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

const emit = defineEmits<{
  remove: []
  retry: []
}>()

const errorID = crypto.randomUUID()
const previewURL = ref('')
let previewFile: File | null = null

function revokePreview() {
  if (previewURL.value && typeof URL !== 'undefined' && typeof URL.revokeObjectURL === 'function') {
    URL.revokeObjectURL(previewURL.value)
  }
  previewURL.value = ''
  previewFile = null
}

function syncPreview(file: File) {
  if (previewFile === file && previewURL.value) return
  revokePreview()
  if (!assistantAttachmentIsImage(file) || typeof URL === 'undefined' || typeof URL.createObjectURL !== 'function') return
  previewFile = file
  previewURL.value = URL.createObjectURL(file)
}

function statusLabel(): string {
  if (props.status === 'uploading') return 'Uploading'
  if (props.status === 'deleting') return 'Removing'
  if (props.status === 'error') return props.retryAction === 'delete' ? 'Removal failed' : props.retryAction === 'upload' ? 'Upload failed' : 'Cannot attach'
  return ''
}

watch(() => props.file, syncPreview, { immediate: true })

onBeforeUnmount(revokePreview)
</script>

<template>
  <div class="k-agentkit-attachment-draft" role="group" :aria-label="statusLabel() ? `${label}, ${statusLabel()}` : label" :aria-describedby="status === 'error' && error ? errorID : undefined">
    <div
      class="k-agentkit-attachment-preview"
      :class="status === 'error' ? 'k-agentkit-attachment-preview--error' : status === 'ready' ? 'k-agentkit-attachment-preview--ready' : ''"
      :title="error || label"
    >
      <img v-if="previewURL" :src="previewURL" :alt="label" class="k-agentkit-attachment-preview__image" />
      <div v-else class="k-agentkit-attachment-preview__placeholder" aria-hidden="true">
        <Image class="k-agentkit-attachment-preview__placeholder-icon" :stroke-width="1.5" />
      </div>
      <div v-if="status !== 'staged' && status !== 'ready'" class="k-agentkit-attachment-preview__overlay" aria-hidden="true" />
      <Loader2
        v-if="status === 'uploading' || status === 'deleting'"
        class="k-agentkit-attachment-preview__spinner animate-spin motion-reduce:animate-none"
        :stroke-width="1.75"
        aria-hidden="true"
      />
      <div v-if="statusLabel()" class="k-agentkit-attachment-preview__status" aria-live="polite">
        <span>{{ statusLabel() }}</span>
      </div>
      <button
        v-if="status === 'error' && retryable"
        type="button"
        class="k-agentkit-attachment-preview__button k-agentkit-attachment-preview__retry k-agentkit-touch-target"
        :aria-label="retryAction === 'delete' ? `Retry attachment removal ${label}` : `Retry attachment upload ${label}`"
        :title="retryAction === 'delete' ? 'Retry removal' : 'Retry upload'"
        @click="emit('retry')"
      >
        <RotateCcw class="k-agentkit-attachment-preview__icon" :stroke-width="1.75" aria-hidden="true" />
      </button>
      <button
        type="button"
        class="k-agentkit-attachment-preview__button k-agentkit-attachment-preview__remove k-agentkit-touch-target"
        :disabled="status === 'deleting'"
        :aria-label="`Remove attachment ${label}`"
        title="Remove attachment"
        @click="emit('remove')"
      >
        <X class="k-agentkit-attachment-preview__icon" :stroke-width="1.75" aria-hidden="true" />
      </button>
    </div>
    <p v-if="status === 'error' && error" :id="errorID" class="k-agentkit-attachment-error" role="status">{{ error }}</p>
  </div>
</template>
