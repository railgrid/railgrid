<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { Check, CornerDownRight, Trash2, X } from 'lucide-vue-next'
import {
  ASSISTANT_MESSAGE_QUEUE_MAX_CONTENT_LENGTH,
  type QueuedAssistantMessage,
} from './assistantMessageQueue'
import ActionMenu, { type ActionMenuItem } from './portalkit/ActionMenu.vue'

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

const editingID = ref('')
const editContent = ref('')

function beginEdit(message: QueuedAssistantMessage) {
  editingID.value = message.id
  editContent.value = message.content
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

function handleDocumentKeydown(event: KeyboardEvent) {
  if (event.key !== 'Escape') return
  if (editingID.value) cancelEdit()
}

function queueMenuItems(): ActionMenuItem[] {
  return [
    { id: 'edit', label: `Edit queued message` },
    { id: 'toggle-queueing', label: props.queueingEnabled ? 'Turn off queueing' : 'Turn on queueing' },
  ]
}

function handleMenuSelect(message: QueuedAssistantMessage, id: string): void {
  if (id === 'edit') beginEdit(message)
  else if (id === 'toggle-queueing') emit('toggleQueueing')
}

onMounted(() => {
  document.addEventListener('keydown', handleDocumentKeydown)
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', handleDocumentKeydown)
})
</script>

<template>
  <section
    v-if="messages.length"
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
            class="app-studio-touch-target flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-accent transition hover:bg-accent-subtle focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:opacity-45"
            :disabled="!editContent.trim()"
            aria-label="Save queued message"
            @click="saveEdit(message)"
          >
            <Check class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
          </button>
          <button
            type="button"
            class="app-studio-touch-target flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
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
            class="app-studio-touch-target inline-flex h-7 shrink-0 items-center gap-1 rounded-md px-2 text-[11px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-wait disabled:opacity-60"
            :disabled="!!steeringID"
            :aria-label="`Steer: ${message.content}`"
            @click="$emit('steer', message)"
          >
            <CornerDownRight class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
            {{ steeringID === message.id ? 'Steering…' : 'Steer' }}
          </button>
          <button
            type="button"
            class="app-studio-touch-target flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-danger focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-45"
            :disabled="!!steeringID"
            :aria-label="`Delete queued message: ${message.content}`"
            @click="$emit('remove', message)"
          >
            <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
          </button>
          <ActionMenu
            :label="`Actions for queued message: ${message.content}`"
            :items="queueMenuItems()"
            @select="handleMenuSelect(message, $event)"
          />
        </template>
      </li>
    </ol>
  </section>
</template>
