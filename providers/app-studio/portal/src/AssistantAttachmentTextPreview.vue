<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import {
  assistantAttachmentIsText,
  readAssistantAttachmentTextPreview,
  type AssistantAttachmentTextPreview as AssistantAttachmentTextPreviewValue,
} from './assistantAttachments'

const props = defineProps<{
  file: File
  label: string
}>()

const preview = ref<AssistantAttachmentTextPreviewValue | null>(null)
const loading = ref(false)
const failed = ref(false)
let loadSerial = 0

async function loadPreview(file: File) {
  const serial = ++loadSerial
  preview.value = null
  failed.value = false
  if (!assistantAttachmentIsText(file)) {
    loading.value = false
    return
  }
  loading.value = true
  try {
    const next = await readAssistantAttachmentTextPreview(file)
    if (serial !== loadSerial) return
    preview.value = next
  } catch {
    if (serial !== loadSerial) return
    failed.value = true
  } finally {
    if (serial === loadSerial) loading.value = false
  }
}

watch(() => props.file, (file) => { void loadPreview(file) }, { immediate: true })

onBeforeUnmount(() => { loadSerial += 1 })
</script>

<template>
  <div
    v-if="assistantAttachmentIsText(file)"
    class="mt-1.5 min-w-0 rounded-sm border border-border-subtle bg-surface px-2 py-1.5"
    :aria-label="`${label} text preview`"
    :aria-busy="loading ? 'true' : undefined"
  >
    <div class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Preview</div>
    <p class="mt-0.5 max-h-10 overflow-hidden whitespace-pre-wrap break-words text-[11px] leading-4 text-text-secondary">
      <span v-if="loading">Reading preview…</span>
      <span v-else-if="failed">Preview unavailable</span>
      <template v-else-if="preview"><span>{{ preview.text }}</span><span v-if="preview.truncated" aria-hidden="true">…</span></template>
    </p>
  </div>
</template>
