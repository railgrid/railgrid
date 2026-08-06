<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { Check, Edit3, Loader2, MessageSquare, Plus, Trash2, X } from 'lucide-vue-next'
import { confirmDialog } from './portalkit/confirm'
import type { ProjectAssistantThread } from './types'

const props = withDefaults(defineProps<{
  threads: readonly ProjectAssistantThread[]
  activeThreadID: string
  disabled?: boolean
  busy?: boolean
  error?: string | null
}>(), {
  disabled: false,
  busy: false,
  error: null,
})

const emit = defineEmits<{
  select: [threadID: string]
  create: []
  rename: [threadID: string, title: string]
  delete: [threadID: string]
}>()

const editingThreadID = ref('')
const draftTitle = ref('')
const deletingThreadID = ref('')
const root = ref<HTMLElement | null>(null)

const editingThread = () => props.threads.find((thread) => thread.id === editingThreadID.value)

function displayTitle(thread: ProjectAssistantThread): string {
  return thread.title?.trim() || 'New thread'
}

function beginRename(thread: ProjectAssistantThread) {
  if (props.disabled || props.busy) return
  editingThreadID.value = thread.id
  draftTitle.value = displayTitle(thread) === 'New thread' ? '' : displayTitle(thread)
  void nextTick(() => {
    const input = root.value?.querySelector<HTMLInputElement>('input[aria-label="Thread name"]')
    input?.focus()
    input?.select()
  })
}

function cancelRename() {
  editingThreadID.value = ''
  draftTitle.value = ''
}

function commitRename() {
  const thread = editingThread()
  const title = draftTitle.value.trim()
  if (!thread || !title) return
  if (title === displayTitle(thread)) {
    cancelRename()
    return
  }
  emit('rename', thread.id, title)
  cancelRename()
}

async function requestDelete(thread: ProjectAssistantThread) {
  if (props.disabled || props.busy || deletingThreadID.value) return
  const title = displayTitle(thread)
  const confirmed = await confirmDialog({
    title: 'Delete thread?',
    message: `Delete “${title}”? The conversation history will be removed permanently.`,
    confirmLabel: 'Delete thread',
    danger: true,
  })
  if (!confirmed) return
  deletingThreadID.value = thread.id
  emit('delete', thread.id)
}

watch(() => props.busy, (busy) => {
  if (!busy) deletingThreadID.value = ''
})

watch(() => props.threads.map((thread) => `${thread.id}:${thread.title ?? ''}`).join('|'), () => {
  const thread = editingThread()
  if (thread && thread.title?.trim() === draftTitle.value.trim()) cancelRename()
})
</script>

