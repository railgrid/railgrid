<script setup lang="ts">
import { Check, ChevronDown, Hammer, MessageCircle, RotateCcw, Sparkles, Trash2 } from 'lucide-vue-next'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

export type AssistantResponseMode = 'auto' | 'ask' | 'build'

export interface SuspendedTaskOption {
  id: string
  label: string
  reason: string
}

const props = defineProps<{
  mode: AssistantResponseMode
  suspendedTasks?: SuspendedTaskOption[]
  selectedTaskId?: string
  disabled?: boolean
}>()

const emit = defineEmits<{
  selectMode: [mode: AssistantResponseMode]
  selectTask: [id: string]
  discardTask: [id: string]
}>()

const root = ref<HTMLElement | null>(null)
const open = ref(false)
const selectedTask = computed(() => props.suspendedTasks?.find((task) => task.id === props.selectedTaskId))
const modeLabel = computed(() => {
  if (selectedTask.value) return `Continue: ${selectedTask.value.label}`
  if (props.mode === 'ask') return 'Discuss'
  if (props.mode === 'build') return 'Build'
  return 'Adaptive'
})

function chooseMode(mode: AssistantResponseMode) {
  if (props.disabled) return
  open.value = false
  emit('selectMode', mode)
}

function chooseTask(id: string) {
  if (props.disabled) return
  open.value = false
  emit('selectTask', id)
}

function discardTask(id: string) {
  if (props.disabled) return
  open.value = false
  emit('discardTask', id)
}

function closeFromOutside(event: PointerEvent) {
  if (root.value && !root.value.contains(event.target as Node)) open.value = false
}

function closeFromEscape(event: KeyboardEvent) {
  if (!open.value || event.key !== 'Escape') return
  open.value = false
  root.value?.querySelector<HTMLButtonElement>('[aria-haspopup="dialog"]')?.focus()
}

onMounted(() => {
  document.addEventListener('pointerdown', closeFromOutside)
  document.addEventListener('keydown', closeFromEscape)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', closeFromOutside)
  document.removeEventListener('keydown', closeFromEscape)
})
</script>

<template>
  <div ref="root" class="relative min-w-0 max-w-52">
    <button
      type="button"
      class="inline-flex h-8 max-w-full items-center gap-1.5 rounded-lg px-2 text-[11px] font-medium text-text-muted transition hover:bg-surface-hover hover:text-text-secondary disabled:cursor-not-allowed disabled:opacity-60"
      :disabled="disabled"
      aria-haspopup="dialog"
      :aria-expanded="open"
      :aria-label="`Response mode: ${modeLabel}`"
      @click="open = !open"
    >
      <RotateCcw v-if="selectedTask" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <MessageCircle v-else-if="mode === 'ask'" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <Hammer v-else-if="mode === 'build'" class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <Sparkles v-else class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
      <span class="truncate">{{ modeLabel }}</span>
      <ChevronDown class="h-3 w-3 shrink-0 transition" :class="{ 'rotate-180': open }" :stroke-width="1.75" />
    </button>

    <div
      v-if="open"
      role="dialog"
      aria-modal="false"
      aria-label="How should App Studio respond?"
      class="fixed inset-x-3 bottom-3 z-50 max-h-[calc(100dvh-1.5rem)] overflow-y-auto rounded-2xl border border-border-default bg-surface-overlay p-2 shadow-2xl md:absolute md:inset-x-auto md:bottom-10 md:left-0 md:max-h-[min(32rem,calc(100dvh-8rem))] md:w-[430px]"
    >
      <div class="px-2 pb-1 pt-0.5 text-[11px] text-text-muted">How should App Studio respond?</div>
      <button
        type="button"
        :aria-pressed="!selectedTaskId && mode === 'auto'"
        class="flex w-full items-start gap-3 rounded-xl px-2 py-2 text-left transition hover:bg-surface-hover"
        @click="chooseMode('auto')"
      >
        <Sparkles class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block text-[13px] font-medium text-text-primary">Adaptive</span>
          <span class="mt-0.5 block text-[12px] leading-4 text-text-muted">Answer directly, inspect safely, or propose work when needed.</span>
        </span>
        <Check v-if="!selectedTaskId && mode === 'auto'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
      </button>
      <button
        type="button"
        :aria-pressed="!selectedTaskId && mode === 'ask'"
        class="flex w-full items-start gap-3 rounded-xl px-2 py-2 text-left transition hover:bg-surface-hover"
        @click="chooseMode('ask')"
      >
        <MessageCircle class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block text-[13px] font-medium text-text-primary">Discuss</span>
          <span class="mt-0.5 block text-[12px] leading-4 text-text-muted">Discuss and inspect without changing project files.</span>
        </span>
        <Check v-if="!selectedTaskId && mode === 'ask'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
      </button>
      <button
        type="button"
        :aria-pressed="!selectedTaskId && mode === 'build'"
        class="flex w-full items-start gap-3 rounded-xl px-2 py-2 text-left transition hover:bg-surface-hover"
        @click="chooseMode('build')"
      >
        <Hammer class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
        <span class="min-w-0 flex-1">
          <span class="block text-[13px] font-medium text-text-primary">Build</span>
          <span class="mt-0.5 block text-[12px] leading-4 text-text-muted">Start a new implementation task.</span>
        </span>
        <Check v-if="!selectedTaskId && mode === 'build'" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
      </button>

      <template v-if="suspendedTasks?.length">
        <div class="mx-2 my-1 border-t border-border-subtle" />
        <div class="px-2 pb-1 pt-1 text-[10px] font-semibold uppercase tracking-wide text-text-muted">Continue previous work</div>
        <div
          v-for="task in suspendedTasks"
          :key="task.id"
          class="group flex items-start gap-1 rounded-xl transition hover:bg-surface-hover"
        >
          <button
            type="button"
            :aria-pressed="selectedTaskId === task.id"
            class="flex min-w-0 flex-1 items-start gap-3 px-2 py-2 text-left"
            @click="chooseTask(task.id)"
          >
            <RotateCcw class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
            <span class="min-w-0 flex-1">
              <span class="block truncate text-[13px] font-medium text-text-primary">{{ task.label }}</span>
              <span class="mt-0.5 block text-[12px] leading-4 text-text-muted">{{ task.reason }}</span>
            </span>
            <Check v-if="selectedTaskId === task.id" class="mt-0.5 h-4 w-4 shrink-0 text-text-secondary" :stroke-width="1.75" />
          </button>
          <button
            type="button"
            class="mr-1 mt-1.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-lg text-text-muted transition hover:bg-danger-subtle hover:text-danger"
            :aria-label="`Discard previous task: ${task.label}`"
            title="Discard task"
            @click="discardTask(task.id)"
          >
            <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" />
          </button>
        </div>
      </template>
    </div>
  </div>
</template>
