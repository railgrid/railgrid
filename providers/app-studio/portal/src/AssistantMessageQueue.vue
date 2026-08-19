<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { Check, CornerDownRight, Ellipsis, Pencil, Trash2, X } from 'lucide-vue-next'
import {
  ASSISTANT_MESSAGE_QUEUE_MAX_CONTENT_LENGTH,
  type QueuedAssistantMessage,
} from './assistantMessageQueue'

const props = withDefaults(defineProps<{
  messages: QueuedAssistantMessage[]
  steeringID?: string
  queueingEnabled?: boolean
}>(), {
  steeringID: '',
  queueingEnabled: true,
})

const emit = defineEmits<{
  steer: [message: QueuedAssistantMessage]
  remove: [message: QueuedAssistantMessage]
  edit: [message: QueuedAssistantMessage, content: string]
  toggleQueueing: []
}>()

const root = ref<HTMLElement | null>(null)
const openMenuID = ref('')
const editingID = ref('')
const editContent = ref('')

function closeMenu() {
  openMenuID.value = ''
}

function toggleMenu(message: QueuedAssistantMessage) {
  openMenuID.value = openMenuID.value === message.id ? '' : message.id
}

function beginEdit(message: QueuedAssistantMessage) {
  editingID.value = message.id
  editContent.value = message.content
  closeMenu()
}

function cancelEdit() {
  editingID.value = ''
  editContent.value = ''
}

function saveEdit(message: QueuedAssistantMessage) {
  const content = editContent.value.trim()
  if (!content) return
  emit('edit', message, content)
  cancelEdit()
}

function handleDocumentPointerDown(event: PointerEvent) {
  if (root.value?.contains(event.target as Node)) return
  closeMenu()
}

function handleDocumentKeydown(event: KeyboardEvent) {
  if (event.key !== 'Escape') return
  if (editingID.value) cancelEdit()
  else closeMenu()
}

onMounted(() => {
  document.addEventListener('pointerdown', handleDocumentPointerDown)
  document.addEventListener('keydown', handleDocumentKeydown)
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', handleDocumentPointerDown)
  document.removeEventListener('keydown', handleDocumentKeydown)
})
</script>

<template>
  <section
    v-if="messages.length"
    ref="root"
    class="relative z-10 -mb-px"
    aria-label="Queued messages"
    aria-live="polite"
  >
    <ol>
      <li
        v-for="message in messages"
        :key="message.id"
        class="relative -mt-px flex min-h-11 min-w-0 items-center gap-2 border border-border-subtle bg-surface px-3 py-2 first:mt-0 first:rounded-t-md"
      >
        <CornerDownRight class="h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
        <template v-if="editingID === message.id">
          <input
            v-model="editContent"
            class="h-8 min-w-0 flex-1 rounded-md border border-border-default bg-surface-raised px-2 text-[12px] text-text-primary outline-none transition focus:border-accent/60 focus:ring-2 focus:ring-accent/15"
            :maxlength="ASSISTANT_MESSAGE_QUEUE_MAX_CONTENT_LENGTH"
            aria-label="Edit queued message"
            autofocus
            @keydown.enter.prevent="saveEdit(message)"
          />
          <button
            type="button"
            class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-accent transition hover:bg-accent-subtle focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:opacity-45"
            :disabled="!editContent.trim()"
            aria-label="Save queued message"
            @click="saveEdit(message)"
          >
            <Check class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
          </button>
          <button
            type="button"
            class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
            aria-label="Cancel editing queued message"
            @click="cancelEdit"
          >
            <X class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
          </button>
        </template>
        <template v-else>
          <span class="min-w-0 flex-1 truncate text-[12px] text-text-primary" :title="message.content">{{ message.content }}</span>
          <button
            type="button"
            class="inline-flex h-7 shrink-0 items-center gap-1 rounded-md px-2 text-[11px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-wait disabled:opacity-60"
            :disabled="!!steeringID"
            :aria-label="`Steer: ${message.content}`"
            @click="$emit('steer', message)"
          >
            <CornerDownRight class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
            {{ steeringID === message.id ? 'Steering…' : 'Steer' }}
          </button>
          <button
            type="button"
            class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-danger focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-45"
            :disabled="!!steeringID"
            :aria-label="`Delete queued message: ${message.content}`"
            @click="$emit('remove', message)"
          >
            <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
          </button>
          <button
            type="button"
            class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
            :aria-expanded="openMenuID === message.id"
            :aria-label="`Queued message options: ${message.content}`"
            @click.stop="toggleMenu(message)"
          >
            <Ellipsis class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" />
          </button>
          <div
            v-if="openMenuID === message.id"
            class="absolute right-2 top-9 z-20 min-w-44 rounded-md border border-border-default bg-surface-overlay p-1 shadow-lg"
            role="menu"
          >
            <button
              type="button"
              class="flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-[12px] text-text-secondary transition hover:bg-surface-hover hover:text-text-primary"
              role="menuitem"
              @click="beginEdit(message)"
            >
              <Pencil class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
              Edit message
            </button>
            <button
              type="button"
              class="flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-[12px] text-text-secondary transition hover:bg-surface-hover hover:text-text-primary"
              role="menuitem"
              @click="closeMenu(); emit('toggleQueueing')"
            >
              <CornerDownRight class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
              {{ props.queueingEnabled ? 'Turn off queueing' : 'Turn on queueing' }}
            </button>
          </div>
        </template>
      </li>
    </ol>
  </section>
</template>