<template>
  <div ref="root" class="flex h-full min-h-0 flex-col gap-3">
    <div class="flex min-w-0 items-start justify-between gap-3">
      <div class="flex min-w-0 items-start gap-2">
        <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay">
          <MessageSquare class="h-4 w-4 text-accent" :stroke-width="1.75" />
        </div>
        <div class="min-w-0">
          <h2 class="truncate text-[13px] font-semibold text-text-primary">Threads</h2>
          <p class="mt-0.5 text-[12px] leading-5 text-text-muted">Switch conversations or start a new one.</p>
        </div>
      </div>
      <button
        type="button"
        class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-accent/30 bg-accent/10 px-2.5 text-[12px] font-medium text-accent transition hover:bg-accent/20 disabled:cursor-not-allowed disabled:opacity-50"
        :disabled="disabled || busy"
        title="New thread"
        @click="emit('create')"
      >
        <Loader2 v-if="busy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
        <Plus v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
        New thread
      </button>
    </div>

    <div v-if="error" class="rounded-md border border-danger/30 bg-danger-subtle px-3 py-2 text-[12px] leading-5 text-danger" role="alert">
      {{ error }}
    </div>

    <div v-if="threads.length" class="min-h-0 flex-1 overflow-auto rounded-md border border-border-subtle bg-surface">
      <ul class="divide-y divide-border-subtle" aria-label="Assistant threads">
        <li v-for="thread in threads" :key="thread.id" class="group p-1.5">
          <div
            class="flex min-w-0 items-center gap-2 rounded-md px-2 py-2 text-left transition"
            :class="activeThreadID === thread.id ? 'bg-accent-subtle text-text-primary' : 'hover:bg-surface-hover'"
          >
            <button
              type="button"
              class="min-w-0 flex-1 text-left outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
              :disabled="disabled || busy || activeThreadID === thread.id"
              :aria-current="activeThreadID === thread.id ? 'page' : undefined"
              :title="displayTitle(thread)"
              @click="emit('select', thread.id)"
            >
              <span v-if="editingThreadID !== thread.id" class="block truncate text-[13px] font-medium">
                {{ displayTitle(thread) }}
              </span>
              <span v-if="editingThreadID !== thread.id" class="mt-0.5 block truncate text-[11px] text-text-muted">
                {{ thread.status === 'active' ? 'Active' : thread.status === 'archived' ? 'Archived' : 'Conversation' }}
              </span>
            </button>

            <form v-if="editingThreadID === thread.id" class="flex min-w-0 flex-1 items-center gap-1" @submit.prevent="commitRename">
              <input
                v-model="draftTitle"
                class="h-7 min-w-0 flex-1 rounded border border-accent/50 bg-surface-overlay px-2 text-[12px] text-text-primary outline-none focus:border-accent"
                aria-label="Thread name"
                maxlength="120"
                @keydown.esc.prevent="cancelRename"
              />
              <button type="submit" class="flex h-7 w-7 shrink-0 items-center justify-center rounded text-accent hover:bg-accent-subtle disabled:opacity-50" :disabled="!draftTitle.trim() || busy" title="Save thread name" aria-label="Save thread name">
                <Check class="h-3.5 w-3.5" :stroke-width="2" />
              </button>
              <button type="button" class="flex h-7 w-7 shrink-0 items-center justify-center rounded text-text-muted hover:bg-surface-hover hover:text-text-primary" title="Cancel rename" aria-label="Cancel rename" @click="cancelRename">
                <X class="h-3.5 w-3.5" :stroke-width="1.75" />
              </button>
            </form>

            <template v-if="editingThreadID !== thread.id">
              <span v-if="activeThreadID === thread.id" class="h-1.5 w-1.5 shrink-0 rounded-full bg-accent" aria-label="Current thread" />
              <button
                type="button"
                class="flex h-7 w-7 shrink-0 items-center justify-center rounded text-text-muted opacity-0 transition hover:bg-surface-hover hover:text-text-primary focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-accent/50 group-hover:opacity-100 disabled:cursor-not-allowed disabled:opacity-30"
                :disabled="disabled || busy"
                title="Rename thread"
                :aria-label="`Rename ${displayTitle(thread)}`"
                @click="beginRename(thread)"
              >
                <Edit3 class="h-3.5 w-3.5" :stroke-width="1.75" />
              </button>
              <button
                type="button"
                class="flex h-7 w-7 shrink-0 items-center justify-center rounded text-text-muted opacity-0 transition hover:bg-danger-subtle hover:text-danger focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-accent/50 group-hover:opacity-100 disabled:cursor-not-allowed disabled:opacity-30"
                :disabled="disabled || busy || deletingThreadID === thread.id"
                title="Delete thread"
                :aria-label="`Delete ${displayTitle(thread)}`"
                @click="requestDelete(thread)"
              >
                <Loader2 v-if="deletingThreadID === thread.id" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
                <Trash2 v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
              </button>
            </template>
          </div>
        </li>
      </ul>
    </div>

    <div v-else class="flex min-h-0 flex-1 items-center justify-center rounded-md border border-dashed border-border-subtle bg-surface/70 p-6 text-center">
      <div class="max-w-xs">
        <MessageSquare class="mx-auto h-6 w-6 text-text-muted" :stroke-width="1.5" />
        <p class="mt-2 text-[13px] font-medium text-text-primary">No threads yet</p>
        <p class="mt-1 text-[12px] leading-5 text-text-muted">Start a thread to keep a separate conversation for this project.</p>
      </div>
    </div>
  </div>
</template>
