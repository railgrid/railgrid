<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { FileText, Paperclip, RotateCcw, X, Plus } from 'lucide-vue-next'
import {
  ASSISTANT_ATTACHMENT_ACCEPT,
  ASSISTANT_LARGE_PASTE_BYTES,
  assistantAttachmentIsImage,
  assistantAttachmentValidationError,
  newAssistantStagedAttachment,
  type AssistantStagedAttachment,
} from './assistantAttachments'
import AssistantAttachmentPreview from './AssistantAttachmentPreview.vue'
import AssistantAttachmentTextPreview from './AssistantAttachmentTextPreview.vue'
import { useDismissibleAddMenu } from './useDismissibleAddMenu'
import { useAssistantFilePickerFocus } from './useAssistantFilePickerFocus'

const props = withDefaults(defineProps<{
  modelValue: string
  attachments?: AssistantStagedAttachment[]
  disabled?: boolean
  placeholder?: string
  inputId?: string
  error?: string
  attachmentOnly?: boolean
}>(), {
  attachments: () => [],
  disabled: false,
  placeholder: 'Describe what you want to build…',
  inputId: 'pre-project-prompt',
  error: '',
  attachmentOnly: false,
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
  'add-attachment': [attachment: AssistantStagedAttachment]
  'remove-attachment': [clientID: string]
  'retry-attachment': [clientID: string]
  'close-menu': []
  submit: []
}>()

const rootRef = ref<HTMLDivElement | null>(null)
const addMenuRootRef = ref<HTMLDivElement | null>(null)
const attachmentMenuTriggerRef = ref<HTMLButtonElement | null>(null)
const editorRef = ref<HTMLTextAreaElement | null>(null)
const attachmentMenuOpen = ref(false)
const attachmentInputRef = ref<HTMLInputElement | null>(null)

const { waitForPicker, restorePickerFocus } = useAssistantFilePickerFocus(() => {
  const editor = editorRef.value
  if (editor && !editor.disabled) return editor
  return attachmentMenuTriggerRef.value || rootRef.value
})

function focus() {
  const target = editorRef.value || rootRef.value
  target?.focus()
}

function setSelectionRange(start: number, end: number) {
  editorRef.value?.setSelectionRange(start, end)
}

defineExpose({ focus, setSelectionRange })

function toggleAttachmentMenu() {
  if (props.disabled) return
  if (attachmentMenuOpen.value) {
    closeAttachmentMenu()
    return
  }
  attachmentMenuOpen.value = true
}

function closeAttachmentMenu() {
  attachmentMenuOpen.value = false
  emit('close-menu')
}

function openAttachmentPicker() {
  if (props.disabled) return
  // Closing through the shared path also dismisses a nested Import menu owned
  // by the landing surface. This keeps Files and Import mutually exclusive.
  closeAttachmentMenu()
  const input = attachmentInputRef.value
  if (!input) return
  input.value = ''
  waitForPicker()
  input.click()
}

function stageFile(file: File, existing: readonly AssistantStagedAttachment[] = props.attachments): AssistantStagedAttachment | null {
  if (props.disabled) return null
  const staged = newAssistantStagedAttachment(file, assistantAttachmentValidationError(file, existing) || undefined)
  emit('add-attachment', staged)
  return staged
}

function handleAttachmentInput(event: Event) {
  const input = event.target instanceof HTMLInputElement ? event.target : null
  const files = input?.files ? Array.from(input.files) : []
  let existing = [...props.attachments]
  for (const file of files) {
    const staged = stageFile(file, existing)
    if (!staged) continue
    existing = [...existing, staged]
  }
  if (input) input.value = ''
  restorePickerFocus()
}

function clipboardImageFile(event: ClipboardEvent): File | null {
  const imageItem = Array.from(event.clipboardData?.items || [])
    .find((item) => item.kind === 'file' && item.type.trim().toLowerCase().startsWith('image/'))
  const file = imageItem?.getAsFile()
  if (!file) return null
  const type = file.type.trim().toLowerCase()
  const extension = type === 'image/jpeg' ? 'jpg' : type === 'image/webp' ? 'webp' : 'png'
  return file.name.trim() ? file : new File([file], `clipboard.${extension}`, { type: file.type || `image/${extension === 'jpg' ? 'jpeg' : extension}` })
}

function handlePaste(event: ClipboardEvent) {
  if (props.disabled) return
  const image = clipboardImageFile(event)
  if (image) {
    event.preventDefault()
    stageFile(image)
    return
  }

  const text = event.clipboardData?.getData('text/plain') || ''
  if (!text || new TextEncoder().encode(text).byteLength <= ASSISTANT_LARGE_PASTE_BYTES) return
  event.preventDefault()
  stageFile(new File([text], 'pasted-text.txt', { type: 'text/plain' }))
}

function handleInput(event: Event) {
  const target = event.target instanceof HTMLTextAreaElement ? event.target : null
  if (target) emit('update:modelValue', target.value)
}

function statusLabel(attachment: AssistantStagedAttachment): string {
  if (attachment.status === 'staged') return 'Ready to attach'
  if (attachment.status === 'uploading') return 'Uploading'
  if (attachment.status === 'deleting') return 'Removing'
  if (attachment.status === 'error') return attachment.retryAction === 'delete' ? 'Removal failed' : attachment.retryAction === 'upload' ? 'Upload failed' : 'Cannot attach'
  return 'Ready'
}

function attachmentLabel(attachment: AssistantStagedAttachment): string {
  return attachment.receipt?.filename || attachment.file.name || 'attachment'
}

useDismissibleAddMenu({
  open: attachmentMenuOpen,
  root: addMenuRootRef,
  trigger: attachmentMenuTriggerRef,
  onClose: closeAttachmentMenu,
})

onBeforeUnmount(() => {
  closeAttachmentMenu()
})
</script>

<template>
  <div
    ref="rootRef"
    class="relative flex flex-col rounded-lg border border-border-subtle bg-surface-raised shadow-sm transition focus-within:border-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
    :class="attachmentOnly ? 'min-h-[64px]' : 'min-h-[132px]'"
    :tabindex="attachmentOnly ? 0 : undefined"
    :role="attachmentOnly ? 'group' : undefined"
    :aria-label="attachmentOnly ? 'Paste an image or large text file to attach' : undefined"
    @paste.self="handlePaste"
  >
    <div v-if="attachments.length" class="relative z-10 flex flex-wrap gap-1.5 px-3 pt-2.5" aria-label="Staged attachments">
      <template v-for="attachment in attachments" :key="attachment.clientID">
        <AssistantAttachmentPreview
          v-if="assistantAttachmentIsImage(attachment.file)"
          :file="attachment.file"
          :label="attachmentLabel(attachment)"
          :status="attachment.status"
          :error="attachment.error"
          :retryable="attachment.retryable"
          :retry-action="attachment.retryAction"
          @retry="emit('retry-attachment', attachment.clientID)"
          @remove="emit('remove-attachment', attachment.clientID)"
        />
        <div
          v-else
          class="flex min-w-0 max-w-full flex-col items-stretch rounded-sm border px-2 py-1 text-[11px] font-mono"
          :class="attachment.status === 'error'
            ? 'border-danger/40 bg-danger-subtle text-danger'
            : attachment.status === 'ready'
              ? 'border-accent/30 bg-accent/10 text-accent'
              : 'border-border-subtle bg-surface text-text-secondary'"
          :title="attachment.error || statusLabel(attachment)"
        >
          <div class="flex min-w-0 items-center gap-1.5">
            <FileText class="h-3 w-3 shrink-0" :stroke-width="1.75" aria-hidden="true" />
            <span class="max-w-48 truncate">{{ attachmentLabel(attachment) }}</span>
            <span class="text-[10px] opacity-75">{{ statusLabel(attachment) }}</span>
            <div class="ml-auto flex shrink-0 items-center gap-0.5">
              <button
                v-if="attachment.status === 'error' && attachment.retryable"
                type="button"
                class="app-studio-touch-target inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-sm hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent"
                :aria-label="attachment.retryAction === 'delete' ? 'Retry attachment removal' : 'Retry attachment upload'"
                :title="attachment.retryAction === 'delete' ? 'Retry removal' : 'Retry upload'"
                @click="emit('retry-attachment', attachment.clientID)"
              >
                <RotateCcw class="h-3 w-3" :stroke-width="1.75" />
              </button>
              <button
                v-if="attachment.status !== 'deleting'"
                type="button"
                class="app-studio-touch-target inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-sm hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent"
                aria-label="Remove attachment"
                title="Remove attachment"
                @click="emit('remove-attachment', attachment.clientID)"
              >
                <X class="h-3 w-3" :stroke-width="1.75" />
              </button>
            </div>
          </div>
          <AssistantAttachmentTextPreview
            v-if="!assistantAttachmentIsImage(attachment.file)"
            :file="attachment.file"
            :label="attachmentLabel(attachment)"
          />
        </div>
      </template>
    </div>
    <textarea
      v-if="!attachmentOnly"
      ref="editorRef"
      :id="inputId"
      :value="modelValue"
      class="min-h-[72px] w-full flex-1 resize-none border-0 bg-transparent px-5 pb-12 pt-4 text-[16px] leading-7 text-text-primary outline-none placeholder:text-text-secondary md:text-[14px]"
      :placeholder="placeholder"
      :disabled="disabled"
      @input="handleInput"
      @paste="handlePaste"
      @keydown.ctrl.enter.prevent="emit('submit')"
      @keydown.meta.enter.prevent="emit('submit')"
    />
    <p v-if="error" class="px-5 pb-2 text-[12px] text-danger" role="alert">{{ error }}</p>
    <input ref="attachmentInputRef" type="file" class="hidden" :accept="ASSISTANT_ATTACHMENT_ACCEPT" multiple @change="handleAttachmentInput" />
    <div class="absolute bottom-2 left-1.5 right-2 flex min-w-0 items-center gap-2">
      <div class="flex min-w-0 items-center gap-0.5">
        <div ref="addMenuRootRef" class="contents">
          <div v-if="attachmentMenuOpen" class="absolute bottom-11 left-1 [z-index:var(--app-studio-z-menu)] min-w-48 rounded-md border border-border-default bg-surface-overlay p-1.5 shadow-lg" role="menu" aria-label="Add">
            <div class="px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-text-muted">Add</div>
            <button
              type="button"
              class="app-studio-touch-target flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-[12px] text-text-secondary hover:bg-surface-hover hover:text-text-primary"
              role="menuitem"
              @click="openAttachmentPicker"
            >
              <Paperclip class="h-3.5 w-3.5" :stroke-width="1.75" />
              Files
            </button>
            <slot name="menu" />
          </div>
          <button
            ref="attachmentMenuTriggerRef"
            type="button"
            class="app-studio-touch-target flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-45"
            :disabled="disabled"
            title="Add"
            aria-label="Add"
            aria-haspopup="menu"
            :aria-expanded="attachmentMenuOpen"
            @click="toggleAttachmentMenu"
          >
            <Plus class="h-4 w-4" :stroke-width="1.75" />
          </button>
        </div>
        <slot name="leading" />
      </div>
      <div class="ml-auto flex min-w-0 items-center justify-end gap-1">
        <slot name="actions" />
      </div>
    </div>
  </div>
</template>
