<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { Image, Loader2, RotateCcw, X } from 'lucide-vue-next'
import { assistantAttachmentIsImage, type AssistantAttachmentStatus } from './assistantAttachments'

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
  <div
    class="group relative h-24 w-24 shrink-0 overflow-hidden rounded-md border border-border-subtle bg-surface"
    :class="status === 'error' ? 'border-danger/50' : status === 'ready' ? 'border-accent/40' : ''"
    :title="error || label"
    role="group"
    :aria-label="statusLabel() ? `${label}, ${statusLabel()}` : label"
  >
    <img v-if="previewURL" :src="previewURL" :alt="label" class="h-full w-full object-cover" />
    <div v-else class="flex h-full w-full items-center justify-center bg-surface-raised text-text-muted" aria-hidden="true">
      <Image class="h-6 w-6" :stroke-width="1.5" />
    </div>
    <div v-if="status !== 'staged' && status !== 'ready'" class="absolute inset-0 bg-surface/55" aria-hidden="true" />
    <Loader2
      v-if="status === 'uploading' || status === 'deleting'"
      class="absolute left-1 top-1 h-3.5 w-3.5 animate-spin text-text-primary motion-reduce:animate-none"
      :stroke-width="1.75"
      aria-hidden="true"
    />
    <div v-if="statusLabel()" class="absolute inset-x-0 bottom-0 bg-surface-overlay/90 px-1 py-0.5 text-center" aria-live="polite">
      <span class="block truncate text-[10px] font-medium leading-3" :class="status === 'error' ? 'text-danger' : 'text-text-primary'">{{ statusLabel() }}</span>
    </div>
    <button
      v-if="status === 'error' && retryable"
      type="button"
      class="app-studio-touch-target absolute bottom-1 left-1 z-10 inline-flex h-6 w-6 items-center justify-center rounded-sm bg-surface-overlay/90 text-text-primary hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-60"
      :aria-label="retryAction === 'delete' ? `Retry attachment removal ${label}` : `Retry attachment upload ${label}`"
      :title="retryAction === 'delete' ? 'Retry removal' : 'Retry upload'"
      @click="emit('retry')"
    >
      <RotateCcw class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
    </button>
    <button
      type="button"
      class="app-studio-touch-target absolute right-1 top-1 z-10 inline-flex h-6 w-6 items-center justify-center rounded-full bg-surface-overlay/90 text-text-primary hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-60"
      :disabled="status === 'deleting'"
      :aria-label="`Remove attachment ${label}`"
      title="Remove attachment"
      @click="emit('remove')"
    >
      <X class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
    </button>
  </div>
</template>
